// Package claudesettings adds and removes fixed permission rules by tier and
// default environment variables in a repo's .claude/settings.json (punt-kit
// permissions.md §6, tool-enable-disable §2.8). It never touches
// ~/.claude/settings.json.
//
// Each read-modify-write holds the repository's shared-local settings lock and
// stages its replacement in the shared-local temp dir (DES-079), so no lock or
// temp file is left in the checkout. Every key, permission entry, and env
// variable outside the owned set is kept, in its original order.
package claudesettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"unicode/utf8"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// ErrRefused marks a settings file left untouched because editing it is unsafe.
var ErrRefused = errors.New("refusing to edit .claude/settings.json")

// Preflight verifies that the settings target can accept rules without writing
// it. Enable uses this before changing any other repository state.
func Preflight(repoRoot string, rules map[string][]string) error {
	return PreflightWithEnv(repoRoot, rules, nil)
}

// PreflightWithEnv verifies that the settings target can accept rules and env
// defaults without writing it.
func PreflightWithEnv(repoRoot string, rules map[string][]string, env map[string]string) error {
	if err := refuseUnsafe(repoRoot); err != nil {
		return err
	}
	path := settingsPath(repoRoot)
	data, _, err := read(path)
	if err != nil {
		return err
	}
	if _, _, _, err := apply(data, rules, env, true); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrRefused, path, err)
	}
	return nil
}

// Register appends each rule to its permissions tier, creating the file when
// absent, and returns the rules it added.
func Register(repoRoot string, rules map[string][]string, p layout.Policy) ([]string, error) {
	added, _, err := update(repoRoot, rules, nil, p, true)
	return added, err
}

// RegisterWithEnv appends each rule to its permissions tier and sets each
// absent env default in one atomic write, creating the file when absent.
// Existing env entries are preserved so user overrides are never clobbered.
func RegisterWithEnv(repoRoot string, rules map[string][]string, env map[string]string, p layout.Policy) ([]string, []string, error) {
	return update(repoRoot, rules, env, p, true)
}

// Deregister removes every rule from its permissions tier and returns the rules
// it removed. A repo with no settings file is left as it is.
func Deregister(repoRoot string, rules map[string][]string, p layout.Policy) ([]string, error) {
	removed, _, err := DeregisterWithEnv(repoRoot, rules, nil, p)
	return removed, err
}

// DeregisterWithEnv removes every rule from its permissions tier and every env
// entry whose value still matches the owned default, in one atomic write. A
// repo with no settings file is left as it is.
func DeregisterWithEnv(repoRoot string, rules map[string][]string, env map[string]string, p layout.Policy) ([]string, []string, error) {
	if _, err := os.Lstat(settingsPath(repoRoot)); errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	return update(repoRoot, rules, env, p, false)
}

func settingsPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".claude", "settings.json")
}

func update(repoRoot string, rules map[string][]string, env map[string]string, p layout.Policy, grant bool) ([]string, []string, error) {
	if err := refuseUnsafe(repoRoot); err != nil {
		return nil, nil, err
	}
	path := settingsPath(repoRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	lockPath, err := p.LockPath("claudesettings", path)
	if err != nil {
		return nil, nil, err
	}
	if err := layout.EnsureLocalDir(p, filepath.Dir(lockPath)); err != nil {
		return nil, nil, fmt.Errorf("creating lock dir %s: %w", filepath.Dir(lockPath), err)
	}
	var changedRules, changedEnv []string
	err = withLock(lockPath, func() error {
		data, mode, err := read(path)
		if err != nil {
			return err
		}
		out, cr, ce, err := apply(data, rules, env, grant)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrRefused, path, err)
		}
		changedRules, changedEnv = cr, ce
		if len(cr) == 0 && len(ce) == 0 {
			return confirmUnchanged(p, path, data)
		}
		return writeAtomic(p, path, out, mode)
	})
	return changedRules, changedEnv, err
}

func refuseUnsafe(repoRoot string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home dir: %w", err)
	}
	if sameDir(repoRoot, home) {
		return fmt.Errorf("%w: the repository is your home directory", ErrRefused)
	}
	for _, p := range []string{filepath.Dir(settingsPath(repoRoot)), settingsPath(repoRoot)} {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symlink", ErrRefused, p)
		}
	}
	return nil
}

func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func read(path string) ([]byte, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0o644, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", path, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return data, fi.Mode().Perm(), nil
}

// apply returns the new file content, the rules added (grant) or removed, and
// the env entries set (grant) or removed.
func apply(data []byte, rules map[string][]string, env map[string]string, grant bool) ([]byte, []string, []string, error) {
	if !utf8.Valid(data) {
		return nil, nil, nil, errors.New("not valid UTF-8")
	}
	root, err := parseObject(data)
	if err != nil {
		return nil, nil, nil, err
	}
	var changedRules []string
	if len(rules) > 0 {
		perms, err := parseObject(root.get("permissions"))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("permissions: %w", err)
		}
		for _, tier := range []string{"allow", "ask"} {
			owned := rules[tier]
			if len(owned) == 0 {
				continue
			}
			var existing []string
			if raw := perms.get(tier); raw != nil {
				if err := json.Unmarshal(raw, &existing); err != nil || existing == nil {
					return nil, nil, nil, fmt.Errorf("permissions.%s is not a list of strings", tier)
				}
			}
			var tierChanged []string
			for _, rule := range owned {
				if slices.Contains(existing, rule) != grant {
					tierChanged = append(tierChanged, rule)
				}
			}
			if len(tierChanged) == 0 {
				continue
			}
			changedRules = append(changedRules, tierChanged...)
			if grant {
				existing = append(existing, tierChanged...)
			} else {
				existing = slices.DeleteFunc(existing, func(r string) bool {
					return slices.Contains(owned, r)
				})
			}
			if existing == nil {
				existing = []string{}
			}
			if err := perms.set(tier, existing); err != nil {
				return nil, nil, nil, err
			}
		}
		if len(changedRules) > 0 {
			if err := root.set("permissions", perms); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	var changedEnv []string
	if len(env) > 0 {
		envObj, err := parseObject(root.get("env"))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("env: %w", err)
		}
		for _, k := range slices.Sorted(maps.Keys(env)) {
			want := env[k]
			raw := envObj.get(k)
			var existing *string
			if raw != nil {
				if err := json.Unmarshal(raw, &existing); err != nil || existing == nil {
					return nil, nil, nil, fmt.Errorf("env.%s is not a string", k)
				}
			}
			if grant {
				if raw == nil {
					if err := envObj.set(k, want); err != nil {
						return nil, nil, nil, err
					}
					changedEnv = append(changedEnv, k+"="+want)
				}
			} else if existing != nil && *existing == want {
				envObj.delete(k)
				changedEnv = append(changedEnv, k+"="+want)
			}
		}
		if len(changedEnv) > 0 {
			if !grant && len(envObj) == 0 {
				root.delete("env")
			} else if err := root.set("env", envObj); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	if len(changedRules) == 0 && len(changedEnv) == 0 {
		return nil, nil, nil, nil
	}
	compact, err := marshal(root)
	if err != nil {
		return nil, nil, nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, nil, nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), changedRules, changedEnv, nil
}

type member struct {
	key   string
	value json.RawMessage
}

// object is a JSON object that keeps its members in file order.
type object []member

// parseObject decodes a JSON object; empty input is an empty object.
func parseObject(data []byte) (object, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return object{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	obj := object{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		if obj.get(key) != nil {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		obj = append(obj, member{key: key, value: raw})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON object")
	}
	return obj, nil
}

func (o object) get(key string) json.RawMessage {
	for _, m := range o {
		if m.key == key {
			return m.value
		}
	}
	return nil
}

func (o *object) set(key string, v any) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	for i := range *o {
		if (*o)[i].key == key {
			(*o)[i].value = raw
			return nil
		}
	}
	*o = append(*o, member{key: key, value: raw})
	return nil
}

func (o *object) delete(key string) {
	*o = slices.DeleteFunc(*o, func(m member) bool {
		return m.key == key
	})
}

// MarshalJSON writes the members in order.
func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := marshal(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshal encodes v without HTML escaping, so a rule like Bash(a && b)
// round-trips byte-for-byte.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func withLock(lockPath string, fn func() error) error {
	f, err := openLock(lockPath)
	if err != nil {
		return fmt.Errorf("opening lock %s: %w", lockPath, err)
	}
	defer func() { _ = f.Close() }()
	if err := flock(f); err != nil {
		return fmt.Errorf("locking %s: %w", lockPath, err)
	}
	defer func() { _ = funlock(f) }()
	return fn()
}

func writeAtomic(p layout.Policy, path string, data []byte, mode os.FileMode) error {
	tmp, err := layout.CreateLocalTemp(p, filepath.Base(path), "")
	if err != nil {
		return fmt.Errorf("creating staging file for %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting mode on %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", name, errors.Join(err, tmp.Close()))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", name, path, err)
	}
	if err := syncDestination(p, path); err != nil {
		return &layout.InstallDurabilityError{Path: path, Err: err}
	}
	return nil
}

// confirmUnchanged re-syncs an existing file a call leaves as is, under the same
// lock: matching bytes do not prove an earlier rename's directory sync completed.
func confirmUnchanged(p layout.Policy, path string, data []byte) error {
	if data == nil {
		return nil
	}
	if err := syncDestination(p, path); err != nil {
		return &layout.InstallDurabilityError{Path: path, Err: err}
	}
	return nil
}

var syncDestination = layout.SyncRepositoryDestination

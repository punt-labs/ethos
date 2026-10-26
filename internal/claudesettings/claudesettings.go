// Package claudesettings adds and removes a fixed set of permissions.allow
// rules in a repo's .claude/settings.json (punt-kit permissions.md §6,
// tool-enable-disable §2.8). It never touches ~/.claude/settings.json.
//
// The file is shared with every other punt tool, so each read-modify-write
// holds the tool-agnostic sibling lock .claude/.settings.json.punt-import.lock
// (the name every punt CLI takes) and replaces the file atomically. Every key
// and allow entry outside the owned rules is kept, in its original order.
package claudesettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"unicode/utf8"
)

// LockName is the shared sibling lock for .claude/settings.json.
const LockName = ".settings.json.punt-import.lock"

// ErrRefused marks a settings file left untouched because editing it is unsafe.
var ErrRefused = errors.New("refusing to edit .claude/settings.json")

// Register appends each rule missing from permissions.allow, creating the file
// when absent, and returns the rules it added.
func Register(repoRoot string, rules []string) ([]string, error) {
	return update(repoRoot, rules, true)
}

// Deregister removes every rule from permissions.allow and returns the rules it
// removed. A repo with no settings file is left as it is.
func Deregister(repoRoot string, rules []string) ([]string, error) {
	if _, err := os.Lstat(settingsPath(repoRoot)); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return update(repoRoot, rules, false)
}

func settingsPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".claude", "settings.json")
}

func update(repoRoot string, rules []string, grant bool) ([]string, error) {
	if err := refuseUnsafe(repoRoot); err != nil {
		return nil, err
	}
	path := settingsPath(repoRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	var changed []string
	err := withLock(filepath.Join(filepath.Dir(path), LockName), func() error {
		data, mode, err := read(path)
		if err != nil {
			return err
		}
		out, c, err := apply(data, rules, grant)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrRefused, path, err)
		}
		changed = c
		if len(c) == 0 {
			return nil
		}
		return writeAtomic(path, out, mode)
	})
	return changed, err
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

// apply returns the new file content and the rules added (grant) or removed.
func apply(data []byte, rules []string, grant bool) ([]byte, []string, error) {
	if !utf8.Valid(data) {
		return nil, nil, errors.New("not valid UTF-8")
	}
	root, err := parseObject(data)
	if err != nil {
		return nil, nil, err
	}
	perms, err := parseObject(root.get("permissions"))
	if err != nil {
		return nil, nil, fmt.Errorf("permissions: %w", err)
	}
	var allow []string
	if raw := perms.get("allow"); raw != nil {
		if err := json.Unmarshal(raw, &allow); err != nil {
			return nil, nil, errors.New("permissions.allow is not a list of strings")
		}
	}
	var changed []string
	for _, rule := range rules {
		if slices.Contains(allow, rule) == grant {
			continue
		}
		changed = append(changed, rule)
	}
	if len(changed) == 0 {
		return nil, nil, nil
	}
	if grant {
		allow = append(allow, changed...)
	} else {
		allow = slices.DeleteFunc(allow, func(r string) bool { return slices.Contains(rules, r) })
	}
	if allow == nil {
		allow = []string{}
	}
	if err := perms.set("allow", allow); err != nil {
		return nil, nil, err
	}
	if err := root.set("permissions", perms); err != nil {
		return nil, nil, err
	}
	compact, err := marshal(root)
	if err != nil {
		return nil, nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), changed, nil
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

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Chmod(name, mode); err != nil {
		return fmt.Errorf("setting mode on %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", name, path, err)
	}
	return nil
}

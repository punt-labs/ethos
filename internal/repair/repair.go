// Package repair removes a bounded set of obsolete repository trace files.
// It never migrates data, changes mission authority, or writes Git's index.
package repair

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// Item names one permanent deletion, not a migration or index operation.
type Item struct {
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Tracked bool   `json:"tracked"`
	sha     [32]byte
}

// Plan is an inventory snapshot. Apply refuses if the inventory changes while
// the operator reads the warning. Construct it with Inventory, not manually.
type Plan struct {
	Items  []Item `json:"items"`
	policy layout.Policy
	locks  []string
}

var capturedFull = regexp.MustCompile(`"record"\s*:\s*"full"`)

// Inventory is read-only, including when the local directory does not exist.
// Canonical local data, overlays, arbitrary artifacts and global state are not
// cleanup candidates. Unknown files are left alone, not guessed at.
func Inventory(ctx context.Context, p layout.Policy) (*Plan, error) {
	if _, err := p.LockPath("repair", "repository"); err != nil {
		return nil, err
	}
	plan := &Plan{policy: p, Items: []Item{}}
	roots := []string{p.RecordRoot()}
	if p.CheckoutRecordRoot() != p.RecordRoot() {
		roots = append(roots, p.CheckoutRecordRoot())
	}
	for _, root := range roots {
		tracked, err := trackedFiles(p, root)
		if err != nil {
			return nil, err
		}
		err = scan(ctx, root, func(path, rel string) error {
			remove, lock, err := obsolete(p, root, rel)
			if err != nil {
				return err
			}
			if lock {
				plan.locks = append(plan.locks, path)
			}
			if !remove {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			// Even damaged published logs with visible full consent are retained.
			if strings.Contains(filepath.Base(path), ".jsonl") && capturedFull.Match(b) {
				return nil
			}
			plan.Items = append(plan.Items, Item{Path: path, Bytes: int64(len(b)), Tracked: tracked[path], sha: sha256.Sum256(b)})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// Probe current locks, including pre-boundary live-file locks, but never
	// remove or move canonical-local data or its lock inodes.
	for _, family := range []string{"locks", "missions", "sessions"} {
		if err := scan(ctx, filepath.Join(p.LocalRoot(), family), func(path, _ string) error {
			if strings.HasSuffix(path, ".lock") {
				plan.locks = append(plan.locks, path)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Slice(plan.Items, func(i, j int) bool { return plan.Items[i].Path < plan.Items[j].Path })
	sort.Strings(plan.locks)
	return plan, nil
}

// Apply requires the caller's explicit permanent-loss confirmation AND assertion
// that old/new writers, hooks and MCP servers are stopped. Advisory locks cannot
// prevent a stopped old binary from being restarted. No locks are stolen.
// Deletions are incremental: on interruption, rerun Inventory and confirm again.
// removed reports actual deletions even on error; no rollback is promised.
func (p *Plan) Apply(ctx context.Context) (removed []string, err error) {
	if !supportsDeletion {
		return nil, fmt.Errorf("repair deletion is unsupported on Windows; use a Unix host with all writers stopped")
	}
	var held []*os.File
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			err = errors.Join(err, held[i].Close())
		}
	}()
	for _, path := range p.locks {
		if err = safePath(path); err != nil {
			return removed, err
		}
		var f *os.File
		f, err = os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return removed, fmt.Errorf("opening repair lock %s: %w", path, err)
		}
		held = append(held, f)
		if err = tryLock(f); err != nil {
			return removed, fmt.Errorf("busy or unavailable lock %s: %w; stop all writers and retry", path, err)
		}
	}
	current, err := Inventory(ctx, p.policy)
	if err != nil {
		return removed, err
	}
	if !reflect.DeepEqual(p, current) {
		return removed, fmt.Errorf("repair inventory changed; no files deleted; rerun and confirm the new inventory")
	}
	for _, item := range p.Items {
		if err = ctx.Err(); err != nil {
			return removed, err
		}
		if err = os.Remove(item.Path); err != nil {
			return removed, fmt.Errorf("deleting %s: %w; rerun repair for remaining files", item.Path, err)
		}
		removed = append(removed, item.Path)
	}
	return removed, nil
}

func obsolete(p layout.Policy, root, rel string) (remove, lock bool, err error) {
	a := strings.Split(rel, "/")
	base := a[len(a)-1]
	if rel == "missions.jsonl.lock" || len(a) == 2 && a[0] == "identities" && strings.HasSuffix(base, ".yaml.lock") {
		return true, true, nil
	}
	if len(a) < 2 || a[0] != "missions" && a[0] != "sessions" {
		return false, false, nil
	}
	if rel == "missions/.create.lock" {
		return true, true, nil
	}
	// Never claim agent-owned artifacts, even runtime-looking filenames.
	if len(a) >= 3 && a[0] == "missions" && (a[2] == "artifacts" || a[2] == "delegations" && !(len(a) == 5 && base == "prompt.md")) {
		return false, false, nil
	}
	if len(a) == 3 && a[0] == "missions" && base == ".lock" {
		return true, true, nil
	}
	if len(a) == 3 && a[0] == "missions" {
		for _, name := range []string{"contract.yaml", "results.yaml", "reflections.yaml", "corrections.yaml", "log.jsonl"} {
			if base == name+layout.TmpMarker || base == name+layout.LegacyTmpSuffix {
				return true, false, nil
			}
		}
	}
	if len(a) == 3 {
		ns := audit.MissionNS
		legacy := base == "log.jsonl"
		if a[0] == "sessions" {
			if len(a[1]) <= 11 || !audit.SessionDirMatches(a[1], a[1][11:]) {
				return false, false, nil
			}
			ns, legacy = audit.SessionNS, base == "audit.jsonl"
		}
		_, kind := audit.Classify(base, ns)
		diagnostic := kind == audit.KindCorrupt || kind == audit.KindQuarantine || kind == audit.KindHeldLoss
		// Quarantine deliberately publishes gap/watermark diagnostics without
		// captured record:full consent. Sessions have no authority record that
		// can distinguish these current publications from legacy diagnostics.
		if a[0] == "sessions" && diagnostic {
			return false, false, nil
		}
		if legacy || kind == audit.KindValid || diagnostic || kind == audit.KindTemp {
			if a[0] == "missions" && (legacy || kind == audit.KindValid || diagnostic) {
				retain, err := retainMissionTrace(p, root, a[1])
				return !retain, false, err
			}
			return true, false, nil
		}
	}
	if a[0] != "missions" {
		return false, false, nil
	}
	detail := len(a) == 3 && base == "reflections.yaml" || len(a) == 5 && base == "prompt.md" && a[2] == "delegations"
	if !detail {
		return false, false, nil
	}
	retain, err := retainMissionTrace(p, root, a[1])
	return !retain, false, err
}

// C can contain a publication without a sibling contract: current authority is
// owned by S. Explicit full exports instead use flat <id>.contract/result.yaml
// files and keep the events' original blame eligibility. Neither missing local
// authority nor the absence of record:full proves that a trace is obsolete.
func retainMissionTrace(p layout.Policy, root, id string) (bool, error) {
	roots := []string{p.RecordRoot()}
	if root != p.RecordRoot() {
		roots = append(roots, root)
	}
	for _, dir := range roots {
		for _, suffix := range []string{".contract.yaml", ".result.yaml"} {
			path := filepath.Join(dir, "missions", filepath.Base(id)+suffix)
			if err := safePath(path); err != nil {
				return true, err
			}
			if _, err := os.Stat(path); err == nil {
				return true, nil // exports, including incomplete/ambiguous ones
			} else if !errors.Is(err, os.ErrNotExist) {
				return true, fmt.Errorf("examining export record %s: %w", path, err)
			}
		}
	}
	legacy := false
	for _, dir := range roots {
		path := filepath.Join(dir, "missions", filepath.Base(id), "contract.yaml")
		if err := safePath(path); err != nil {
			return true, err
		}
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return true, fmt.Errorf("reading publication authority %s: %w", path, err)
		}
		decoder := yaml.NewDecoder(strings.NewReader(string(b)))
		var contract map[string]yaml.Node
		if err := decoder.Decode(&contract); err != nil || contract == nil {
			return true, fmt.Errorf("cannot distinguish current publication from legacy detail: inspect %s", path)
		}
		var extra yaml.Node
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return true, fmt.Errorf("cannot distinguish current publication from legacy detail: inspect %s", path)
		}
		if _, current := contract["round_decisions"]; current {
			return true, nil
		}
		legacy = true
	}
	return !legacy, nil
}

// scan never follows symlinks or enters nested repositories. Refusal is safer
// than claiming a complete inventory through a path we did not inspect.
func scan(ctx context.Context, root string, visit func(string, string) error) error {
	if err := safePath(root); err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if path == root && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.Name() == ".git" {
			return fmt.Errorf("repair cannot safely scan symlink or nested repository: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("repair cannot safely scan nonregular file: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return visit(path, filepath.ToSlash(rel))
	})
}

func safePath(path string) error {
	for d := path; ; d = filepath.Dir(d) {
		info, err := os.Lstat(d)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("repair refuses symlink: %s", d)
		}
		if filepath.Dir(d) == d {
			return nil
		}
	}
}

func trackedFiles(p layout.Policy, root string) (map[string]bool, error) {
	env := gitenv.Parsed()
	if root == p.CheckoutRecordRoot() {
		var err error
		env, err = gitenv.ActualIndexEnv()
		if err != nil {
			return nil, err
		}
	}
	checkout := filepath.Dir(filepath.Dir(root))
	out, err := gitenv.Run(gitenv.Spec{Git: "git", Dir: checkout, Env: env, Args: []string{"ls-files", "--cached", "-z", "--", ".punt-labs/ethos/"}})
	if err != nil {
		return nil, fmt.Errorf("reading tracked repair paths in %s: %w", checkout, err)
	}
	tracked := map[string]bool{}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" && filepath.IsLocal(rel) {
			tracked[filepath.Join(checkout, rel)] = true
		}
	}
	return tracked, nil
}

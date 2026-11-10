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
	"github.com/punt-labs/ethos/v4/internal/mission"
)

// Item names one permanent deletion, not a migration or index operation.
type Item struct {
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Tracked bool   `json:"tracked"`
	sha     [32]byte
}

// SHA256 is the digest of the file's bytes as Inventory read them. A deleter
// compares it to a fresh digest under its lock so a file that changed since
// the inventory is never removed.
func (i Item) SHA256() [32]byte { return i.sha }

// Plan is an inventory snapshot. Apply refuses if the inventory changes
// between Inventory and the moment its locks are held. Construct it with
// Inventory, not manually. Skipped lists items a busy lock deferred.
type Plan struct {
	Items   []Item   `json:"items"`
	Skipped []string `json:"skipped,omitempty"`
	// Unreadable lists obsolete-looking files Inventory could not read. They
	// are not Items: nothing is claimed about what they hold.
	Unreadable []string `json:"unreadable,omitempty"`
	policy     layout.Policy
	locks      []string
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
				plan.Unreadable = append(plan.Unreadable, path)
				return nil
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

// Apply deletes the inventoried files. A lock held by a running process is
// never stolen: the files it guards (and the lock itself) are skipped, listed
// in Skipped, and left for a later run. The inventory is re-read under the
// locks; if it changed, nothing is deleted. removed reports actual deletions
// even on error.
func (p *Plan) Apply(ctx context.Context) (removed []string, err error) {
	var held []*os.File
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			err = errors.Join(err, held[i].Close())
		}
	}()
	busy := map[string]bool{}
	for _, path := range p.locks {
		if err = safePath(path); err != nil {
			return removed, err
		}
		var f *os.File
		f, err = os.OpenFile(path, os.O_RDWR, 0)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
			continue
		}
		if err != nil {
			return removed, fmt.Errorf("opening lock %s: %w", path, err)
		}
		held = append(held, f)
		if lerr := TryLock(f); lerr != nil {
			busy[path] = true
		}
	}
	current, err := Inventory(ctx, p.policy)
	if err != nil {
		return removed, err
	}
	// Items and locks are both re-derived: a lock created after the first
	// inventory was never probed, so its items cannot be known idle.
	if !reflect.DeepEqual(p.Items, current.Items) || !reflect.DeepEqual(p.locks, current.locks) {
		return removed, fmt.Errorf("repair inventory changed while locks were taken; nothing deleted")
	}
	p.Skipped = nil
	for _, item := range p.Items {
		if err = ctx.Err(); err != nil {
			return removed, err
		}
		if p.guardedByBusyLock(item.Path, busy) {
			p.Skipped = append(p.Skipped, item.Path)
			continue
		}
		if err = os.Remove(item.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, fmt.Errorf("deleting %s: %w", item.Path, err)
		}
		err = nil
		removed = append(removed, item.Path)
	}
	return removed, nil
}

// guardedByBusyLock reports whether path is a busy lock, belongs to a mission
// whose old directory lock or current mission-state lock is busy, or is a
// session chunk whose stream lock is busy.
func (p *Plan) guardedByBusyLock(path string, busy map[string]bool) bool {
	if len(busy) == 0 {
		return false
	}
	if busy[path] {
		return true
	}
	for _, root := range []string{p.policy.RecordRoot(), p.policy.CheckoutRecordRoot()} {
		rel, err := filepath.Rel(root, path)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		a := strings.Split(filepath.ToSlash(rel), "/")
		if len(a) < 3 {
			continue
		}
		if a[0] == "sessions" {
			if lock, err := StreamLockPath(p.policy, rel); err == nil && busy[lock] {
				return true
			}
			continue
		}
		if a[0] != "missions" {
			continue
		}
		if busy[filepath.Join(root, "missions", a[1], ".lock")] {
			return true
		}
		if lock, err := p.policy.LockPath("mission-state", a[1]); err == nil && busy[lock] {
			return true
		}
	}
	return false
}

// StreamLockPath is the current audit stream lock that guards the record-tree
// trace at rel (slash-separated, relative to a record root): the session
// stream for sessions/<date>-<sid>/<file>, the mission stream for
// missions/<id>/<chunk> where the chunk names its session. A path that names
// no stream (a frozen log.jsonl, a reflection, a prompt) is an error.
func StreamLockPath(p layout.Policy, rel string) (string, error) {
	a := strings.Split(filepath.ToSlash(rel), "/")
	if len(a) != 3 {
		return "", fmt.Errorf("no audit stream for %s", rel)
	}
	var s audit.Stream
	switch a[0] {
	case "sessions":
		if len(a[1]) <= len(audit.SessionDateFormat)+1 {
			return "", fmt.Errorf("no audit stream for %s", rel)
		}
		s = audit.Stream{Namespace: audit.SessionNS, Session: a[1][len(audit.SessionDateFormat)+1:]}
	case "missions":
		cn, kind := audit.Classify(a[2], audit.MissionNS)
		if kind != audit.KindValid && kind != audit.KindCorrupt && kind != audit.KindQuarantine {
			return "", fmt.Errorf("no audit stream for %s", rel)
		}
		s = audit.Stream{Namespace: audit.MissionNS, Mission: a[1], Session: cn.Session}
	default:
		return "", fmt.Errorf("no audit stream for %s", rel)
	}
	if err := s.Validate(); err != nil {
		return "", err
	}
	return p.LockPath("audit-stream", s.Key())
}

// ErrNotRegular is returned by Obsolete for a working entry that is not a plain
// regular file reached without a symlink.
var ErrNotRegular = errors.New("not a regular file")

// refuseSymlinkBelow returns ErrNotRegular when a directory component of rel
// under root is a symlink. root itself is trusted: it may sit below a system
// symlink such as macOS /var, which safePath (it walks to /) would refuse.
func refuseSymlinkBelow(root, rel string) error {
	d := root
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		d = filepath.Join(d, part)
		info, err := os.Lstat(d)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrNotRegular, rel)
		}
	}
	return nil
}

// retainedByContent reports whether a published log's bytes keep it out of
// cleanup: even damaged logs with visible full consent are retained. Inventory
// and Obsolete share this one rule.
func retainedByContent(b []byte, rel string) bool {
	return strings.Contains(filepath.Base(rel), ".jsonl") && capturedFull.Match(b)
}

// Obsolete reports whether Inventory would list the file at rel, a path relative
// to root (a record root of p), as obsolete. Callers that remove files from
// another place, such as the index, use it so both agree on what is retained.
// When the working file is gone (ErrNotExist) and fallback is non-nil, the
// retain test runs on the bytes fallback returns, typically the indexed blob;
// fallback's error is returned as is. Any other read error is returned wrapped.
// A working entry that is a symlink (or sits below one) or is not a regular
// file is never opened, since reading a FIFO or /dev/zero through a committed
// link would block or exhaust memory: Obsolete returns ErrNotRegular.
func Obsolete(p layout.Policy, root, rel string, fallback func() ([]byte, error)) (bool, error) {
	remove, _, err := obsolete(p, root, rel)
	if err != nil || !remove {
		return false, err
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := refuseSymlinkBelow(root, rel); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return false, fmt.Errorf("%w: %s", ErrNotRegular, rel)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && fallback != nil {
		b, err = fallback()
	}
	if err != nil {
		return false, err
	}
	return !retainedByContent(b, rel), nil
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
	// A live session stream is never a publication, even in a directory whose
	// name cannot be matched to a session. Whether it is retained for visible
	// full consent is decided by content, as for every other published log.
	if len(a) == 3 && a[0] == "sessions" && base == "audit.jsonl" && (len(a[1]) <= 11 || !audit.SessionDirMatches(a[1], a[1][11:])) {
		return true, false, nil
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

// TraceClass is one verdict on a mission's record-tree trace file.
type TraceClass int

const (
	// TraceLegacyDetail: an older release wrote it and nothing current
	// publishes it. Archiving it locally and deleting it loses nothing.
	TraceLegacyDetail TraceClass = iota + 1
	// TraceCurrentPublication: the current writers publish this file in this
	// repository, or an explicit export sits beside it. It is never deleted.
	TraceCurrentPublication
	// TraceAuthority: the mission's only round authority (a reflections
	// aggregate beside a contract without round_decisions). It stays until
	// shared authority exists.
	TraceAuthority
)

// ClassifyReflections says what a mission's missions/<id>/reflections.yaml
// is. It decides from the aggregate and the contract, never from the current
// record mode: mode changes affect future eligibility only, and never delete
// published history (docs/storage-layout.md).
//
// Two writers leave two different shared decision ids. The legacy import
// (importLegacyDecisions) derives each id from the aggregate entry itself,
// hex(sha256(json(entry))[:16]); a live reflect allocates a random operation
// id, and in full mode also publishes the aggregate. So the aggregate is
// legacy detail only when every round it holds has a contract decision whose
// id is that round's legacy-import hash. Any other id means a live reflect
// decided that round: the aggregate is its publication and stays.
//
// A contract without round_decisions (absent or null) makes the aggregate
// the mission's round authority; an export record beside the mission makes
// it a publication; a missing or unreadable contract is an error.
func ClassifyReflections(p layout.Policy, root, id string) (TraceClass, error) {
	exported, err := HasExport(p, id)
	if err != nil {
		return 0, err
	}
	if exported {
		return TraceCurrentPublication, nil
	}
	roots := []string{p.RecordRoot()}
	if root != p.RecordRoot() {
		roots = append(roots, root)
	}
	var contract *mission.Contract
	for _, dir := range roots {
		path := filepath.Join(dir, "missions", filepath.Base(id), "contract.yaml")
		if err := safePath(path); err != nil {
			return 0, err
		}
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("reading publication authority %s: %w", path, err)
		}
		c, err := mission.DecodeContract(b)
		if err != nil {
			return 0, fmt.Errorf("cannot distinguish current publication from legacy detail; left in place: %s", path)
		}
		if contract == nil || c.RoundDecisions != nil && contract.RoundDecisions == nil {
			contract = c
		}
	}
	if contract == nil {
		return 0, fmt.Errorf("mission %s has no contract in either record root; reflections.yaml cannot be classified", id)
	}
	if contract.RoundDecisions == nil {
		return TraceAuthority, nil
	}
	aggPath := filepath.Join(root, "missions", filepath.Base(id), "reflections.yaml")
	if err := safePath(aggPath); err != nil {
		return 0, err
	}
	data, err := os.ReadFile(aggPath)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", aggPath, err)
	}
	rs, err := mission.DecodeLegacyReflections(data, id)
	if err != nil {
		return 0, fmt.Errorf("cannot distinguish current publication from legacy detail; left in place: %s", aggPath)
	}
	for _, r := range rs {
		d := contract.DecisionFor(r.Round)
		if d == nil || d.ID != mission.LegacyDecisionID(r) {
			return TraceCurrentPublication, nil
		}
	}
	return TraceLegacyDetail, nil
}

// HasExport reports whether mission id has an explicit export record
// (<id>.contract.yaml or <id>.result.yaml) in either record tree. Exports and
// the trace beside them are never claimed.
func HasExport(p layout.Policy, id string) (bool, error) {
	roots := []string{p.RecordRoot()}
	if c := p.CheckoutRecordRoot(); c != p.RecordRoot() {
		roots = append(roots, c)
	}
	for _, dir := range roots {
		for _, suffix := range []string{".contract.yaml", ".result.yaml"} {
			path := filepath.Join(dir, "missions", filepath.Base(id)+suffix)
			if err := safePath(path); err != nil {
				return true, err
			}
			if _, err := os.Stat(path); err == nil {
				return true, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return true, fmt.Errorf("examining export record %s: %w", path, err)
			}
		}
	}
	return false, nil
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
			return true, fmt.Errorf("cannot distinguish current publication from legacy detail; left in place: %s", path)
		}
		var extra yaml.Node
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return true, fmt.Errorf("cannot distinguish current publication from legacy detail; left in place: %s", path)
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

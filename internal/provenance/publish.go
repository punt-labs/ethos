package provenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

// Receipt separates checkout installation from actual-index success. On any
// failure callers receive the receipt AND an error; copied files are retained
// for an explicit retry, never rolled back over another writer's work.
type Receipt struct {
	Manifest       string   `json:"manifest"`
	Head           string   `json:"head"`
	Index          string   `json:"index"`
	Copied         []string `json:"copied"`
	IndexInstalled bool     `json:"index_installed"`
	Durability     string   `json:"durability"`
}

// Publisher has an operation-scoped immutable policy and Git environment. The
// unexported checkpoints are deterministic failure/concurrency injection points.
type Publisher struct {
	Policy              layout.Policy
	Repository          Repository
	afterSummaryCapture func() error
	beforeLock          func() error
	afterCopy           func(string) error
	renameRecord        func(string, string) error
	syncDir             func(string) error
}

type fileState struct {
	data   []byte
	exists bool
}

func readState(p string) (fileState, error) {
	info, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	if !info.Mode().IsRegular() {
		return fileState{}, fmt.Errorf("refusing nonregular publication target %s", p)
	}
	data, err := os.ReadFile(p)
	return fileState{data: data, exists: true}, err
}
func sameState(a, b fileState) bool { return a.exists == b.exists && bytes.Equal(a.data, b.data) }

func (p Publisher) Publish(ctx context.Context, snapshot mission.PublicationSnapshot, base string) (receipt Receipt, retErr error) {
	receipt.Durability = "not_applicable"
	snapshot, err := mission.RedactPublicationSnapshot(filepath.Dir(filepath.Dir(p.Policy.RecordRoot())), snapshot)
	if err != nil {
		return receipt, fmt.Errorf("invalid selected snapshot: %w", err)
	}
	r := p.Repository
	checkout := filepath.Dir(filepath.Dir(p.Policy.CheckoutRecordRoot()))
	if p.Policy.RecordRoot() == "" || r.Root != checkout || r.Env == nil {
		return receipt, fmt.Errorf("publication requires matching checkout and immutable repository policy/environment")
	}
	// No mission/delegation lock is acquired here. Serialize summary capture and
	// replacement with its established writers, including when S and C coincide.
	err = mission.WithSummaryLock(filepath.Dir(filepath.Dir(p.Policy.RecordRoot())), func() error {
		var publishErr error
		receipt, publishErr = p.publishLocked(ctx, snapshot, base)
		return publishErr
	})
	return receipt, err
}

func (p Publisher) publishLocked(ctx context.Context, snapshot mission.PublicationSnapshot, base string) (receipt Receipt, retErr error) {
	receipt.Durability = "not_applicable"
	r := p.Repository
	head, err := r.Resolve(ctx, "HEAD")
	if err != nil {
		return receipt, err
	}
	receipt.Head = head
	baseSHA, err := r.Resolve(ctx, base)
	if err != nil {
		return receipt, err
	}
	if _, err := r.git(ctx, "merge-base", "--is-ancestor", baseSHA, head); err != nil {
		return receipt, fmt.Errorf("publication base must be an ancestor: %w", err)
	}
	indexOut, err := r.git(ctx, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return receipt, err
	}
	index, err := gitenv.IndexPathOutput(indexOut)
	if err != nil {
		return receipt, fmt.Errorf("reading actual index path: %w", err)
	}
	if !filepath.IsAbs(index) {
		return receipt, fmt.Errorf("git did not return an absolute actual index")
	}
	receipt.Index = index
	original, err := readState(index)
	if err != nil {
		return receipt, err
	}
	if err := layout.EnsureLocalDir(p.Policy, p.Policy.TempDir()); err != nil {
		return receipt, err
	}
	private, err := os.MkdirTemp(p.Policy.TempDir(), layout.ScratchDirPattern("publication"))
	if err != nil {
		return receipt, err
	}
	defer func() {
		if err := os.RemoveAll(private); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("removing local publication staging: %w", err))
		}
	}()
	candidate := filepath.Join(private, "index")
	if original.exists {
		if err := os.WriteFile(candidate, original.data, 0o600); err != nil {
			return receipt, err
		}
	}
	cr := Repository{Root: r.Root, Env: gitenv.WithIndex(r.Env, candidate)}
	if !original.exists {
		if _, err := cr.git(ctx, "read-tree", "--empty"); err != nil {
			return receipt, err
		}
	}
	// A split index resolves its shared file relative to the index itself.
	shared, err := r.git(ctx, "rev-parse", "--shared-index-path")
	if err != nil {
		return receipt, err
	}
	if len(shared) > 0 {
		name, err := gitenv.IndexPathOutput(shared)
		if err != nil {
			return receipt, fmt.Errorf("reading shared index path: %w", err)
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(r.Root, name)
		}
		state, err := readState(name)
		if err != nil {
			return receipt, fmt.Errorf("reading split index %s: %w", name, err)
		}
		if !state.exists {
			return receipt, fmt.Errorf("split index missing: %s", name)
		}
		if err := os.WriteFile(filepath.Join(private, filepath.Base(name)), state.data, 0o600); err != nil {
			return receipt, err
		}
		if _, err := cr.git(ctx, "update-index", "--no-split-index"); err != nil {
			return receipt, err
		}
	}
	m := Manifest{SchemaVersion: 1, BranchHead: head, Base: baseSHA, Missions: append([]string(nil), snapshot.Missions...)}
	files := map[string][]byte{}
	for _, record := range snapshot.Records {
		if !safePath(record.Path) || files[record.Path] != nil {
			return receipt, fmt.Errorf("invalid/duplicate publication record %q", record.Path)
		}
		files[record.Path] = bytes.Clone(record.Data)
		m.Records = append(m.Records, Record{Path: record.Path, SHA256: Digest(record.Data)})
		if strings.Contains(record.Path, "/delegations/") {
			var d mission.Delegation
			if err := yaml.Unmarshal(record.Data, &d); err != nil {
				return receipt, err
			}
			a := Association{Mission: d.Mission, Delegation: d.ID}
			if pairPath(a) != record.Path || d.AgentType == "" {
				return receipt, fmt.Errorf("delegation path/identity mismatch: %s", record.Path)
			}
			m.Associations = append(m.Associations, a)
		}
	}
	rows, order, err := SummaryRows(snapshot.Summary)
	if err != nil {
		return receipt, err
	}
	for _, id := range order {
		m.Summaries = append(m.Summaries, Summary{Mission: id, SHA256: Digest(rows[id])})
	}
	sort.Slice(m.Records, func(i, j int) bool { return m.Records[i].Path < m.Records[j].Path })
	sort.Slice(m.Associations, func(i, j int) bool { return pairKey(m.Associations[i]) < pairKey(m.Associations[j]) })
	if err := cr.BuildSources(ctx, &m); err != nil {
		return receipt, err
	}
	name, data, err := Encode(m)
	if err != nil {
		return receipt, err
	}
	receipt.Manifest = name
	files[name] = data
	// A summary has independent checkout and index unions. Unrelated unstaged
	// summary rows must not be staged as a side effect of publication.
	if err := safeParents(r.Root, SummaryPath, false); err != nil {
		return receipt, err
	}
	workSummary, err := readState(filepath.Join(r.Root, filepath.FromSlash(SummaryPath)))
	if err != nil {
		return receipt, err
	}
	if p.afterSummaryCapture != nil {
		if err := p.afterSummaryCapture(); err != nil {
			return receipt, err
		}
	}
	indexedSummary, err := cr.optionalIndex(ctx, SummaryPath)
	if err != nil {
		return receipt, err
	}
	workMerged, err := MergeSummary(workSummary.data, snapshot.Summary)
	if err != nil {
		return receipt, err
	}
	indexMerged, err := MergeSummary(indexedSummary.data, snapshot.Summary)
	if err != nil {
		return receipt, err
	}
	// Existing unrelated rows bypass selected-snapshot redaction. Refuse unsafe
	// final payloads before any staging or checkout writes; never rewrite those
	// rows as a side effect of publishing a different mission.
	storeRoot := filepath.Dir(filepath.Dir(p.Policy.RecordRoot()))
	if err := mission.GuardPublicationSummary(storeRoot, workMerged); err != nil {
		return receipt, fmt.Errorf("merged checkout summary: %w", err)
	}
	if err := mission.GuardPublicationSummary(storeRoot, indexMerged); err != nil {
		return receipt, fmt.Errorf("merged index summary: %w", err)
	}
	if len(workMerged) > 0 || workSummary.exists || indexedSummary.exists {
		files[SummaryPath] = workMerged
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	before := map[string]fileState{}
	for _, path := range paths {
		if err := safeParents(r.Root, path, false); err != nil {
			return receipt, err
		}
		work := workSummary
		if path != SummaryPath {
			work, err = readState(filepath.Join(r.Root, filepath.FromSlash(path)))
			if err != nil {
				return receipt, err
			}
		}
		before[path] = work
		if path != SummaryPath {
			staged, err := cr.optionalIndex(ctx, path)
			if err != nil {
				return receipt, err
			}
			base, err := r.optionalCommit(ctx, head, path)
			if err != nil {
				return receipt, err
			}
			desired := fileState{data: files[path], exists: true}
			if !sameState(staged, base) && !sameState(staged, desired) {
				return receipt, fmt.Errorf("selected indexed target diverges: %s", path)
			}
			if !sameState(work, base) && !sameState(work, desired) {
				return receipt, fmt.Errorf("selected checkout target diverges: %s", path)
			}
		}
		indexed := files[path]
		if path == SummaryPath {
			indexed = indexMerged
		}
		if err := cr.stageBytes(ctx, path, indexed); err != nil {
			return receipt, err
		}
	}
	if err := cr.verifyManifest(ctx, ":", name, &m); err != nil {
		return receipt, fmt.Errorf("candidate index: %w", err)
	}
	if p.beforeLock != nil {
		if err := p.beforeLock(); err != nil {
			return receipt, err
		}
	}
	// The only adjacent temporary is Git's mandatory actual index.lock. Never
	// create a fallback record-tree temporary, and never restore original index.
	lock, err := os.OpenFile(index+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return receipt, fmt.Errorf("acquiring actual index lock: %w", err)
	}
	locked := true
	defer func() {
		if locked {
			if err := lock.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				retErr = errors.Join(retErr, err)
			}
			if err := os.Remove(index + ".lock"); err != nil {
				retErr = errors.Join(retErr, err)
			}
		}
	}()
	checkIndex := func() error {
		now, err := readState(index)
		if err != nil {
			return err
		}
		if !sameState(now, original) {
			return fmt.Errorf("actual index changed since capture; refusing CAS installation")
		}
		return nil
	}
	if err := checkIndex(); err != nil {
		return receipt, err
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return receipt, err
		}
		if err := safeParents(r.Root, path, true); err != nil {
			return receipt, err
		}
		now, err := readState(filepath.Join(r.Root, filepath.FromSlash(path)))
		if err != nil {
			return receipt, err
		}
		if !sameState(now, before[path]) {
			return receipt, fmt.Errorf("selected checkout target changed during publication: %s", path)
		}
		if now.exists && bytes.Equal(now.data, files[path]) {
			// A prior attempt may have installed this entry but failed its
			// directory sync. Retrying must complete durability, not skip it.
			if err := p.syncParents(filepath.Join(r.Root, filepath.FromSlash(path))); err != nil {
				return receipt, fmt.Errorf("confirming checkout durability at %s: %w", path, err)
			}
			continue
		}
		installed, err := p.installFile(path, files[path])
		if installed {
			receipt.Copied = append(receipt.Copied, path)
		}
		if err != nil {
			return receipt, fmt.Errorf("checkout copy incomplete at %s (actual index not installed): %w", path, err)
		}
		if p.afterCopy != nil {
			if err := p.afterCopy(path); err != nil {
				return receipt, err
			}
		}
	}
	if err := checkIndex(); err != nil {
		return receipt, err
	}
	latest, err := r.Resolve(ctx, "HEAD")
	if err != nil {
		return receipt, err
	}
	if latest != head {
		return receipt, fmt.Errorf("branch head changed during publication")
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	candidateBytes, err := os.ReadFile(candidate)
	if err != nil {
		return receipt, err
	}
	if n, err := lock.Write(candidateBytes); err != nil {
		return receipt, fmt.Errorf("writing actual index lock: %w", err)
	} else if n != len(candidateBytes) {
		return receipt, fmt.Errorf("writing actual index lock: %w", io.ErrShortWrite)
	}
	if err := lock.Sync(); err != nil {
		return receipt, fmt.Errorf("syncing actual index lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		return receipt, err
	}
	if err := os.Rename(index+".lock", index); err != nil {
		return receipt, fmt.Errorf("installing actual index: %w", err)
	}
	locked = false
	receipt.IndexInstalled = true
	receipt.Durability = "unconfirmed"
	if err := p.syncParents(index); err != nil {
		return receipt, fmt.Errorf("actual index installed; crash durability unconfirmed: %w", err)
	}
	receipt.Durability = directoryDurability
	return receipt, nil
}

func safeParents(root, rel string, create bool) error {
	if !safePath(rel) {
		return fmt.Errorf("unsafe publication path %q", rel)
	}
	dir := root
	for _, part := range strings.Split(pathDir(rel), "/") {
		if part == "." {
			continue
		}
		dir = filepath.Join(dir, filepath.Base(part))
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			if !create {
				return nil
			}
			if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = os.Lstat(dir)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("publication ancestor is not a real directory: %s", dir)
		}
	}
	return nil
}
func pathDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

func (p Publisher) installFile(rel string, data []byte) (installed bool, retErr error) {
	f, err := layout.CreateLocalTemp(p.Policy, "publication", ".record")
	if err != nil {
		return false, err
	}
	defer func() {
		if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			retErr = errors.Join(retErr, err)
		}
		if err := os.Remove(f.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			retErr = errors.Join(retErr, err)
		}
	}()
	if n, err := f.Write(data); err != nil {
		return false, err
	} else if n != len(data) {
		return false, io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	rename := p.renameRecord
	if rename == nil {
		rename = os.Rename
	}
	dest := filepath.Join(p.Repository.Root, filepath.FromSlash(rel))
	if err := rename(f.Name(), dest); err != nil {
		return false, err
	}
	return true, p.syncParents(dest)
}

func (p Publisher) syncParents(name string) error {
	sync := p.syncDir
	if sync == nil {
		sync = syncDirectory
	}
	for dir := filepath.Dir(name); ; dir = filepath.Dir(dir) {
		if err := sync(dir); err != nil {
			return err
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}

func (r Repository) stageBytes(ctx context.Context, path string, data []byte) error {
	out, _, err := gitenv.Read(gitenv.Spec{Git: "git", Dir: r.Root, Env: r.Env, Args: []string{"hash-object", "-w", "--stdin"}, Stdin: bytes.NewReader(data)}, 0)
	if err != nil {
		return err
	}
	sha := strings.TrimSpace(string(out))
	if !objectID(sha) {
		return fmt.Errorf("invalid blob identity")
	}
	_, err = r.git(ctx, "update-index", "--add", "--cacheinfo", "100644,"+sha+","+path)
	return err
}

func (r Repository) optionalIndex(ctx context.Context, p string) (fileState, error) {
	list, err := r.git(ctx, "ls-files", "--stage", "-z", "--", p)
	if err != nil {
		return fileState{}, err
	}
	if len(list) == 0 {
		return fileState{}, nil
	}
	entries := strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00")
	if len(entries) != 1 {
		return fileState{}, fmt.Errorf("unmerged selected index target %s", p)
	}
	meta, name, ok := strings.Cut(entries[0], "\t")
	fields := strings.Fields(meta)
	if !ok || name != p || len(fields) != 3 || fields[0] != "100644" || fields[2] != "0" {
		return fileState{}, fmt.Errorf("unsupported selected index target %s", p)
	}
	data, err := r.read(ctx, ":", p)
	return fileState{data: data, exists: true}, err
}

func (r Repository) optionalCommit(ctx context.Context, ref, p string) (fileState, error) {
	out, err := r.git(ctx, "ls-tree", "-z", ref, "--", p)
	if err != nil {
		return fileState{}, err
	}
	if len(out) == 0 {
		return fileState{}, nil
	}
	entries := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if len(entries) != 1 {
		return fileState{}, fmt.Errorf("unsupported selected tree target %s", p)
	}
	meta, name, ok := strings.Cut(entries[0], "\t")
	fields := strings.Fields(meta)
	if !ok || name != p || len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" {
		return fileState{}, fmt.Errorf("unsupported selected tree target %s", p)
	}
	data, err := r.read(ctx, ref, p)
	return fileState{data: data, exists: true}, err
}

func (r Repository) record(ctx context.Context, ref, p string) ([]byte, error) {
	var state fileState
	var err error
	if ref == ":" {
		state, err = r.optionalIndex(ctx, p)
	} else {
		state, err = r.optionalCommit(ctx, ref, p)
	}
	if err != nil {
		return nil, err
	}
	if !state.exists {
		return nil, fmt.Errorf("required record missing: %s", p)
	}
	return state.data, nil
}

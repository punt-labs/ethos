package hook

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"golang.org/x/sys/unix"
)

const (
	handoffBuildCacheBytes      int64 = 10 << 30
	handoffBuildCacheNamespaces       = 4
	handoffBuildCacheFiles            = 100_000
	handoffBuildCacheMount            = "/gocache"
)

var errHandoffBuildCacheUnsafe = errors.New("build cache tree is unsafe or over budget")

type buildCacheGeneration struct {
	Number uint64
	System string
	ID     string
}

type handoffBuildCache struct {
	loc        layout.Policy
	digest     string
	system     string
	lock       *os.File
	generation buildCacheGeneration
}

func openHandoffBuildCache(ctx context.Context, loc layout.Policy, digest, system string, exclusive bool) (snapshotBuildCache, bool, error) {
	c := &handoffBuildCache{loc: loc, digest: digest, system: system}
	if !validBuildCacheHex(digest, 32) {
		return nil, false, errors.New("missing or invalid build cache runtime digest")
	}
	if err := layout.EnsureLocalDir(loc, c.stateRoot()); err != nil {
		return nil, false, err
	}
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	lock, waited, err := lockBuildCache(ctx, filepath.Join(c.stateRoot(), digest+".lock"), mode, true)
	if err != nil {
		return nil, waited, err
	}
	c.lock = lock
	c.generation, err = c.readGeneration()
	if err == nil && exclusive && (c.generation.Number == 0 || c.generation.System != system) {
		err = c.advance(system)
	}
	if err != nil {
		return nil, waited, errors.Join(err, lock.Close())
	}
	return c, waited, nil
}

func lockBuildCache(ctx context.Context, path string, mode int, wait bool) (*os.File, bool, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, false, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, errors.Join(errors.New("not a regular cache lock"), err, f.Close())
	}
	waited := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, waited, errors.Join(err, f.Close())
		}
		err := unix.Flock(fd, mode|unix.LOCK_NB)
		if err == nil {
			return f, waited, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) || !wait {
			return nil, waited, errors.Join(err, f.Close())
		}
		waited = true
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func validBuildCacheHex(value string, size int) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == size && hex.EncodeToString(raw) == value
}

func (c *handoffBuildCache) close() { _ = c.lock.Close() }

func (c *handoffBuildCache) stateRoot() string {
	return filepath.Join(c.loc.LocalRoot(), "handoff-cache-state")
}

func (c *handoffBuildCache) cacheRoot() string {
	return filepath.Join(c.loc.LocalRoot(), "handoff-cache")
}

func (c *handoffBuildCache) namespace() string {
	return filepath.Join(c.cacheRoot(), c.digest)
}

func (c *handoffBuildCache) generationDir() string {
	return filepath.Join(c.namespace(), fmt.Sprintf("g%d", c.generation.Number))
}

func (c *handoffBuildCache) idFile() string {
	return filepath.Join(c.generationDir(), "id")
}

func (c *handoffBuildCache) keyGeneration() uint64 { return c.generation.Number }

func (c *handoffBuildCache) readGeneration() (buildCacheGeneration, error) {
	var generation buildCacheGeneration
	data, err := readBuildCacheFile(filepath.Join(c.stateRoot(), c.digest+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return generation, nil
	}
	if err != nil {
		return generation, err
	}
	if err := json.Unmarshal(data, &generation); err != nil {
		return generation, err
	}
	if generation.Number == 0 || (generation.ID != "" && !validBuildCacheHex(generation.ID, 16)) {
		return generation, errors.New("invalid build cache generation")
	}
	return generation, nil
}

func readBuildCacheFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, errors.New("invalid cache state file")
	}
	return io.ReadAll(io.LimitReader(f, 4097))
}

func (c *handoffBuildCache) saveGeneration(generation buildCacheGeneration) error {
	b, err := json.Marshal(generation)
	if err != nil {
		return err
	}
	if err := writeBuildCacheFile(c.loc, filepath.Join(c.stateRoot(), c.digest+".json"), b); err != nil {
		return err
	}
	c.generation = generation
	return nil
}

func writeBuildCacheFile(loc layout.Policy, path string, data []byte) error {
	f, err := layout.CreateLocalTemp(loc, "handoff-build-cache", ".json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err := errors.Join(err, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func (c *handoffBuildCache) advance(system string) error {
	next := c.generation.Number + 1
	if next == 0 {
		return errors.New("build cache generation exhausted")
	}
	return c.saveGeneration(buildCacheGeneration{Number: next, System: system})
}

func (c *handoffBuildCache) currentID() string {
	if c.generation.ID == "" || c.generation.System != c.system {
		return ""
	}
	for _, dir := range []string{c.cacheRoot(), c.namespace(), c.generationDir()} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			return ""
		}
	}
	data, err := readBuildCacheFile(c.idFile())
	if err != nil || string(data) != c.generation.ID {
		return ""
	}
	return c.generation.ID
}

func (c *handoffBuildCache) touch() {
	now := time.Now()
	_ = os.Chtimes(c.idFile(), now, now)
}

func (c *handoffBuildCache) prune() {
	current := filepath.Base(c.generationDir())
	dirs, _ := os.ReadDir(c.namespace())
	for _, entry := range dirs {
		if entry.Name() != current {
			_ = removeSnapshotScratch(filepath.Join(c.namespace(), entry.Name()))
		}
	}
	dir := c.generationDir()
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), layout.TmpMarker) {
			_ = removeSnapshotScratch(filepath.Join(dir, entry.Name()))
		}
	}
}

func (c *handoffBuildCache) prepare(ctx context.Context, private string) error {
	if err := layout.EnsureLocalDir(c.loc, c.cacheRoot()); err != nil {
		return err
	}
	if info, err := os.Lstat(c.namespace()); err == nil && info.IsDir() {
		c.prune()
	} else if err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("invalid handoff build cache namespace")
	}
	if c.currentID() != "" {
		artifacts := filepath.Join(c.generationDir(), "gocache")
		if _, err := os.Lstat(artifacts); errors.Is(err, os.ErrNotExist) {
			c.touch()
			return nil
		}
		if _, err := walkBuildCache(ctx, artifacts, private, true); err == nil {
			c.touch()
			return nil
		} else if !errors.Is(err, errHandoffBuildCacheUnsafe) {
			return err
		}
		if err := removeSnapshotScratch(private); err != nil {
			return err
		}
		if err := os.Mkdir(private, 0o700); err != nil {
			return err
		}
	}
	if err := removeSnapshotScratch(c.generationDir()); err != nil {
		return err
	}
	return c.withBudget(ctx, 4096, func() error {
		if err := layout.EnsureLocalDir(c.loc, c.generationDir()); err != nil {
			return err
		}
		var id [16]byte
		rand.Read(id[:])
		generation := c.generation
		generation.ID = hex.EncodeToString(id[:])
		if err := writeBuildCacheFile(c.loc, c.idFile(), []byte(generation.ID)); err != nil {
			return err
		}
		return c.saveGeneration(generation)
	})
}

func walkBuildCache(ctx context.Context, source, dest string, validate bool) (int64, error) {
	var bytes int64
	var files int
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("cannot measure build cache entry")
		}
		size := max(st.Blocks*512, info.Size())
		if size > math.MaxInt64-bytes {
			return errHandoffBuildCacheUnsafe
		}
		bytes += size
		files++
		if validate && (bytes > handoffBuildCacheBytes || files > handoffBuildCacheFiles ||
			(path == source && !info.IsDir()) || (!info.IsDir() && (!info.Mode().IsRegular() || st.Nlink != 1))) {
			return errHandoffBuildCacheUnsafe
		}
		if dest == "" || path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		return copyBuildCacheFile(path, target)
	})
	return bytes, err
}

func copyBuildCacheFile(source, target string) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, f)
	return errors.Join(err, out.Close())
}

type buildCacheUsage struct {
	digest string
	bytes  int64
	used   time.Time
}

func (c *handoffBuildCache) withBudget(ctx context.Context, incoming int64, install func() error) error {
	budget, _, err := lockBuildCache(ctx, filepath.Join(c.stateRoot(), "budget.lock"), unix.LOCK_EX, true)
	if err != nil {
		return err
	}
	defer budget.Close()
	entries, err := os.ReadDir(c.cacheRoot())
	if err != nil {
		return err
	}
	var used int64
	var existing bool
	var candidates []buildCacheUsage
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(c.cacheRoot(), name)
		bytes, err := walkBuildCache(ctx, path, "", false)
		if err != nil {
			return err
		}
		if bytes > math.MaxInt64-used {
			return errHandoffBuildCacheFull
		}
		used += bytes
		if name == c.digest {
			existing = true
		} else if entry.IsDir() && validBuildCacheHex(name, 32) {
			other := &handoffBuildCache{loc: c.loc, digest: name}
			other.generation, _ = other.readGeneration()
			var modified time.Time
			if info, err := os.Lstat(other.idFile()); err == nil {
				modified = info.ModTime()
			}
			candidates = append(candidates, buildCacheUsage{name, bytes, modified})
		}
	}
	slices.SortFunc(candidates, func(a, b buildCacheUsage) int {
		if order := a.used.Compare(b.used); order != 0 {
			return order
		}
		return strings.Compare(a.digest, b.digest)
	})
	count := len(entries)
	if !existing {
		count++
	}
	evictionStart := time.Now()
	evicted := 0
	for _, candidate := range candidates {
		if count <= handoffBuildCacheNamespaces && used <= handoffBuildCacheBytes-incoming {
			break
		}
		other := &handoffBuildCache{loc: c.loc, digest: candidate.digest}
		lock, _, err := lockBuildCache(ctx, filepath.Join(c.stateRoot(), candidate.digest+".lock"), unix.LOCK_EX, false)
		if err != nil {
			continue
		}
		err = other.clear()
		_ = lock.Close()
		if err == nil {
			used -= candidate.bytes
			count--
			evicted++
		}
	}
	diagnostic.FromContext(ctx).Info("Handoff build cache eviction", "duration_ms", time.Since(evictionStart).Milliseconds(), "evicted", evicted)
	if count > handoffBuildCacheNamespaces || used > handoffBuildCacheBytes-incoming {
		return errHandoffBuildCacheFull
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return install()
}

func (c *handoffBuildCache) promoteCache(ctx context.Context, private string) error {
	if c.currentID() == "" {
		return errors.New("build cache generation no longer matches")
	}
	bytes, err := walkBuildCache(ctx, private, "", true)
	if err != nil {
		return err
	}
	return c.withBudget(ctx, bytes, func() error {
		stage := filepath.Join(c.generationDir(), layout.TmpPath("promote"))
		if err := removeSnapshotScratch(stage); err != nil {
			return err
		}
		if err := os.Rename(private, stage); err != nil {
			return err
		}
		defer removeSnapshotScratch(stage)
		if err := ctx.Err(); err != nil {
			return err
		}
		dest := filepath.Join(c.generationDir(), "gocache")
		if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(stage, dest); err != nil {
				return err
			}
		} else if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, dest, unix.RENAME_EXCHANGE); err != nil {
			return err
		}
		c.touch()
		return nil
	})
}

func (c *handoffBuildCache) privateDir() string {
	return filepath.Join(c.stateRoot(), layout.TmpPath("run-"+c.digest))
}

func (c *handoffBuildCache) clear() error {
	current, err := c.readGeneration()
	if err != nil {
		return err
	}
	c.generation = current
	if err := c.advance(current.System); err != nil {
		return err
	}
	return removeSnapshotScratch(c.namespace())
}

// ClearHandoffBuildCache invalidates and deletes the selected repository's snapshots' build caches.
func ClearHandoffBuildCache(ctx context.Context, located resolve.Located) error {
	loc, err := located.Layout()
	if err != nil {
		return err
	}
	c := &handoffBuildCache{loc: loc}
	if err := layout.EnsureLocalDir(loc, c.stateRoot()); err != nil {
		return err
	}
	if err := layout.EnsureLocalDir(loc, c.cacheRoot()); err != nil {
		return err
	}
	entries, err := os.ReadDir(c.stateRoot())
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		digest, ok := strings.CutSuffix(entry.Name(), ".lock")
		if !ok || !validBuildCacheHex(digest, 32) {
			continue
		}
		c.digest = digest
		lock, _, err := lockBuildCache(ctx, filepath.Join(c.stateRoot(), entry.Name()), unix.LOCK_EX, true)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		errs = append(errs, c.clear(), lock.Close())
	}
	return errors.Join(errs...)
}

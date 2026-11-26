package hook

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestHandoffBuildCacheGateLifecycle(t *testing.T) {
	root, run := handoffMetadataFixture(t, `set -eu
test "${GOCACHE-unset}" = /gocache
test ! -e "$GOCACHE/seed"
echo clean > "$GOCACHE/seed"
`)
	loc := handoffDataFixtureLocation(t, root)
	p, err := loc.Layout()
	require.NoError(t, err)
	sidecar := filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml")
	writeRuntimeFixture(t, root, []tar.Header{{Name: "bin/", Typeflag: tar.TypeDir, Mode: 0o755}}, nil)
	policy, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	writeFile(t, sidecar, string(policy)+"    cache: [{name: gocache, env: GOCACHE}]\n")
	pass := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(pass.Blocking))
	require.Empty(t, pass.Blocking)
	artifacts, err := filepath.Glob(filepath.Join(p.LocalRoot(), "handoff-cache", "*", "g*", "gocache"))
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	persisted := artifacts[0]
	checkSeed := func(want string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(persisted, "seed"))
		require.NoError(t, err)
		require.Equal(t, want+"\n", string(b))
	}
	checkSeed("clean")
	firstID, err := os.ReadFile(filepath.Join(filepath.Dir(persisted), "id"))
	require.NoError(t, err)
	require.Empty(t, run().Blocking, "identical cold gate must reuse the saved verdict")

	gate := filepath.Join(root, "gate.sh")
	writeFile(t, gate, `set -eu
test "$(cat "$GOCACHE/seed")" = clean
echo failure > "$GOCACHE/seed"
touch "$GOCACHE/added"
exit 41
`)
	require.Contains(t, run().Blocking, "exit status 41")
	checkSeed("clean")
	require.NoFileExists(t, filepath.Join(persisted, "added"))

	writeFile(t, gate, `set -eu
test "$(cat "$GOCACHE/seed")" = clean
ln -s /etc/passwd "$GOCACHE/link"
echo invalid > "$GOCACHE/seed"
`)
	require.Empty(t, run().Blocking, "invalid artifacts never change a completed gate verdict")
	checkSeed("clean")
	require.NoFileExists(t, filepath.Join(persisted, "link"))

	writeFile(t, gate, `set -eu
test "$(cat "$GOCACHE/seed")" = clean
echo promoted > "$GOCACHE/seed"
`)
	require.Empty(t, run().Blocking)
	checkSeed("promoted")
	currentID, err := os.ReadFile(filepath.Join(filepath.Dir(persisted), "id"))
	require.NoError(t, err)
	require.Equal(t, firstID, currentID, "promotion preserves generation ID")
	require.Empty(t, run().Blocking, "same inputs reuse the promoted verdict")
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(persisted), "id"), []byte("changed"), 0o600))
	require.Contains(t, run().Blocking, "exit status 1", "an ID mismatch must relaunch without old artifacts")
	require.NoDirExists(t, persisted)

	require.NoError(t, ClearHandoffBuildCache(t.Context(), loc))
	require.NoDirExists(t, filepath.Dir(filepath.Dir(persisted)))
	writeFile(t, sidecar, string(policy))
	writeFile(t, gate, `set -eu
test -z "${GOCACHE-unset}" || test "${GOCACHE-unset}" = unset
`)
	require.Empty(t, run().Blocking, "omitting cache must leave the environment unchanged")
}

func TestHandoffBuildCacheEvictsInactiveLRU(t *testing.T) {
	ctx := t.Context()
	store := t.TempDir()
	p, err := layout.NewRepository(store, store, layout.RecordBlame)
	require.NoError(t, err)
	open := func(n int, system string, exclusive bool) *handoffBuildCache {
		t.Helper()
		c, _, err := openHandoffBuildCache(ctx, p, fmt.Sprintf("%064x", n), system, exclusive)
		require.NoError(t, err)
		return c.(*handoffBuildCache)
	}
	for i := range handoffBuildCacheNamespaces {
		c := open(i, "system", true)
		require.NoError(t, c.prepare(ctx, t.TempDir()))
		older := time.Unix(int64(i+1), 0)
		require.NoError(t, os.Chtimes(c.idFile(), older, older))
		c.close()
	}
	active := open(0, "system", false)
	defer active.close()
	c := open(4, "system", true)
	defer c.close()
	require.NoError(t, c.prepare(ctx, t.TempDir()))
	require.NotEmpty(t, active.currentID(), "active namespace must not be evicted")
	cleared := open(1, "system", false)
	require.Equal(t, uint64(2), cleared.keyGeneration())
	require.Empty(t, cleared.currentID())
	require.NoDirExists(t, cleared.namespace(), "oldest inactive namespace must be evicted")
	cleared.close()
	retained := open(2, "system", false)
	require.NotEmpty(t, retained.currentID())
	retained.close()

	other := t.TempDir()
	require.NoError(t, ClearHandoffBuildCache(ctx, resolve.Located{Store: other, Checkout: other}))
	require.NotEmpty(t, c.currentID(), "clearing another store must not invalidate this one")
	entries, err := os.ReadDir(c.cacheRoot())
	require.NoError(t, err)
	require.Len(t, entries, handoffBuildCacheNamespaces)
}

func TestHandoffBuildCacheRejectsUnsafeTreesAndCountsResidue(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "bad")
			switch kind {
			case "symlink":
				require.NoError(t, os.Symlink("/etc/passwd", name))
			case "hardlink":
				f := filepath.Join(dir, "file")
				writeFile(t, f, "file")
				require.NoError(t, os.Link(f, name))
			case "fifo":
				require.NoError(t, unix.Mkfifo(name, 0o600))
			case "oversize":
				writeFile(t, name, "")
				require.NoError(t, os.Truncate(name, handoffBuildCacheBytes+1))
			}
			_, err := walkBuildCache(t.Context(), dir, "", true)
			require.ErrorIs(t, err, errHandoffBuildCacheUnsafe)
		})
	}
	store := t.TempDir()
	p, err := layout.NewRepository(store, store, layout.RecordBlame)
	require.NoError(t, err)
	cache, _, err := openHandoffBuildCache(t.Context(), p, fmt.Sprintf("%064x", 1), "system", true)
	require.NoError(t, err)
	c := cache.(*handoffBuildCache)
	require.NoError(t, c.prepare(t.Context(), t.TempDir()))
	stale := filepath.Join(c.namespace(), "g0", "leftover")
	writeFile(t, stale, "")
	require.NoError(t, os.Truncate(stale, handoffBuildCacheBytes))
	require.ErrorIs(t, c.withBudget(t.Context(), 1, func() error {
		t.Fatal("publication would exceed the budget")
		return nil
	}), errHandoffBuildCacheFull)
	c.prune()
	require.NoFileExists(t, stale)
	require.NoError(t, c.withBudget(t.Context(), 1, func() error { return nil }))
	c.close()
	changedSystem, _, err := openHandoffBuildCache(context.Background(), p, c.digest, "changed system", true)
	require.NoError(t, err)
	defer changedSystem.close()
	require.Equal(t, c.keyGeneration()+1, changedSystem.keyGeneration())
	require.Empty(t, changedSystem.currentID())
	require.NoError(t, changedSystem.prepare(t.Context(), t.TempDir()))
	require.NoDirExists(t, c.generationDir())
}

func TestHandoffBuildCacheRepairsUnsafeSeedWithoutFollowingLinks(t *testing.T) {
	store, outside := t.TempDir(), t.TempDir()
	ctx := t.Context()
	p, err := layout.NewRepository(store, store, layout.RecordBlame)
	require.NoError(t, err)
	cache, _, err := openHandoffBuildCache(ctx, p, fmt.Sprintf("%064x", 1), "system", true)
	require.NoError(t, err)
	defer cache.close()
	c := cache.(*handoffBuildCache)
	first := t.TempDir()
	require.NoError(t, c.prepare(ctx, first))
	writeFile(t, filepath.Join(first, "regular"), "fixture")
	require.NoError(t, c.promoteCache(ctx, first))
	artifacts := filepath.Join(c.generationDir(), "gocache")
	require.NoError(t, os.Symlink(outside, filepath.Join(artifacts, "symlink")))
	want := filepath.Join(outside, layout.TmpPath("untouched"))
	writeFile(t, want, "protected")
	before := c.currentID()
	private := t.TempDir()
	require.NoError(t, c.prepare(ctx, private))
	require.NotEqual(t, before, c.currentID())
	require.NoDirExists(t, artifacts)
	require.NoFileExists(t, filepath.Join(private, "regular"))

	require.NoError(t, removeSnapshotScratch(c.generationDir()))
	require.NoError(t, os.Symlink(outside, c.generationDir()))
	require.NoError(t, c.prepare(ctx, private))
	require.FileExists(t, want)
	require.NotEmpty(t, c.currentID())
}

func TestHandoffBuildCacheLockAndStateFailures(t *testing.T) {
	store := t.TempDir()
	p, err := layout.NewRepository(store, store, layout.RecordBlame)
	require.NoError(t, err)
	ctx := t.Context()
	const invalid = "not-a-digest"
	_, _, err = openHandoffBuildCache(ctx, p, invalid, "system", true)
	require.ErrorContains(t, err, "invalid build cache runtime digest")
	digest := fmt.Sprintf("%064x", 1)
	c := &handoffBuildCache{loc: p, digest: digest}
	require.NoError(t, layout.EnsureLocalDir(p, c.stateRoot()))
	metadata := filepath.Join(c.stateRoot(), digest+".json")
	lock := filepath.Join(c.stateRoot(), digest+".lock")
	writeFile(t, metadata, "bad json")
	_, _, err = openHandoffBuildCache(ctx, p, digest, "system", true)
	require.Error(t, err)
	writeFile(t, metadata, `{"Number":1,"ID":"bad"}`)
	_, _, err = openHandoffBuildCache(ctx, p, digest, "system", false)
	require.ErrorContains(t, err, "invalid build cache generation")
	require.NoError(t, os.Remove(metadata))

	held, _, err := lockBuildCache(ctx, lock, unix.LOCK_EX, true)
	require.NoError(t, err)
	wait, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	_, waited, err := openHandoffBuildCache(wait, p, digest, "system", false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, waited)
	require.NoError(t, held.Close())

	require.NoError(t, os.Remove(lock))
	require.NoError(t, os.Symlink(metadata, lock))
	_, _, err = openHandoffBuildCache(ctx, p, digest, "system", true)
	require.ErrorIs(t, err, unix.ELOOP)
}

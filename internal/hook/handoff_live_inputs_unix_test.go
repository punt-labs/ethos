//go:build linux || darwin

package hook

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestHandoffLiveGitFingerprint(t *testing.T) {
	root, _ := postEditFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	stageGit(t, root, "worktree", "add", "--detach", linked)

	capture := func(root string) string {
		t.Helper()
		view, err := captureLiveHandoffRefs(t.Context(), root)
		require.NoError(t, err)
		require.NotEmpty(t, view.fingerprint)
		return view.fingerprint
	}
	first := capture(linked)
	require.Equal(t, first, capture(linked))
	require.NotEqual(t, first, capture(root), "linked worktree has a separate HEAD")

	stageGit(t, root, "tag", "proof")
	loose := capture(linked)
	require.NotEqual(t, first, loose)
	stageGit(t, root, "pack-refs", "--all")
	packed := capture(linked)
	require.NotEqual(t, loose, packed)
	stageGit(t, root, "tag", "-d", "proof")
	require.NotEqual(t, packed, capture(linked))

	_, err := captureLiveHandoffRefs(t.Context(), t.TempDir())
	require.ErrorContains(t, err, "cannot capture live-verified Git refs")

	refs := filepath.Join(root, ".git", "refs")
	require.NoError(t, os.Rename(refs, refs+"-saved"))
	require.NoError(t, os.Symlink(refs+"-saved", refs))
	_, err = captureLiveHandoffRefs(t.Context(), linked)
	require.ErrorContains(t, err, "ref symlink leaves keyed Git metadata")

	newlineRoot := filepath.Join(t.TempDir(), "embedded\nnewline")
	require.NoError(t, os.Mkdir(newlineRoot, 0o700))
	stageGit(t, newlineRoot, "init")
	_, err = captureLiveHandoffRefs(t.Context(), newlineRoot)
	require.ErrorContains(t, err, "cannot capture live-verified Git ref paths")
}

func TestHandoffLiveGitFingerprintRefTypes(t *testing.T) {
	dir := t.TempDir()
	head := filepath.Join(dir, "HEAD")
	branch := filepath.Join(dir, "refs", "heads", "main")
	writeFile(t, branch, "oid\n")
	require.NoError(t, os.Symlink(branch, head))
	alias := filepath.Join(dir, "refs", "alias")
	require.NoError(t, os.Symlink("heads/main", alias))
	digest := func(dir string, bytes int64) error {
		budget := &gateHashBudget{ctx: t.Context(), dst: io.Discard, remaining: bytes}
		return digestLiveGitRefs(budget, dir, []string{"HEAD", "refs", "packed-refs"})
	}
	require.NoError(t, digest(dir, 4096))
	require.ErrorContains(t, digest(dir, 1), "exceeds byte budget")
	require.ErrorIs(t, digest(filepath.Join(dir, "missing"), 4096), os.ErrNotExist)

	require.NoError(t, os.Remove(alias))
	require.NoError(t, os.Symlink("../config", alias))
	require.ErrorContains(t, digest(dir, 4096), "ref symlink leaves keyed Git metadata")
	require.NoError(t, os.Remove(alias))
	require.NoError(t, unix.Mkfifo(alias, 0o600))
	require.ErrorContains(t, digest(dir, 4096), "unsupported Git ref type")
	require.NoError(t, os.Remove(alias))
	require.NoError(t, os.Remove(head))
	require.ErrorIs(t, digest(dir, 4096), os.ErrNotExist)

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer root.Close()
	budget := &gateHashBudget{ctx: t.Context(), dst: io.Discard, remaining: 4096}
	require.ErrorIs(t, digestLiveGitRefSymlink(budget, root, "HEAD"), os.ErrNotExist)
}

func TestHandoffLiveInputFingerprints(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join("sub", "input")
	link := filepath.Join("sub", "alias")
	writeFile(t, filepath.Join(dir, target), "before\n")
	require.NoError(t, os.Symlink("./input", filepath.Join(dir, link)))
	fingerprint := func(inputs ...string) (string, map[string][sha256.Size]byte, error) {
		entries := make(map[string][sha256.Size]byte)
		proof, err := digestGateInputPaths(t.Context(), dir, "", inputs, true, entries)
		return proof, entries, err
	}
	before, entries, err := fingerprint(target, link, link)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	writeFile(t, filepath.Join(dir, target), "after\n")
	after, edited, err := fingerprint(link, target)
	require.NoError(t, err)
	require.NotEqual(t, before, after)
	require.NotEqual(t, entries[target], edited[target])
	require.Equal(t, entries[link], edited[link])

	_, _, err = fingerprint(link)
	require.ErrorContains(t, err, "symlink target is outside the keyed inputs")
	_, _, err = fingerprint("absent")
	var pathErr *os.PathError
	require.ErrorAs(t, err, &pathErr)
	require.Equal(t, "fingerprint", pathErr.Op)
	require.Equal(t, "absent", pathErr.Path)

	require.NoError(t, os.Symlink("sub", filepath.Join(dir, "mapped")))
	_, _, err = fingerprint(filepath.Join("mapped", "alias"), filepath.Join("mapped", "input"))
	require.Error(t, err, "a keyed alias must not resolve through a symlink parent")
	require.True(t, hasHandoffLinkBacktrack("sub/../input"))
	require.False(t, hasHandoffLinkBacktrack("../input"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = digestGateInputPaths(ctx, dir, "", []string{target}, true, edited)
	require.ErrorIs(t, err, context.Canceled)
	_, err = digestGateInputPaths(t.Context(), filepath.Join(dir, "missing-root"), "", nil, true, edited)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestHandoffChangedInputNames(t *testing.T) {
	require.Equal(t, "added", changedHandoffInput([]string{"z", "same"}, []string{"z", "same", "added"}))
	require.Equal(t, "z", changedHandoffInput([]string{"z", "same"}, []string{"same"}))
	require.Equal(t, "z", changedHandoffInput([]string{"same"}, []string{"z", "same"}))
	require.Empty(t, changedHandoffInput([]string{"z", "same"}, []string{"same", "z"}))
}

func TestHandoffSnapshotEnvironmentRejectsUnsafeOverrides(t *testing.T) {
	roots := handoffRoots{checkout: "/checkout", runtime: "/runtime", scratch: "/scratch", temp: "/tmp"}
	profile := &checkset.WorktreeSnapshot{
		Path: []string{"{runtime}/bin"},
		Env:  map[string]string{"LANG": "en_US.UTF-8", "FIXTURE": "{root}/default"},
	}
	platform := checkset.SnapshotPlatform{Env: map[string]string{"FIXTURE": "{scratch}/platform"}}
	environment, err := roots.environment(profile, platform, "/usr/bin:/bin")
	require.NoError(t, err)
	require.Contains(t, environment, "PATH=/runtime/bin:/usr/bin:/bin")
	require.Contains(t, environment, "LANG=en_US.UTF-8")
	require.NotContains(t, environment, "LANG=C")
	require.Contains(t, environment, "FIXTURE=/scratch/platform")
	require.NotContains(t, environment, "FIXTURE=/checkout/default")

	for _, path := range []string{"bin", "/runtime/../bin", "/one:/two"} {
		_, err := roots.environment(profile, checkset.SnapshotPlatform{Path: []string{path}}, "/bin")
		require.ErrorContains(t, err, "invalid snapshot runtime PATH entry")
	}
	for _, key := range []string{"GOCOVERDIR", "HOME", "PATH", "TMPDIR", "TZ"} {
		_, err := roots.environment(profile, checkset.SnapshotPlatform{Env: map[string]string{key: "/wrong"}}, "/bin")
		require.ErrorContains(t, err, "snapshot runtime environment overrides a runner variable")
	}
	for _, key := range []string{"", "ETHOS_HANDOFF_ROOT", "a=b"} {
		_, err := roots.environment(profile, checkset.SnapshotPlatform{Env: map[string]string{key: "/wrong"}}, "/bin")
		require.ErrorContains(t, err, "invalid snapshot runtime environment variable")
	}
}

func TestHandoffSnapshotOperandAndScratchBoundaries(t *testing.T) {
	snapshot := t.TempDir()
	file := filepath.Join(snapshot, "tracked")
	writeFile(t, file, "payload")
	require.True(t, gatePlanInputsPresent(snapshot, []string{"/bin/true", gateRoot}))
	require.False(t, gatePlanInputsPresent(file, []string{"/bin/true"}))
	require.False(t, gatePlanInputsPresent(snapshot, []string{"/bin/true", gateRoot + "/../outside"}))

	targets, err := handoffScratchTargets(snapshot, []string{"nested/cache", "out"})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(snapshot, "nested", "cache"), filepath.Join(snapshot, "out")}, targets)
	for _, name := range []string{".", "../outside", ".git/state", "nested/../out"} {
		_, err := handoffScratchTargets(snapshot, []string{name})
		require.ErrorContains(t, err, "invalid checkout scratch path")
	}
	for _, paths := range [][]string{{"nested", "nested/cache"}, {"nested/cache", "nested"}} {
		_, err := handoffScratchTargets(snapshot, paths)
		require.ErrorContains(t, err, "overlapping checkout scratch paths")
	}
	_, err = handoffScratchTargets(snapshot, []string{"tracked"})
	require.ErrorIs(t, err, errHandoffScratchShadowsInput)
	require.ErrorContains(t, rejectHandoffSymlinkParents(snapshot, filepath.Dir(snapshot)), "snapshot path is outside its root")
}

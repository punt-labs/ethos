package hook

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandoffWorktreeCopiesScratchSeeds(t *testing.T) {
	root, run := handoffMetadataFixture(t, `set -eu
test "$(cat /scratch/root-seed)" = checkout
test "$(cat /scratch/cache/value)" = runtime
printf writable > /scratch/root-seed
printf writable > /scratch/cache/value
test "$(cat input)" = checkout
test "$(cat /runtime/cache/value)" = runtime
`)
	writeFile(t, filepath.Join(root, "input"), "checkout\n")
	writeHandoffScratchProfile(t, root, "    scratch:\n      seed:\n        '{scratch}/root-seed': '{root}/input'\n        '{scratch}/cache': '{runtime}/cache'\n", map[string]string{"cache/value": "runtime\n"})
	got := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(got.Blocking))
	require.Equal(t, StageFeedback{}, got)
	require.NoDirExists(t, filepath.Join(root, "cache"))

	writeHandoffScratchProfile(t, root, "    scratch: {seed: {'{scratch}/unsafe': '{root}/../input'}}\n", nil)
	got = run()
	require.Contains(t, got.Notice, "copy private scratch.seed")
	require.Contains(t, got.Blocking, "Run the explicit project gate")
}

func TestHandoffScratchSeedPaths(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, gateRoot)
	scratch := filepath.Join(root, linuxHandoffRoots().scratch)
	require.NoError(t, os.Mkdir(scratch, 0o700))
	writeFile(t, filepath.Join(checkout, "tree", "data"), "value")
	require.NoError(t, os.Symlink("data", filepath.Join(checkout, "tree", "link")))
	require.NoError(t, os.Mkdir(filepath.Join(checkout, "outside"), 0o700))
	require.NoError(t, os.Symlink("../tree/data", filepath.Join(checkout, "outside", "link")))

	require.NoError(t, seedHandoffScratch(root, map[string]string{"copy": "{root}/tree"}))
	data, err := os.ReadFile(filepath.Join(scratch, "copy", "link"))
	require.NoError(t, err)
	require.Equal(t, "value", string(data))

	for _, tc := range []struct {
		name    string
		entries map[string]string
		want    string
	}{
		{"bad destination", map[string]string{"../escape": "{root}/tree"}, "invalid scratch seed destination"},
		{"overlap", map[string]string{"nested": "{root}/tree", "nested/child": "{root}/tree"}, "overlapping scratch seed destinations"},
		{"bare source", map[string]string{"bare": "host/file"}, "invalid scratch seed source root"},
		{"missing runtime", map[string]string{"runtime": "{runtime}/cache"}, "no such file"},
		{"escaping symlink", map[string]string{"unsafe": "{root}/outside"}, "scratch seed symlink escapes the copied tree"},
		{"existing destination", map[string]string{"copy": "{root}/tree"}, "file exists"},
	} {
		t.Run(tc.name, func(t *testing.T) { require.ErrorContains(t, seedHandoffScratch(root, tc.entries), tc.want) })
	}
	dest, err := os.OpenRoot(scratch)
	require.NoError(t, err)
	defer dest.Close()
	remaining := int64(2)
	require.ErrorContains(t, copyHandoffScratchSeed(checkout, "tree/data", dest, "limited", &remaining), "scratch seed exceeds the copy budget")
}

func writeHandoffScratchProfile(t *testing.T, root, fields string, members map[string]string) {
	t.Helper()
	profile := "checks:\n  metadata:\n    profile: linux-worktree-v1\n    epoch: scratch-probe\n    sources: {mode: worktree}\n" + fields
	if members != nil {
		profilePath := filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml")
		writeFile(t, profilePath, profile)
		var headers []tar.Header
		for name, data := range members {
			headers = append(headers, tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))})
		}
		writeRuntimeFixture(t, root, headers, members)
		return
	}
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), profile)
}

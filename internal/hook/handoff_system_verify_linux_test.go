package hook

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestVerifyMountedHandoffSystemRejectsPathAndProofChanges(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "proof")
	writeFile(t, tool, "#!/bin/sh\nexit 0\n")
	require.NoError(t, os.Chmod(tool, 0o755))
	file := filepath.Join(dir, "input")
	writeFile(t, file, "initial")
	require.NoError(t, os.Symlink("input", filepath.Join(dir, "alias")))
	ctx := context.Background()
	proof := func(path string, directory bool) string {
		t.Helper()
		key, err := fingerprintHandoffSystemInput(ctx, path, directory)
		require.NoError(t, err)
		return key
	}
	plan := handoffSystemPlan{Inputs: []handoffSystemInput{
		{Path: tool, Tool: "proof", Digest: proof(tool, false)},
		{Path: file, Digest: proof(file, false)},
		{Path: dir, Directory: true, Digest: proof(dir, true)},
	}}
	env := []string{"PATH=" + dir}
	require.NoError(t, verifyMountedHandoffSystem(plan, env))
	require.ErrorContains(t, verifyMountedHandoffSystem(plan, []string{"HOME=/scratch/home"}), "tool did not match")
	shadow := t.TempDir()
	shadowTool := filepath.Join(shadow, "proof")
	writeFile(t, shadowTool, "#!/bin/sh\nexit 0\n")
	require.NoError(t, os.Chmod(shadowTool, 0o755))
	require.ErrorContains(t, verifyMountedHandoffSystem(plan, []string{"PATH=" + shadow + ":" + dir}), "tool did not match")
	require.NoError(t, os.Remove(filepath.Join(dir, "alias")))
	require.NoError(t, os.Symlink("proof", filepath.Join(dir, "alias")))
	require.ErrorContains(t, verifyMountedHandoffSystem(plan, env), "system input did not match")
	require.NoError(t, os.Remove(file))
	require.ErrorContains(t, verifyMountedHandoffSystem(plan, env), "system input did not match")
}

func TestHandoffSystemDirectoryProofIncludesFileContents(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "input")
	mtime := time.Unix(1700000000, 0)
	ctx := context.Background()
	writeFile(t, file, "hello")
	require.NoError(t, os.Chtimes(file, mtime, mtime))
	initial, err := fingerprintHandoffSystemInput(ctx, dir, true)
	require.NoError(t, err)
	writeFile(t, file, "world")
	require.NoError(t, os.Chtimes(file, mtime, mtime))
	changed, err := fingerprintHandoffSystemInput(ctx, dir, true)
	require.NoError(t, err)
	require.NotEqual(t, initial, changed)
}

func TestHandoffSystemRejectsUncoveredToolsAndSpecialInputs(t *testing.T) {
	ctx := context.Background()
	checkout, host := t.TempDir(), handoffSystemFixtureDir(t)
	fifo := filepath.Join(host, "pipe")
	require.NoError(t, unix.Mkfifo(fifo, 0o600))
	_, err := fingerprintHandoffSystemInput(ctx, fifo, false)
	require.ErrorContains(t, err, "not a regular file")
	_, err = fingerprintHandoffSystemInput(ctx, fifo, true)
	require.ErrorContains(t, err, "not a directory")
	_, err = fingerprintHandoffSystemInput(ctx, host, true)
	require.ErrorContains(t, err, "special file")
	_, err = fingerprintHandoffSystemInput(ctx, filepath.Join(host, "absent"), true)
	require.ErrorIs(t, err, os.ErrNotExist)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = fingerprintHandoffSystemInput(canceled, host, true)
	require.ErrorIs(t, err, context.Canceled)
	profile := &checkset.WorktreeSnapshot{}
	profile.System.Tools = []string{"handoff-tool-not-on-mounted-path"}
	_, err = prepareHandoffSystem(ctx, checkout, profile)
	require.ErrorContains(t, err, "not available on the mounted sandbox PATH")
	profile.System.Dirs = []string{fifo}
	_, err = prepareHandoffSystem(ctx, checkout, profile)
	require.ErrorContains(t, err, "cannot fingerprint system input")
	profile.System.Dirs = nil
	profile.System.Files = []string{"/etc/passwd"}
	_, err = prepareHandoffSystem(ctx, checkout, profile)
	require.ErrorContains(t, err, "cannot safely resolve system input")
	profile.System.Files = nil
	profile.Path = []string{host}
	proof := filepath.Join(host, profile.System.Tools[0])
	writeFile(t, proof, "#!/bin/sh\nexit 0\n")
	require.NoError(t, os.Chmod(proof, 0o755))
	_, err = prepareHandoffSystem(ctx, checkout, profile)
	require.ErrorContains(t, err, "not available on the mounted sandbox PATH")
}

func TestMountHandoffSystemRejectsOccupiedTargets(t *testing.T) {
	t.Run("existing accounts directory", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, "etc"), 0o700))
		require.ErrorIs(t, mountHandoffSystem(root, handoffSystemPlan{}), os.ErrExist)
	})
	for _, tc := range []struct {
		name      string
		target    string
		directory bool
	}{
		{"existing bind file", "/occupied", false},
		{"file where bind directory is needed", "/occupied", true},
		{"file where bind parent is needed", "/occupied/child", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "occupied")
			writeFile(t, path, "existing content")
			plan := handoffSystemPlan{Mounts: []handoffSystemMount{{Target: tc.target, Directory: tc.directory}}}
			require.Error(t, mountHandoffSystem(root, plan))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "existing content", string(data))
		})
	}
}

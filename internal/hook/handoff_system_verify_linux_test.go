package hook

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

func TestHandoffSystemDirectoryProofKeepsWalkOrderAcrossBatches(t *testing.T) {
	dir := t.TempDir()
	total := handoffSystemProofBatchSize + 1
	for i := total - 1; i >= 0; i-- {
		name := fmt.Sprintf("input-%04d", i)
		writeFile(t, filepath.Join(dir, name), name)
	}
	h := sha256.New()
	enc := json.NewEncoder(h)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.NoError(t, enc.Encode([]any{"", info.Size(), info.ModTime().UnixNano(), info.Mode(), "", ""}))
	for i := range total {
		name := fmt.Sprintf("input-%04d", i)
		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
		require.NoError(t, enc.Encode([]any{"/" + name, info.Size(), info.ModTime().UnixNano(), info.Mode(), "", digest}))
	}
	got, err := fingerprintHandoffSystemInput(context.Background(), dir, true)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%x", h.Sum(nil)), got)
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

func TestHandoffSystemRefusesPrivateSidecarPaths(t *testing.T) {
	private, err := os.MkdirTemp("/var/tmp", "ethos-private-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Remove(private)) })
	profile := &checkset.WorktreeSnapshot{}
	profile.System.Dirs = []string{private}
	_, err = prepareHandoffSystem(context.Background(), t.TempDir(), profile)
	require.ErrorContains(t, err, "cannot safely resolve system input "+private)
	for _, path := range []string{"/home/a/input", "/root/input", "/etc/shadow", "/etc/ssl/private/key", "/sys/input", "/proc/input", "/run/input", "/tmp/input", "/usr-other/input"} {
		require.False(t, safeHandoffSystemPath(path, "/checkout-fixture"), path)
	}
	require.True(t, safeHandoffSystemPath("/usr/bin/sh", "/checkout-fixture"))
}

func TestHandoffSystemAllowsOnlyFixedLinuxbrewPrefix(t *testing.T) {
	const prefix = "/home/linuxbrew/.linuxbrew"
	const checkout = "/checkout-fixture"
	require.True(t, safeHandoffSystemPath(prefix, checkout))
	require.True(t, safeHandoffSystemPath(prefix+"/bin/brew", checkout))
	require.True(t, safeHandoffSystemSource(prefix+"/bin/brew", prefix+"/Homebrew/bin/brew", checkout, false))
	require.False(t, safeHandoffSystemPath(prefix, prefix+"/Homebrew"))
	require.False(t, safeHandoffSystemSource(prefix+"/bin/brew", "/home/linuxbrew/private", checkout, false))
	for _, path := range []string{"/home/linuxbrew", "/home/linuxbrew/.ssh", prefix + "-other/bin/brew", "/home/ubuntu/.linuxbrew"} {
		require.False(t, safeHandoffSystemPath(path, checkout), path)
	}
}

func TestHandoffSystemAllowsOnlyFixedTeXRoots(t *testing.T) {
	const checkout = "/checkout-fixture"
	for _, root := range []string{"/etc/texmf", "/var/lib/texmf"} {
		t.Run(root, func(t *testing.T) {
			require.True(t, safeHandoffSystemPath(root, checkout))
			require.True(t, safeHandoffSystemSource(root, root, checkout, true))
			require.True(t, safeHandoffSystemPath(root+"/web2c", checkout))
			require.False(t, safeHandoffSystemPath(root+"-other", checkout))
		})
	}
	require.False(t, safeHandoffSystemPath("/var/lib", checkout))
	require.False(t, safeHandoffSystemPath("/var/lib/private", checkout))
}

func TestHandoffSystemAllowsNixOSReleaseTargetOnly(t *testing.T) {
	const source = "/nix/store/hash-etc-os-release/os-release"
	require.True(t, safeHandoffSystemSource("/etc/os-release", source, "/checkout-fixture", false))
	require.False(t, safeHandoffSystemSource("/etc/os-release", source, "/checkout-fixture", true))
	require.False(t, safeHandoffSystemSource("/usr/share/other", source, "/checkout-fixture", false))
	require.False(t, safeHandoffSystemSource("/etc/os-release", source, "/nix/store/hash-etc-os-release", false))
	require.False(t, safeHandoffSystemSource("/etc/os-release", "/nix/store-other/os-release", "/checkout-fixture", false))
	require.False(t, safeHandoffSystemPath(source, "/checkout-fixture"))
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

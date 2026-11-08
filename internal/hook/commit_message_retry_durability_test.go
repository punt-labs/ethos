//go:build linux || darwin

package hook

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestCommitMessageRetryDirectoryDurability(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a non-root process enforcing directory read permissions")
	}
	for _, parsedNoOp := range []bool{false, true} {
		name := "identical bytes"
		if parsedNoOp {
			name = "equal parsed trailers with configured normalization"
		}
		t.Run(name, func(t *testing.T) {
			p := messagePolicy(t, layout.RecordBlame)
			checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
			dir := filepath.Join(t.TempDir(), "physical-message-dir")
			require.NoError(t, os.Mkdir(dir, 0o700))
			dir, err := filepath.EvalSymlinks(dir)
			require.NoError(t, err)
			path := filepath.Join(dir, "MSG")
			require.NoError(t, os.WriteFile(path, []byte("subject\n"), 0o640))
			require.NoError(t, os.Chmod(path, 0o640))
			alias := filepath.Join(checkout, "message-alias")
			require.NoError(t, os.Symlink(path, alias))
			// Search and rename still work; opening the physical directory for
			// fsync does not. The alias's directory remains readable throughout.
			require.NoError(t, os.Chmod(dir, 0o300))
			t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
			_, err = os.ReadDir(dir)
			require.ErrorIs(t, err, os.ErrPermission, "the real fault must execute, not silently pass under privilege")
			renames := 0
			rename := func(from, to string) error {
				renames++
				assert.Equal(t, path, to, "installation targets the physical message")
				return os.Rename(from, to)
			}
			apply := func() error { return applyCommitMessage(p, alias, messageMission, "", rename) }
			err = apply()
			require.ErrorIs(t, err, os.ErrPermission)
			require.ErrorContains(t, err, "commit message installed, but syncing its directory failed")
			t.Logf("uid=%d; initial real rename applied; result: %v", os.Geteuid(), err)
			require.Equal(t, 1, renames, "the first rename really applied")
			installed, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "subject\n\nMission: "+messageMission+"\n", string(installed))
			before, err := os.Stat(path)
			require.NoError(t, err)
			if parsedNoOp {
				// Changing the output key's case makes Git render different bytes,
				// but --parse normalizes both inputs identically. Do not edit the
				// installed payload to manufacture this second no-op branch.
				config := filepath.Join(t.TempDir(), "gitconfig")
				require.NoError(t, os.WriteFile(config, []byte("[trailer \"Mission\"]\n\tkey = mission\n"), 0o600))
				t.Setenv("GIT_CONFIG_GLOBAL", config)
			}
			rendered, err := interpretCommitTrailers(checkout, gitenv.Parsed(), installed,
				"--if-exists=doNothing", "--if-missing=add", "--trailer", "Mission="+messageMission)
			require.NoError(t, err)
			require.Equal(t, !parsedNoOp, bytes.Equal(installed, rendered), "exercise distinct no-op branches")
			if parsedNoOp {
				existing, err := interpretCommitTrailers(checkout, gitenv.Parsed(), installed, "--parse")
				require.NoError(t, err)
				updated, err := interpretCommitTrailers(checkout, gitenv.Parsed(), rendered, "--parse")
				require.NoError(t, err)
				require.Equal(t, existing, updated)
			}
			for range 2 {
				err = apply()
				assert.ErrorIs(t, err, os.ErrPermission, "a retry must not erase the persistent sync failure")
				assert.ErrorContains(t, err, "commit message already contains trailers; crash durability unconfirmed")
				t.Logf("persistent no-write retry: %v", err)
				assertCommitMessageRetryUnchanged(t, p, path, before, installed, renames)
			}
			require.NoError(t, os.Chmod(dir, 0o700))
			require.NoError(t, apply(), "permission recovery permits confirmation without a rewrite")
			assertCommitMessageRetryUnchanged(t, p, path, before, installed, renames)
			info, err := os.Lstat(alias)
			require.NoError(t, err)
			assert.NotZero(t, info.Mode()&os.ModeSymlink)
		})
	}
}

func assertCommitMessageRetryUnchanged(t *testing.T, p layout.Policy, path string, before os.FileInfo, want []byte, renames int) {
	t.Helper()
	assert.Equal(t, 1, renames, "no no-op retry may rename or normalize the payload")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, data, "no normalization or duplicate trailers")
	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after))
	assert.Equal(t, before.ModTime(), after.ModTime())
	assert.Equal(t, os.FileMode(0o640), after.Mode().Perm())
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries, "committed or failed operations leave no stage")
	lock, err := p.LockPath("commit-message", path)
	require.NoError(t, err)
	f, err := os.OpenFile(lock, os.O_RDWR, 0)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB), "the error path releases the stable lock")
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_UN))
}

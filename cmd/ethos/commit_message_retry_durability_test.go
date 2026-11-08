//go:build linux || darwin

package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

func TestCLICommitMessageRetryDirectoryDurability(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a non-root process enforcing directory read permissions")
	}
	require.NotEmpty(t, ethosBinary, "execute the actual full CLI entry point")
	binary, err := os.ReadFile(ethosBinary)
	require.NoError(t, err)
	t.Logf("uid=%d; actual CLI binary sha256=%x", os.Geteuid(), sha256.Sum256(binary))
	for _, parsedNoOp := range []bool{false, true} {
		name := "identical bytes"
		if parsedNoOp {
			name = "equal parsed trailers"
		}
		t.Run(name, func(t *testing.T) {
			se := freshCLIEnv(t, "message-human")
			const mission = "m-2026-11-04-043"
			se.env = append(se.env, "MISSION_ID="+mission)
			// A populated unrelated index must survive failures and retries.
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, "source"), []byte("staged source\n"), 0o600))
			git := testhelpers.GitCommand("-C", se.repo, "add", "source")
			git.Env = se.env
			out, err := git.CombinedOutput()
			require.NoError(t, err, "%s", out)
			index := filepath.Join(se.repo, ".git", "index")
			indexBefore, err := os.ReadFile(index)
			require.NoError(t, err)
			dir := filepath.Join(t.TempDir(), "message-dir")
			require.NoError(t, os.Mkdir(dir, 0o700))
			dir, err = filepath.EvalSymlinks(dir)
			require.NoError(t, err)
			path := filepath.Join(dir, "MSG")
			require.NoError(t, os.WriteFile(path, []byte("subject\n"), 0o640))
			require.NoError(t, os.Chmod(path, 0o640))
			alias := filepath.Join(se.repo, "message-alias")
			require.NoError(t, os.Symlink(path, alias))
			require.NoError(t, os.Chmod(dir, 0o300))
			t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
			_, err = os.ReadDir(dir)
			require.ErrorIs(t, err, os.ErrPermission)
			_, stderr, code := runCLI(t, se, "hook", "commit-message", alias)
			require.NotZero(t, code, "%s", stderr)
			require.Contains(t, stderr, "commit message installed, but syncing its directory failed")
			t.Logf("initial install: CLI exit=%d; %s", code, stderr)
			installed, err := os.Stat(path)
			require.NoError(t, err)
			if parsedNoOp {
				config := filepath.Join(t.TempDir(), "gitconfig")
				require.NoError(t, os.WriteFile(config, []byte("[trailer \"Mission\"]\n\tkey = mission\n"), 0o600))
				se.env = append(se.env, "GIT_CONFIG_GLOBAL="+config)
			}
			for range 2 {
				_, stderr, code = runCLI(t, se, "hook", "commit-message", alias)
				assert.NotZero(t, code, "persistent directory failure cannot become CLI success: %s", stderr)
				assert.Contains(t, stderr, "commit message already contains trailers; crash durability unconfirmed")
				t.Logf("persistent no-write retry: CLI exit=%d; %s", code, stderr)
			}
			require.NoError(t, os.Chmod(dir, 0o700))
			_, stderr, code = runCLI(t, se, "hook", "commit-message", alias)
			require.Equal(t, 0, code, "recovery confirms the directory: %s", stderr)
			t.Logf("permission recovery: CLI exit=%d", code)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, "subject\n\nMission: "+mission+"\n", string(data))
			after, err := os.Stat(path)
			require.NoError(t, err)
			assert.True(t, os.SameFile(installed, after), "neither retry branch replaces the message")
			assert.Equal(t, installed.ModTime(), after.ModTime())
			assert.Equal(t, os.FileMode(0o640), after.Mode().Perm())
			indexAfter, err := os.ReadFile(index)
			require.NoError(t, err)
			assert.Equal(t, indexBefore, indexAfter)
			assert.NoFileExists(t, index+".lock")
			p, err := layout.NewRepository(se.repo, se.repo, layout.RecordBlame)
			require.NoError(t, err)
			entries, err := os.ReadDir(p.TempDir())
			require.NoError(t, err)
			assert.Empty(t, entries)
			lock, err := p.LockPath("commit-message", path)
			require.NoError(t, err)
			f, err := os.OpenFile(lock, os.O_RDWR, 0)
			require.NoError(t, err)
			defer func() { require.NoError(t, f.Close()) }()
			require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB), "no child retained the message lock")
			require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_UN))
		})
	}
}

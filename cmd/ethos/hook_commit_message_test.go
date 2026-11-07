//go:build linux || darwin

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/resolve"
)

func TestHookCommitMessageUsesLocatedSnapshot(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid snapshot", true: "failed snapshot"}[broken], func(t *testing.T) {
			store, checkout, home := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("MISSION_ID", "m-2026-11-04-043")
			t.Setenv("DELEGATION_ID", "d-2026-11-04-043")
			loc := resolve.Located{Store: store, Checkout: checkout}
			if broken {
				loc.ConfigErr = errors.New("fixture rejected config")
			}
			cmdRoots.Store(&repoRoots{Located: loc, Global: true})
			t.Cleanup(func() { cmdRoots.Store(nil) })
			// Contradict the captured snapshot on disk. Neither an invalid new
			// config nor a repaired old config may replace the entry decision.
			require.NoError(t, os.MkdirAll(filepath.Join(store, ".punt-labs"), 0o700))
			config := "record: [invalid\n"
			if broken {
				config = "record: blame\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(store, ".punt-labs", "ethos.yaml"), []byte(config), 0o600))
			path := filepath.Join(checkout, "MSG")
			require.NoError(t, os.WriteFile(path, []byte("subject\n"), 0o600))
			err := runHookCommitMessage(nil, []string{path})
			if broken {
				require.ErrorContains(t, err, "fixture rejected config")
				assert.NoDirExists(t, filepath.Join(store, ".punt-labs", "local"))
			} else {
				require.NoError(t, err)
				assert.DirExists(t, filepath.Join(store, ".punt-labs", "local", "ethos", "tmp"))
			}
			assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"))
			assert.NoDirExists(t, filepath.Join(home, ".punt-labs"))
		})
	}
}

func TestCLIHookCommitMessageArgumentsAndOutsideRepository(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the current CLI must build")
	for _, args := range [][]string{{"hook", "commit-message"}, {"hook", "commit-message", "one", "two"}, {"hook", "commit-message", "MSG"}} {
		t.Run(filepath.Base(args[len(args)-1]), func(t *testing.T) {
			se := freshCLIEnv(t, "message-human")
			se.repo = t.TempDir() // deliberately outside any repository
			_, stderr, code := runCLI(t, se, args...)
			assert.NotZero(t, code, stderr)
			assert.NoDirExists(t, filepath.Join(se.repo, ".punt-labs"))
		})
	}
}

//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLI_HookSnapshotEntryPoints(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the current CLI binary must build; this acceptance must not skip")
	se := freshCLIEnv(t, "snapshot-human")
	_, stderr, code := runCLI(t, se, "seed")
	require.Equal(t, 0, code, stderr)
	answers := writeAnswers(t, se.repo, "name: Snapshot Human\nhandle: snapshot-human\n")
	_, stderr, code = runCLI(t, se, "setup", "--file", answers, "--bundle", "foundation")
	require.Equal(t, 0, code, stderr)
	for _, name := range []string{"session-start", "pre-compact"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, ethosBinary, "hook", name)
			cmd.Dir = se.repo
			cmd.Env = withForcedNotUnderClaudeCode(se.env)
			cmd.Stdin = strings.NewReader(`{"session_id":"snapshot-cli-session"}`)
			var out, errOut bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &errOut
			require.NoError(t, cmd.Run(), "stdout: %s\nstderr: %s", out.String(), errOut.String())
			assert.Contains(t, out.String(), "Claude")
			assert.Contains(t, out.String(), "## Team: foundation")
			assert.NotContains(t, errOut.String(), "could not")
		})
	}
}

func TestHookAuditSnapshot_ConfigReplacement(t *testing.T) {
	for _, repaired := range []bool{false, true} {
		t.Run(fmt.Sprintf("entry-error=%t", repaired), func(t *testing.T) {
			env := setupBundleTestEnv(t)
			configPath := filepath.Join(env.repo, ".punt-labs", "ethos.yaml")
			require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
			initial, replacement := "record: blame\n", "record: [invalid\n"
			if repaired {
				initial, replacement = replacement, initial
			}
			require.NoError(t, os.WriteFile(configPath, []byte(initial), 0o600))
			loc := resolve.Roots()
			loc.Checkout = t.TempDir()
			cmdRoots.Store(&repoRoots{Located: loc, Global: true})
			t.Cleanup(func() { cmdRoots.Store(nil) })
			require.NoError(t, os.WriteFile(configPath, []byte(replacement), 0o600))
			payload := filepath.Join(t.TempDir(), "payload.json")
			require.NoError(t, os.WriteFile(payload, []byte(`{"session_id":"snapshot-audit","tool_name":"Bash","tool_input":{"command":"true"}}`), 0o600))
			stdin, err := os.Open(payload)
			require.NoError(t, err)
			defer stdin.Close()
			originalStdin := os.Stdin
			os.Stdin = stdin
			t.Cleanup(func() { os.Stdin = originalStdin })
			err = writeAuditLog(filepath.Join(env.globalRoot, "sessions"))
			live := filepath.Join(env.repo, ".punt-labs", "local", "ethos", "sessions", "snapshot-audit.audit.jsonl")
			if repaired {
				require.ErrorIs(t, err, hook.ErrAuditDropped)
				assert.NoFileExists(t, live)
			} else {
				require.NoError(t, err, "a replacement config must not replace the captured policy")
				assert.FileExists(t, live)
				policy, err := loc.Layout()
				require.NoError(t, err)
				stream := audit.Stream{Namespace: audit.SessionNS, Session: "snapshot-audit"}
				lock, err := policy.LockPath("audit-stream", stream.Key())
				require.NoError(t, err)
				assert.FileExists(t, lock, "the shared policy also owns the stream lock")
			}
			assert.Empty(t, pathsNaming(t, loc.Checkout, "snapshot-audit"), "checkout is not the local owner")
			assert.Empty(t, pathsNaming(t, env.home, "snapshot-audit"), "no global fallback")
		})
	}
}

func TestCLI_HookAuditSnapshotLinkedWorktree(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the current CLI binary must build")
	for _, mode := range []string{"blame", "full"} {
		t.Run(mode, func(t *testing.T) {
			// All Git writes here are isolated fixture construction, never the
			// development checkout. This fixture installs no Git hooks.
			home, store := t.TempDir(), t.TempDir()
			gitInitDir(t, store, home)
			env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"}
			checkout := filepath.Join(t.TempDir(), "feature")
			for _, args := range [][]string{
				{"-c", "user.name=Snapshot", "-c", "user.email=snapshot@example.test", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "fixture"},
				{"worktree", "add", "-q", "-b", "feature", checkout},
			} {
				cmd := testhelpers.GitCommand(append([]string{"-C", store}, args...)...)
				cmd.Env = env
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, string(out))
			}
			require.NoError(t, os.MkdirAll(filepath.Join(store, ".punt-labs"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(store, ".punt-labs", "ethos.yaml"), []byte("record: "+mode+"\n"), 0o600))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			done := make(chan error, 4)
			for _, dir := range []string{store, checkout, store, checkout} {
				go func() {
					cmd := exec.CommandContext(ctx, ethosBinary, "hook", "audit-log")
					cmd.Dir = dir
					cmd.Env = withForcedNotUnderClaudeCode(env)
					cmd.Stdin = strings.NewReader(`{"session_id":"linked-snapshot","tool_name":"Bash","tool_input":{"command":"true"}}`)
					out, err := cmd.CombinedOutput()
					if err != nil || strings.Contains(string(out), "audit entry not written") {
						done <- fmt.Errorf("hook audit-log: %v: %s", err, out)
						return
					}
					done <- nil
				}()
			}
			for range 4 {
				assert.NoError(t, <-done)
			}
			data, err := os.ReadFile(filepath.Join(store, ".punt-labs", "local", "ethos", "sessions", "linked-snapshot.audit.jsonl"))
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			require.Len(t, lines, 4, "concurrent checkout writers must share one stream")
			var previous int64
			for _, line := range lines {
				var entry struct{ Ts string }
				require.NoError(t, json.Unmarshal([]byte(line), &entry))
				ts, err := audit.ParseLineTS(entry.Ts)
				require.NoError(t, err)
				assert.Greater(t, ts, previous, "the shared lock must allocate increasing timestamps")
				previous = ts
			}
			assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"))
			assert.NoDirExists(t, filepath.Join(home, ".punt-labs", "ethos", "sessions"))
		})
	}
}

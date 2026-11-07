//go:build linux || darwin

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func repairPut(t *testing.T, path, data string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
}

func repairRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

func TestCLIRepairYesRetainsRecordsAndIndexThenReruns(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "real CLI must build")
	se := setupCLISubprocessEnv(t)
	m := filepath.Join(se.repo, ".punt-labs", "ethos", "missions", "m-old")
	obsolete := []string{
		filepath.Join(m, "log.jsonl"), filepath.Join(m, "reflections.yaml"),
		filepath.Join(m, "delegations", "d-old", "prompt.md"),
		filepath.Join(m, audit.MissionChunkFile("sid", 100, 200)+".corrupt"),
		filepath.Join(m, strings.TrimSuffix(audit.MissionChunkFile("sid", 100, 200), ".jsonl")+".quarantine"),
		filepath.Join(m, audit.MissionTempFile("sid", 100, 200)),
		filepath.Join(m, ".lock"),
	}
	for _, path := range obsolete {
		repairPut(t, path, "raw obsolete bytes\x00")
	}
	retained := []string{
		filepath.Join(m, "contract.yaml"), filepath.Join(m, "results.yaml"), filepath.Join(m, "corrections.yaml"),
		filepath.Join(m, "delegations", "d-old", "record.yaml"), filepath.Join(m, "artifacts", "my.ethos-tmp"),
		filepath.Join(se.repo, ".punt-labs", "ethos", "identities", "test-agent.ext", "secret.local.yaml"),
		filepath.Join(se.repo, ".punt-labs", "ethos", "adrs", "des-1.yaml"),
		filepath.Join(se.repo, ".punt-labs", "ethos", "provenance", "proof.json"),
		filepath.Join(se.repo, ".punt-labs", "local", "ethos", "trace", "missions", "m-old", "log.jsonl"),
		filepath.Join(se.home, ".punt-labs", "ethos", "sessions", "global.yaml"),
		filepath.Join(se.repo, "unrelated.go"),
	}
	for _, path := range retained {
		repairPut(t, path, "fixture: preserved\n")
	}
	full := filepath.Join(m, audit.MissionChunkFile("full", 300, 400))
	repairPut(t, full, "{\"ts\":\"2026-01-01T00:00:00Z\",\"record\":\"full\"}\n")
	gitRunEnv(t, se.repo, se.env, "add", "--", ".punt-labs/ethos", "unrelated.go")
	index := filepath.Join(se.repo, ".git", "index")
	before := repairRead(t, index)
	alternate := filepath.Join(t.TempDir(), "index")
	require.NoError(t, os.WriteFile(alternate, before, 0o600))
	se.env = append(se.env, "GIT_INDEX_FILE="+alternate)
	stdout, stderr, exit := runCLI(t, se, "repair", "--dry-run")
	require.Equal(t, 0, exit, stderr)
	assert.Contains(t, stdout, "7 files")
	for _, path := range obsolete {
		assert.FileExists(t, path)
	}
	stdout, stderr, exit = runCLI(t, se, "repair", "--yes")
	require.Equal(t, 0, exit, stderr)
	assert.Contains(t, stdout, "permanently deletes")
	assert.Contains(t, stdout, "Stop ALL old/new writers")
	assert.Contains(t, stdout, "manual retirement")
	assert.Contains(t, stdout, "tracked=true")
	assert.Contains(t, stdout, "Deleted 7 files")
	assert.Contains(t, stdout, "Next: ethos enable")
	for _, path := range obsolete {
		assert.NoFileExists(t, path)
	}
	for _, path := range retained {
		assert.Equal(t, "fixture: preserved\n", string(repairRead(t, path)))
	}
	assert.FileExists(t, full)
	assert.Equal(t, before, repairRead(t, index))
	assert.Equal(t, before, repairRead(t, alternate))
	stdout, stderr, exit = runCLI(t, se, "repair", "--yes", "--json")
	require.Equal(t, 0, exit, stderr)
	assert.JSONEq(t, `{"items":[],"removed":null}`, stdout)
	assert.Equal(t, before, repairRead(t, alternate))
}

// Use a real PTY for stdin, not an in-process "isTerminal" bypass. The Python
// standard library avoids adding a PTY dependency to the product's go.mod.
func repairWithTerminal(t *testing.T, se *cliSubprocessEnv, answer string) (stdout, stderr string, exit int) {
	t.Helper()
	require.NotEmpty(t, ethosBinary)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-c", `
import json, os, pty, subprocess, sys
master, slave = pty.openpty()
p = subprocess.Popen([sys.argv[1], 'repair'], stdin=slave, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
os.close(slave)
os.write(master, sys.argv[2].encode())
out, err = p.communicate(timeout=10)
os.close(master)
print(json.dumps({'stdout': out.decode(), 'stderr': err.decode(), 'exit': p.returncode}))
`, ethosBinary, answer)
	cmd.Dir = se.repo
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var result struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
		Exit   int    `json:"exit"`
	}
	require.NoError(t, json.Unmarshal(data, &result))
	t.Logf("terminal answer %q: exit=%d stdout=%s stderr=%s", answer, result.Exit, result.Stdout, result.Stderr)
	return result.Stdout, result.Stderr, result.Exit
}

func TestCLIRepairTerminalConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		deleted      bool
	}{
		{"yes", "y\n", true}, {"uppercase", "Y\n", true},
		{"default", "\n", false}, {"no", "n\n", false}, {"eof", "\x04", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			path := filepath.Join(se.repo, ".punt-labs", "ethos", "missions", "m-old", "log.jsonl")
			repairPut(t, path, "old raw history")
			repairPut(t, filepath.Join(filepath.Dir(path), "contract.yaml"), "mission_id: m-old\n")
			stdout, stderr, exit := repairWithTerminal(t, se, tc.answer)
			require.Equal(t, 0, exit, stderr)
			assert.Contains(t, stdout, "Are you sure? [y/N]")
			assert.Contains(t, stdout, path)
			if tc.deleted {
				assert.NoFileExists(t, path)
			} else {
				assert.Equal(t, "old raw history", string(repairRead(t, path)))
				assert.NoDirExists(t, filepath.Join(se.repo, ".punt-labs", "local"))
			}
		})
	}
}

func TestCLIRepairNonTerminalAndBusyLocksRefuse(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	path := filepath.Join(se.repo, ".punt-labs", "ethos", "missions", "m-old", "log.jsonl")
	repairPut(t, path, "old")
	repairPut(t, filepath.Join(filepath.Dir(path), "contract.yaml"), "mission_id: m-old\n")
	_, stderr, exit := runCLI(t, se, "repair")
	require.Equal(t, 1, exit)
	assert.Contains(t, stderr, "requires a terminal")
	assert.Contains(t, stderr, "--yes")
	assert.FileExists(t, path)
	p, err := layout.NewRepository(se.repo, se.repo, layout.RecordBlame)
	require.NoError(t, err)
	for _, current := range []bool{false, true} {
		lock := filepath.Join(filepath.Dir(path), ".lock")
		if current {
			lock, err = p.LockPath("mission-state", "m-old")
			require.NoError(t, err)
		}
		repairPut(t, lock, "")
		f, err := os.OpenFile(lock, os.O_RDWR, 0)
		require.NoError(t, err)
		require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
		_, stderr, exit = runCLI(t, se, "repair", "--yes")
		assert.Equal(t, 1, exit)
		assert.Contains(t, stderr, "busy or unavailable lock")
		assert.FileExists(t, path)
		assert.FileExists(t, lock)
		require.NoError(t, f.Close())
	}
}

func TestCLIRepairPreservesFullExportUnderBlame(t *testing.T) {
	for _, portable := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical", true: "portable-flat-only"}[portable], func(t *testing.T) {
			se, _, id := fullExportFixture(t)
			destination := se
			if portable {
				destination = setupCLISubprocessEnv(t)
			}
			root := filepath.Join(destination.repo, ".punt-labs", "ethos", "missions")
			// Omitted record defaults to blame. Explicit --full export is consent to
			// export the trace, not permission to rewrite the events' captured mode.
			_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", root)
			require.Equal(t, 0, exit, stderr)
			assert.FileExists(t, filepath.Join(root, id+".contract.yaml"))
			if portable {
				assert.NoFileExists(t, filepath.Join(root, id, "contract.yaml"))
			}
			files := map[string][]byte{}
			chunks := 0
			err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files[path] = b
				if strings.HasPrefix(entry.Name(), "log-") && strings.HasSuffix(entry.Name(), ".jsonl") {
					chunks++
					assert.Contains(t, string(b), `"record":"blame"`)
					assert.NotContains(t, string(b), `"record":"full"`)
				}
				return nil
			})
			require.NoError(t, err)
			require.Positive(t, chunks, "exercise actual exported event chunks, not an empty export")
			stdout, stderr, exit := runCLI(t, destination, "repair", "--yes")
			require.Equal(t, 0, exit, stderr)
			assert.Contains(t, stdout, "Deleted 0 files")
			for path, before := range files {
				assert.Equal(t, before, repairRead(t, path), path)
			}
			// Repeat to prove that repair doesn't consume an export as stale runtime.
			_, stderr, exit = runCLI(t, destination, "repair", "--yes")
			require.Equal(t, 0, exit, stderr)
			for path, before := range files {
				assert.Equal(t, before, repairRead(t, path), path)
			}
		})
	}
}

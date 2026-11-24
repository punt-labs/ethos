//go:build linux || darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/require"
)

func loggingProcess(t *testing.T, home, cwd string, args ...string) *exec.Cmd {
	t.Helper()
	require.NotEmpty(t, ethosBinary, "the real CLI must build")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, ethosBinary, args...)
	cmd.Dir = cwd
	cmd.Env = withForcedNotUnderClaudeCode([]string{"HOME=" + home, "USER=logging-private-login", "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"})
	return cmd
}

func diagnosticLog(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".punt-labs", "ethos", "logs")
	path := filepath.Join(dir, "ethos.log")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	for p, mode := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		require.Equal(t, mode, info.Mode().Perm())
	}
	return string(b)
}

func privateObservabilityPath(home, path string) bool {
	root := filepath.Join(home, ".punt-labs", "ethos")
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	switch rel {
	case "logs", "telemetry.db", "telemetry.db-journal", "telemetry.db-wal", "telemetry.db-shm", ".telemetry.lock":
		return true
	}
	return false
}

func TestSharedLoggingCLI(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			args := []string{"version"}
			if failed {
				args = append(args, "--private-token=ghp_abcdefghijklmnopqrstuvwxyz0123456789", filepath.Join(home, "private-content"))
			}
			cmd := loggingProcess(t, home, cwd, args...)
			if failed {
				require.Error(t, cmd.Run())
			} else {
				require.NoError(t, cmd.Run())
			}
			log := diagnosticLog(t, home)
			require.Equal(t, 1, strings.Count(log, "Invocation finished"), log)
			require.Regexp(t, `(?m)^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \[INFO\] ethos.cli: Invocation finished`, log)
			require.Contains(t, log, "command=version")
			if failed {
				require.Contains(t, log, "outcome=failed")
				require.Contains(t, log, "exit_code=2")
				require.Contains(t, log, "error_type=")
			} else {
				require.Contains(t, log, "outcome=completed")
				require.Contains(t, log, "exit_code=0")
			}
			require.Contains(t, log, "duration_ms=")
			for _, private := range []string{home, cwd, "logging-private-login", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "private-content", "private-token"} {
				require.NotContains(t, log, private)
			}
		})
	}
}

func TestSharedLoggingHook(t *testing.T) {
	home, cwd := t.TempDir(), testhelpers.OutsideRepo(t)
	cmd := loggingProcess(t, home, cwd, "hook", "format-output")
	cmd.Env = append(cmd.Env, "GIT_CEILING_DIRECTORIES="+os.Getenv("GIT_CEILING_DIRECTORIES"))
	cmd.Stdin = strings.NewReader(`{"tool_name":"unrelated","tool_response":{}}`)
	out, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "{\"hookSpecificOutput\":{\"hookEventName\":\"PostToolUse\",\"updatedMCPToolOutput\":\"{}\"}}\n", string(out), "hook JSON must remain unchanged")
	log := diagnosticLog(t, home)
	require.Contains(t, log, "[INFO] ethos.hook: Invocation finished command=hook format-output outcome=completed")
	require.Equal(t, 1, strings.Count(log, "Invocation finished"))
	require.Contains(t, log, "project=none")
	require.Contains(t, log, "tool=unrelated")
	require.NotContains(t, log, "session_id=")
	require.NotContains(t, log, "agent_id=")
	require.NotContains(t, log, "agent=")
}

func TestSharedLoggingHookToolContext(t *testing.T) {
	for _, tc := range []struct {
		name, hook, project string
		invalidStore        bool
	}{
		{"audit", "audit-log", "logging-project", false},
		{"pre-tool", "pre-tool-use", "logging-project", false},
		{"unresolved-store", "format-output", "unknown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			repo := filepath.Join(t.TempDir(), "logging-project")
			require.NoError(t, os.Mkdir(repo, 0o700))
			runGit(t, repo, "init", "-q")
			runGit(t, repo, "commit", "--allow-empty", "-qm", "init")
			checkout := filepath.Join(t.TempDir(), "logging-worktree")
			runGit(t, repo, "worktree", "add", "--detach", checkout)
			subdir := filepath.Join(checkout, "nested")
			require.NoError(t, os.Mkdir(subdir, 0o700))
			cmd := loggingProcess(t, home, subdir, "hook", tc.hook)
			if tc.invalidStore {
				cmd.Env = append(cmd.Env, "ETHOS_REPO_ROOT="+checkout)
			}
			cmd.Env = append(cmd.Env, "ETHOS_SESSION=outer-session", "ETHOS_AGENT_ID=outer-agent")
			cmd.Stdin = strings.NewReader(`{"session_id":"fixture","agent_id":"worker-01","agent_type":"implementer","tool_name":"Bash","tool_input":{"command":"do-not-log-payload-arguments"}}`)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			log := diagnosticLog(t, home)
			require.Regexp(t, "Invocation finished command=hook "+tc.hook+" .*project="+tc.project+".*checkout=logging-worktree.*tool=Bash", log)
			require.NotContains(t, log, repo)
			require.NotContains(t, log, checkout)
			require.NotContains(t, log, "do-not-log-payload-arguments")
			require.Contains(t, log, "session_id=fixture")
			require.Contains(t, log, "agent_id=worker-01")
			require.Contains(t, log, "agent=implementer")
			require.NotContains(t, log, "outer-session")
			require.NotContains(t, log, "outer-agent")
		})
	}
}

func TestSharedLoggingHookSessionSwitch(t *testing.T) {
	for _, tc := range []struct {
		name, inheritedSession string
		keepAgent              bool
	}{
		{"nested session", "outer-session", false},
		{"same session", "payload-session", true},
		{"no inherited session", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), testhelpers.OutsideRepo(t)
			cmd := loggingProcess(t, home, cwd, "hook", "format-output")
			cmd.Env = append(cmd.Env, "ETHOS_SESSION="+tc.inheritedSession, "ETHOS_AGENT_ID=worker", "CLAUDE_AGENT_TYPE=implementer")
			cmd.Stdin = strings.NewReader(`{"session_id":"payload-session","tool_name":"Bash"}`)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			log := diagnosticLog(t, home)
			require.Regexp(t, "Invocation finished .*session_id=payload-session", log)
			for _, field := range []string{"agent_id=worker", "agent=implementer"} {
				if tc.keepAgent {
					require.Contains(t, log, field)
				} else {
					require.NotContains(t, log, field)
				}
			}
		})
	}
}

func TestSharedLoggingHookUnsafeIdentity(t *testing.T) {
	for _, agent := range []string{"../not-a-persona", "C:private-agent"} {
		home, cwd := t.TempDir(), testhelpers.OutsideRepo(t)
		cmd := loggingProcess(t, home, cwd, "hook", "format-output")
		cmd.Env = append(cmd.Env, "ETHOS_SESSION=env/session", "ETHOS_AGENT_ID=env\\agent", "CLAUDE_AGENT_TYPE=agent\nforge")
		cmd.Stdin = strings.NewReader(`{"session_id":"https://fixture.invalid/path","agent_id":"/private/worker","agent_type":"` + agent + `"}`)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		log := diagnosticLog(t, home)
		for _, field := range []string{"session_id=", "agent_id=", "agent=", "fixture.invalid", "private/worker", agent} {
			require.NotContains(t, log, field)
		}
	}
}

func TestSharedLoggingLifecycleHookIdentity(t *testing.T) {
	for _, hook := range []string{"session-end", "pre-compact", "subagent-start"} {
		t.Run(hook, func(t *testing.T) {
			home, cwd := t.TempDir(), testhelpers.OutsideRepo(t)
			cmd := loggingProcess(t, home, cwd, "hook", hook)
			cmd.Stdin = strings.NewReader(`{"session_id":"lifecycle-fixture","agent_type":"implementer"}`)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Regexp(t, "Invocation finished .*session_id=lifecycle-fixture agent=implementer", diagnosticLog(t, home))
		})
	}
}

func TestSharedLoggingHookRepoDiscoveryFailure(t *testing.T) {
	home, cwd := t.TempDir(), testhelpers.OutsideRepo(t)
	override := filepath.Join(home, "missing-repository")
	cmd := loggingProcess(t, home, cwd, "hook", "format-output")
	cmd.Env = append(cmd.Env, "ETHOS_REPO_ROOT="+override)
	cmd.Stdin = strings.NewReader(`{"tool_name":"fixture"}`)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	log := diagnosticLog(t, home)
	require.Regexp(t, "Invocation finished command=hook format-output .*project=unknown", log)
	require.NotContains(t, log, override)
}

func TestSharedLoggingKilledHooks(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "logging-project")
	require.NoError(t, os.Mkdir(repo, 0o700))
	runGit(t, repo, "init", "-q")
	for _, tc := range []struct{ hook, project string }{
		{"pre-commit", "logging-project"},
		{"subagent-stop", "logging-project"},
		{"post-edit", "logging-project"},
	} {
		t.Run(tc.hook, func(t *testing.T) {
			home := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(home, ".punt-hooks-kill"), nil, 0o600))
			cmd := loggingProcess(t, home, repo, "hook", tc.hook)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Empty(t, out)
			log := diagnosticLog(t, home)
			require.Regexp(t, "Invocation finished command=hook "+tc.hook+" .*project="+tc.project, log)
			require.NotContains(t, log, "Check finished")
		})
	}
}

func TestSharedLoggingHookChecks(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "logging-project")
	dir := filepath.Join(repo, ".punt-labs", "ethos", "checks")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	runGit(t, repo, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), []byte("checks: timing\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "sample.txt"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "timing.yaml"), []byte(`name: timing
checks:
  - name: edit-probe
    stage: edit
    files: ['*.txt']
    run: [sh, -c, 'sleep 0.05']
  - name: commit-probe
    stage: commit
    files: all
    run: [sh, -c, 'sleep 0.05']
  - name: handoff-probe
    stage: handoff
    files: all
    run: [sh, -c, 'sleep 0.05; exit 1']
    on_fail: advisory
`), 0o600))
	runGit(t, repo, "add", "--", "sample.txt")
	for _, tc := range []struct {
		hook, stage, input, outcome string
	}{
		{"post-edit", "edit", `{"session_id":"fixture","tool_name":"Edit","tool_input":{"file_path":"sample.txt"}}`, "completed"},
		{"pre-commit", "commit", "", "completed"},
		{"subagent-stop", "handoff", `{"session_id":"fixture","agent_id":"worker","agent_type":"implementer"}`, "failed"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			home := t.TempDir()
			cmd := loggingProcess(t, home, repo, "hook", tc.hook)
			cmd.Env = append(cmd.Env, "ETHOS_SESSION=fixture", "ETHOS_AGENT_ID=worker", "CLAUDE_AGENT_TYPE=implementer")
			cmd.Stdin = strings.NewReader(tc.input)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			log := diagnosticLog(t, home)
			require.Regexp(t, "Invocation finished command=hook "+tc.hook+" .*project=logging-project", log)
			require.Regexp(t, "Check finished .*stage="+tc.stage+" check="+tc.stage+"-probe outcome="+tc.outcome+" duration_ms=[1-9][0-9]+", log)
			require.Equal(t, 1, strings.Count(log, "Check finished"), log)
			for _, message := range []string{"Invocation finished ", "Check finished "} {
				require.Regexp(t, message+".*session_id=fixture.*agent_id=worker.*agent=implementer", log)
			}
			require.NotContains(t, log, "sleep 0.05")
		})
	}
}

func TestSharedLoggingCheckWorkingDirectory(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "logging-project")
	checksDir := filepath.Join(repo, ".punt-labs", "ethos", "checks")
	require.NoError(t, os.MkdirAll(checksDir, 0o700))
	runGit(t, repo, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), []byte("checks: timing\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(checksDir, "timing.yaml"), []byte(`name: timing
checks:
  - name: package-probe
    stage: commit
    files: all
    cwd: package_dir
    run: [sh, -c, 'true']
`), 0o600))
	for _, dir := range []string{"first", "nested/second"} {
		require.NoError(t, os.MkdirAll(filepath.Join(repo, dir), 0o700))
		file := dir + "/sample.txt"
		require.NoError(t, os.WriteFile(filepath.Join(repo, filepath.FromSlash(file)), nil, 0o600))
		runGit(t, repo, "add", "--", file)
	}
	home := t.TempDir()
	cmd := loggingProcess(t, home, repo, "hook", "pre-commit")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	log := diagnosticLog(t, home)
	require.Regexp(t, "Check finished .*check=package-probe .*dir=first", log)
	require.Regexp(t, "Check finished .*check=package-probe .*dir=nested/second", log)
	require.Equal(t, 2, strings.Count(log, "Check finished"), log)
	require.NotContains(t, log, repo)
}

func TestSharedLoggingMCP(t *testing.T) {
	runLoggingMCP(t)
}

func runLoggingMCP(t *testing.T) string {
	home, _ := runLoggingMCPEnv(t, "")
	return home
}

func runLoggingMCPEnv(t *testing.T, veto string) (string, []byte) {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	cmd := loggingProcess(t, home, cwd, "serve")
	if veto != "" {
		cmd.Env = append(cmd.Env, veto+"=off")
	}
	var transcript []byte
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	// A nil Stderr deliberately discards diagnostics from the real server.
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for _, request := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"logging-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"identity","arguments":{"method":"list"}}}`,
	} {
		_, err = stdin.Write([]byte(request + "\n"))
		require.NoError(t, err)
		require.True(t, scanner.Scan(), "server must answer each request: %v", scanner.Err())
		transcript = append(transcript, scanner.Bytes()...)
		transcript = append(transcript, '\n')
		var response map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &response), scanner.Text())
		require.JSONEq(t, `"2.0"`, string(response["jsonrpc"]))
		require.NotContains(t, response, "error", scanner.Text())
	}
	require.NoError(t, stdin.Close())
	for scanner.Scan() {
		transcript = append(transcript, scanner.Bytes()...)
		transcript = append(transcript, '\n')
		var response map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &response))
		require.JSONEq(t, `"2.0"`, string(response["jsonrpc"]))
	}
	require.NoError(t, scanner.Err())
	require.NoError(t, cmd.Wait())
	log := diagnosticLog(t, home)
	for _, text := range []string{"ethos.mcp: Server started", "ethos.mcp: Server stopped", "ethos.mcp: Tool call finished", "tool=identity", "action=list", "outcome=completed"} {
		require.Contains(t, log, text)
	}
	require.Equal(t, 1, strings.Count(log, "Tool call finished"))
	require.False(t, regexp.MustCompile(`(?m)^.*(logging-private-login|private-content).*$`).MatchString(log))
	return home, transcript
}

func TestSharedLoggingUnavailable(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".punt-labs"), []byte("blocked"), 0o600))
	cmd := loggingProcess(t, home, cwd, "version")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "ethos: private diagnostics unavailable\n", stderr.String())
}

func TestSharedLoggingRotationLockTimeout(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	normal := loggingProcess(t, home, cwd, "version")
	want, err := normal.Output()
	require.NoError(t, err)
	lock, err := os.OpenFile(filepath.Join(home, ".punt-labs", "ethos", "logs", "ethos.log.rotate.lock"), os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX))
	t.Cleanup(func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) })
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ethosBinary, "version")
	cmd.Dir, cmd.Env = cwd, normal.Env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	require.NoError(t, cmd.Run(), "rotation-lock contention must not prevent the command from running")
	require.Less(t, time.Since(started), 6*time.Second)
	require.Equal(t, string(want), stdout.String())
	require.Equal(t, "ethos: private diagnostics unavailable\n", stderr.String())
	log := diagnosticLog(t, home)
	require.Equal(t, 1, strings.Count(log, "Invocation finished"), "the contended invocation uses a discard logger")
}

//go:build linux || darwin

package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// sessionID names the session directory every trailer test writes its
// sidecars into, and the value the tests pass as ETHOS_SESSION. The
// hook resolves the committing session through ethos, so the two must
// agree (ethos-pobi).
const sessionID = "2026-07-29-sess1"

var (
	ethosOnce sync.Once
	ethosDir  string // directory holding the built binary; "" if the build failed
	ethosErr  error
)

// TestMain removes the shared binary directory after the last test.
// The build is cached across tests by ethosOnce, so no single test can
// own its lifetime.
func TestMain(m *testing.M) {
	code := m.Run()
	if ethosDir != "" {
		_ = os.RemoveAll(ethosDir)
	}
	os.Exit(code)
}

// ethosBinDir builds the ethos binary once per test binary and returns
// the directory to prepend to PATH. The commit-msg hook shells out to
// `ethos hook commit-trailers` for the fallback trailer values, so the
// hook cannot be exercised without it.
func ethosBinDir(t *testing.T) string {
	t.Helper()
	ethosOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ethos-hook-bin")
		if err != nil {
			ethosErr = err
			return
		}
		// Record the directory BEFORE the build so TestMain removes it
		// on a failed build too; a build that fails on every run would
		// otherwise leave one temp dir behind per run.
		ethosDir = dir
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "ethos"), "../../cmd/ethos")
		if out, err := cmd.CombinedOutput(); err != nil {
			ethosErr = fmt.Errorf("%w\n%s", err, out)
		}
	})
	if ethosErr != nil {
		t.Fatalf("building ethos binary: %v", ethosErr)
	}
	return ethosDir
}

// hookEnv is the environment the hook runs under: the temp HOME, the
// built binary on PATH, and the committing session named explicitly.
// An empty session leaves ETHOS_SESSION unset, which is the
// "unresolvable session" shape.
func hookEnv(home, binDir, session string) []string {
	env := []string{
		"HOME=" + home,
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	if session != "" {
		env = append(env, "ETHOS_SESSION="+session)
	}
	return env
}

// TestCommitMsgHook_TrailerGate runs the embedded commit-msg hook under
// /bin/sh against a temp git repo and a temp HOME holding session
// sidecars. It proves ethos-jawp: the Mission/Delegation trailers are
// gated on an ACTIVE mission (the active-mission sidecar), not the
// never-cleared delegation-binding. A missionless commit gets no
// trailers even when a stale binding is on disk.
func TestCommitMsgHook_TrailerGate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping")
	}
	binDir := ethosBinDir(t)

	const (
		missionID = "m-2026-07-29-007"
		delegID   = "d-2026-07-29-042"
	)

	tests := []struct {
		name           string
		homeSubdir     string // HOME is this subdir of the temp dir; empty => temp dir itself
		activeMission  string // active-mission sidecar body; empty => no sidecar
		binding        string // delegation-binding body; empty => no file
		wantMission    bool
		wantDelegation bool
	}{
		{
			name:        "missionless commit gets no trailers",
			wantMission: false,
		},
		{
			name:           "active mission with matching binding gets both trailers",
			activeMission:  missionID + "\n",
			binding:        delegID + "\n" + missionID + "\n" + "sess1\n",
			wantMission:    true,
			wantDelegation: true,
		},
		{
			name:          "active mission with no binding gets only the mission trailer",
			activeMission: missionID + "\n",
			wantMission:   true,
		},
		{
			name:        "stale binding without an active mission gets no trailers",
			binding:     delegID + "\n" + missionID + "\n" + "sess1\n",
			wantMission: false,
		},
		{
			name:           "binding for a different mission is not tagged",
			activeMission:  missionID + "\n",
			binding:        delegID + "\n" + "m-2026-07-29-999" + "\n" + "sess1\n",
			wantMission:    true,
			wantDelegation: false,
		},
		{
			// Session discovery once word-split find's output, so any
			// space in HOME lost the sidecar and dropped the trailer.
			name:           "a HOME containing spaces still discovers the session",
			homeSubdir:     "My Home Dir",
			activeMission:  missionID + "\n",
			binding:        delegID + "\n" + missionID + "\n" + "sess1\n",
			wantMission:    true,
			wantDelegation: true,
		},
		{
			// A hand-edited or CRLF sidecar must still match: the
			// mission comparison strips whitespace on both sides.
			name:           "CRLF and trailing space in the sidecars still match",
			activeMission:  missionID + " \r\n",
			binding:        delegID + "\r\n" + missionID + "\r\n" + "sess1\r\n",
			wantMission:    true,
			wantDelegation: true,
		},
		{
			// An empty active-mission file leaves MISSION_ID blank; a
			// blank must not match a binding's blank line 2 and tag a
			// lone Delegation trailer.
			name:           "an empty active-mission file tags nothing",
			activeMission:  "\n",
			binding:        delegID + "\n\nsess1\n",
			wantMission:    false,
			wantDelegation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.homeSubdir != "" {
				home = filepath.Join(home, tt.homeSubdir)
			}
			sessDir := filepath.Join(home, ".punt-labs", "ethos", "sessions", sessionID)
			if err := os.MkdirAll(sessDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if tt.activeMission != "" {
				writeFile(t, filepath.Join(sessDir, "active-mission"), tt.activeMission)
			}
			if tt.binding != "" {
				writeFile(t, filepath.Join(sessDir, "delegation-binding"), tt.binding)
			}

			repo := t.TempDir()
			runGit(t, repo, "init")
			writeFile(t, filepath.Join(repo, ".punt-labs", "ethos", "enabled"), "")

			msgFile := filepath.Join(repo, "COMMIT_EDITMSG")
			writeFile(t, msgFile, "feat: a change\n")

			script := filepath.Join(t.TempDir(), "commit-msg.sh")
			writeFile(t, script, string(CommitMsg))
			if err := os.Chmod(script, 0o755); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("/bin/sh", script, msgFile)
			cmd.Dir = repo
			cmd.Env = hookEnv(home, binDir, sessionID)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("hook failed: %v\n%s", err, out)
			}

			data, err := os.ReadFile(msgFile)
			if err != nil {
				t.Fatal(err)
			}
			msg := string(data)

			gotMission := strings.Contains(msg, "Mission: "+missionID)
			if gotMission != tt.wantMission {
				t.Errorf("Mission trailer = %v, want %v\nmessage:\n%s", gotMission, tt.wantMission, msg)
			}
			gotDelegation := strings.Contains(msg, "Delegation: "+delegID)
			if gotDelegation != tt.wantDelegation {
				t.Errorf("Delegation trailer = %v, want %v\nmessage:\n%s", gotDelegation, tt.wantDelegation, msg)
			}
		})
	}
}

// TestCommitMsgHook_ResolvesCommittingSession pins ethos-pobi: with
// two sessions each holding an active mission, the trailer names the
// mission of the session that is COMMITTING — never the newest
// session directory. The hook used to reverse-sort the session dirs
// and take the first with a sidecar, so a commit from the older
// session was stamped with the newer session's mission and delegation.
func TestCommitMsgHook_ResolvesCommittingSession(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping")
	}
	binDir := ethosBinDir(t)

	const (
		olderSession  = "2026-07-29-sess-old"
		olderMission  = "m-2026-07-29-007"
		olderDeleg    = "d-2026-07-29-042"
		newerSession  = "2026-07-31-sess-new"
		newerMission  = "m-2026-07-31-004"
		newerDelegate = "d-2026-07-31-011"
	)

	tests := []struct {
		name           string
		session        string // ETHOS_SESSION; empty => unresolvable
		wantMission    string // "" => no Mission trailer
		wantDelegation string // "" => no Delegation trailer
	}{
		{
			// The bug: reverse-sorted discovery handed this commit
			// the newer session's mission.
			name:           "older session gets its own mission",
			session:        olderSession,
			wantMission:    olderMission,
			wantDelegation: olderDeleg,
		},
		{
			name:           "newer session gets its own mission",
			session:        newerSession,
			wantMission:    newerMission,
			wantDelegation: newerDelegate,
		},
		{
			// No ETHOS_SESSION and no session file for any Claude
			// ancestor under this HOME: the committing session is
			// unresolvable, so no trailer is added at all.
			name:        "unresolvable session gets no trailer",
			session:     "",
			wantMission: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			stageSession(t, home, olderSession, olderMission, olderDeleg)
			stageSession(t, home, newerSession, newerMission, newerDelegate)

			repo := t.TempDir()
			runGit(t, repo, "init")
			writeFile(t, filepath.Join(repo, ".punt-labs", "ethos", "enabled"), "")

			msgFile := filepath.Join(repo, "COMMIT_EDITMSG")
			writeFile(t, msgFile, "feat: a change\n")

			script := filepath.Join(t.TempDir(), "commit-msg.sh")
			writeFile(t, script, string(CommitMsg))
			if err := os.Chmod(script, 0o755); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("/bin/sh", script, msgFile)
			cmd.Dir = repo
			cmd.Env = hookEnv(home, binDir, tt.session)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("hook failed: %v\n%s", err, out)
			}

			data, err := os.ReadFile(msgFile)
			if err != nil {
				t.Fatal(err)
			}
			msg := string(data)

			if tt.wantMission == "" {
				if strings.Contains(msg, "Mission: ") {
					t.Errorf("want no Mission trailer, got:\n%s", msg)
				}
				if strings.Contains(msg, "Delegation: ") {
					t.Errorf("want no Delegation trailer, got:\n%s", msg)
				}
				return
			}
			if !strings.Contains(msg, "Mission: "+tt.wantMission) {
				t.Errorf("want Mission: %s, got:\n%s", tt.wantMission, msg)
			}
			if !strings.Contains(msg, "Delegation: "+tt.wantDelegation) {
				t.Errorf("want Delegation: %s, got:\n%s", tt.wantDelegation, msg)
			}
			// The other session's mission must not appear anywhere.
			other := olderMission
			if tt.wantMission == olderMission {
				other = newerMission
			}
			if strings.Contains(msg, other) {
				t.Errorf("another session's mission %s leaked into:\n%s", other, msg)
			}
		})
	}
}

// TestCommitMsgHook_OldBinaryPrintsHelp pins the version-skew hole
// rsc found: an ethos predating `hook commit-trailers` answers the
// unknown subcommand by printing the hook group's HELP on stdout and
// exiting 0. The `||` fallback never fires, no KEY=value line is
// found, and the trailer disappears without a word — the silent drop
// the fallback exists to prevent. The hook must recognize help-shaped
// output as failure and say so.
func TestCommitMsgHook_OldBinaryPrintsHelp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping")
	}

	home := t.TempDir()
	stageSession(t, home, sessionID, "m-2026-07-29-007", "d-2026-07-29-042")

	// A stub standing in for the old binary: help on stdout, exit 0.
	binDir := t.TempDir()
	writeFile(t, filepath.Join(binDir, "ethos"), `#!/bin/sh
cat <<'HELP'
Internal hook dispatcher (not for direct use)

Usage:
  ethos hook [command]

Available Commands:
  audit-log       PostToolUse audit logger
HELP
exit 0
`)
	if err := os.Chmod(filepath.Join(binDir, "ethos"), 0o755); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	runGit(t, repo, "init")
	writeFile(t, filepath.Join(repo, ".punt-labs", "ethos", "enabled"), "")

	msgFile := filepath.Join(repo, "COMMIT_EDITMSG")
	writeFile(t, msgFile, "feat: a change\n")

	script := filepath.Join(t.TempDir(), "commit-msg.sh")
	writeFile(t, script, string(CommitMsg))
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", script, msgFile)
	cmd.Dir = repo
	cmd.Env = hookEnv(home, binDir, sessionID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hook failed: %v\n%s", err, out)
	}

	data, err := os.ReadFile(msgFile)
	if err != nil {
		t.Fatal(err)
	}
	msg := string(data)
	if strings.Contains(msg, "Mission: ") || strings.Contains(msg, "Usage:") {
		t.Errorf("help output must not reach the commit message:\n%s", msg)
	}
	if !strings.Contains(string(out), "does not support") {
		t.Errorf("the version mismatch must be reported, got:\n%s", out)
	}
}

// stageSession writes one session's active-mission and
// delegation-binding sidecars under home.
func stageSession(t *testing.T, home, session, missionID, delegationID string) {
	t.Helper()
	dir := filepath.Join(home, ".punt-labs", "ethos", "sessions", session)
	writeFile(t, filepath.Join(dir, "active-mission"), missionID+"\n")
	writeFile(t, filepath.Join(dir, "delegation-binding"),
		delegationID+"\n"+missionID+"\n"+session+"\n")
}

// writeFile writes content to path, creating parent dirs, failing the
// test on any error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runGit runs git in dir with an isolated environment, failing the test
// on any error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

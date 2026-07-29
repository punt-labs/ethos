//go:build linux || darwin

package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

	const (
		missionID = "m-2026-07-29-007"
		delegID   = "d-2026-07-29-042"
	)

	tests := []struct {
		name           string
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			sessDir := filepath.Join(home, ".punt-labs", "ethos", "sessions", "2026-07-29-sess1")
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
			cmd.Env = append(os.Environ(), "HOME="+home)
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

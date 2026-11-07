//go:build linux || darwin

package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	testhelpers.MustIsolateGit() // no inherited GIT_DIR; temp dirs stop at the temp root
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
	cmd := testhelpers.GitCommand(args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Env,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// enabledMessageRepo exercises the CLI's actual enable/chain entry point in an
// isolated fixture, not a hand-written approximation of an installed hook.
func enabledMessageRepo(t *testing.T, linked bool) (store, checkout, hooksDir string, env []string) {
	t.Helper()
	store, home := t.TempDir(), t.TempDir()
	bin := ethosBinDir(t)
	runGit(t, store, "init", "-q")
	checkout = store
	if linked {
		// Git writes are confined to this disposable fixture, never the checkout
		// under development. A linked worktree requires a base commit.
		runGit(t, store, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "fixture")
		checkout = filepath.Join(t.TempDir(), "feature")
		runGit(t, store, "worktree", "add", "-q", "--detach", checkout)
	}
	env = append(hookEnv(home, bin, sessionID), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	stageSession(t, home, sessionID, "m-2026-11-04-043", "d-2026-11-04-043")
	cmd := exec.Command(filepath.Join(bin, "ethos"), "enable")
	cmd.Dir, cmd.Env = checkout, env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "enable must really install the hooks: %s", out)
	hooksDir = filepath.Join(store, ".git", "hooks")
	for _, name := range []string{"pre-commit", "commit-msg"} {
		data, err := os.ReadFile(filepath.Join(hooksDir, name))
		require.NoError(t, err)
		ident := SealIdent
		if name == "commit-msg" {
			ident = TrailerIdent
		}
		assert.Contains(t, string(data), ident, "installed ownership fingerprint is unchanged")
	}
	return store, checkout, hooksDir, env
}

func TestEnabledInstalledCommitMessageTrailersAndHostStatus(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("linked=%t", linked), func(t *testing.T) {
			store, checkout, hooksDir, env := enabledMessageRepo(t, linked)
			msg := filepath.Join(checkout, "MSG")
			writeFile(t, msg, "subject\n")
			for range 2 {
				cmd := exec.Command(filepath.Join(hooksDir, "commit-msg"), msg)
				cmd.Dir, cmd.Env = checkout, env
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, string(out))
				data, err := os.ReadFile(msg)
				require.NoError(t, err)
				assert.Equal(t, "subject\n\nMission: m-2026-11-04-043\nDelegation: d-2026-11-04-043\n", string(data))
			}
			assert.DirExists(t, filepath.Join(store, ".punt-labs", "local", "ethos", "tmp"))
			if linked {
				assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"))
			}
			// Chaining a foreign fall-through status must remain transparent even
			// when the helper succeeds. Its two trailer IDs are supplied via env.
			body, err := os.ReadFile(filepath.Join(hooksDir, "commit-msg"))
			require.NoError(t, err)
			script := filepath.Join(t.TempDir(), "chained-commit-msg")
			require.NoError(t, os.WriteFile(script, append([]byte("#!/bin/sh\n/bin/sh -c 'exit 7'\n"), body...), 0o700))
			cmd := exec.Command(script, msg)
			cmd.Dir = checkout
			cmd.Env = append(env, "MISSION_ID=m-2026-11-04-043", "DELEGATION_ID=d-2026-11-04-043")
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, string(out))
			assert.Equal(t, 7, exit.ExitCode())
		})
	}
}

// Observe syscalls, including files removed before exit, rather than treating a
// clean post-run directory listing as proof that temporary placement was safe.
// The rename/fsync injections exercise the installed shell -> real CLI path.
// Pre-commit is seeded with a real policy-aware audit append, so its successful
// case observes archival too. Full publication and archival faults have their
// own, less restrictive write assertions in pre_commit_archival_placement_test.go.
func TestEnabledInstalledHooksTransientWrites(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall tracing/injection acceptance is Linux-specific")
	}
	strace, err := exec.LookPath("strace")
	if err != nil {
		t.Skip("transient placement acceptance requires strace; not established on this host")
	}
	for _, linked := range []bool{false, true} {
		for _, inside := range []bool{false, true} {
			t.Run(fmt.Sprintf("linked=%t/TMPDIR-record=%t", linked, inside), func(t *testing.T) {
				store, checkout, hooksDir, env := enabledMessageRepo(t, linked)
				tmp := t.TempDir()
				if inside {
					tmp = filepath.Join(checkout, ".punt-labs", "ethos")
				}
				bin := t.TempDir()
				// Fail immediately if either hook reintroduces shell temporary files.
				mktempCall := filepath.Join(t.TempDir(), "mktemp-called")
				writeFile(t, filepath.Join(bin, "mktemp"), "#!/bin/sh\ntouch '"+mktempCall+"'\nexit 99\n")
				require.NoError(t, os.Chmod(filepath.Join(bin, "mktemp"), 0o700))
				env = append(env, "TMPDIR="+tmp, "PATH="+bin+":"+ethosBinDir(t)+":"+os.Getenv("PATH"), "GIT_TRACE="+filepath.Join(tmp, "trace"), "GIT_TRACE2_EVENT="+filepath.Join(tmp, "trace-event"))
				msg := filepath.Join(checkout, "MSG")
				for _, tc := range []struct{ hook, inject, want string }{
					{"pre-commit", "", ""},
					{"commit-msg", "", ""},
					{"commit-msg", "renameat:error=EXDEV", "cross-device"},
					{"commit-msg", "fsync:error=EIO", "syncing commit message stage"},
					{"commit-msg", "renameat:error=EINTR:signal=SIGKILL", "signal"},
				} {
					t.Run(tc.hook+"/"+tc.inject, func(t *testing.T) {
						var live []byte
						if tc.hook == "pre-commit" {
							live = seedInstalledAudit(t, store, checkout, env)
						}
						writeFile(t, msg, "original\n")
						trace := filepath.Join(t.TempDir(), "syscalls")
						args := []string{"-f", "-s", "4096", "-o", trace, "-e", "trace=%file,fsync"}
						if tc.inject != "" {
							args = append(args, "-e", "inject="+tc.inject)
						}
						args = append(args, filepath.Join(hooksDir, tc.hook), msg)
						cmd := exec.Command(strace, args...)
						cmd.Dir, cmd.Env = checkout, env
						out, err := cmd.CombinedOutput()
						if tc.want == "" {
							require.NoError(t, err, string(out))
						} else {
							require.Error(t, err, string(out))
							if tc.want != "signal" {
								assert.Contains(t, string(out), tc.want)
							}
						}
						data, err := os.ReadFile(msg)
						require.NoError(t, err)
						if tc.want != "" || tc.hook == "pre-commit" {
							assert.Equal(t, "original\n", string(data), "all pre-install failures preserve bytes")
						} else {
							assert.Contains(t, string(data), "Mission: m-2026-11-04-043\nDelegation: d-2026-11-04-043\n")
						}
						calls, err := os.ReadFile(trace)
						require.NoError(t, err)
						local := filepath.Join(store, ".punt-labs", "local", "ethos")
						staged := false
						for _, line := range strings.Split(string(calls), "\n") {
							if strings.Contains(line, "O_CREAT") && !strings.Contains(line, `"/dev/null"`) {
								assert.Contains(t, line, local+"/", "every transient created file must be S-local: %s", line)
								t.Log(line)
							}
							if strings.Contains(line, "renameat(") && strings.Contains(line, "commit-message-") && strings.Contains(line, ".ethos-tmp") {
								staged = true
								assert.Contains(t, line, filepath.Join(local, "tmp")+"/")
								t.Log(line)
							}
							assert.NotContains(t, line, "\""+msg+".", "never stage beside the message")
						}
						if tc.hook == "commit-msg" && !strings.HasPrefix(tc.inject, "fsync") {
							assert.True(t, staged, "observe actual helper staging at rename, not just cleanup")
						}
						assert.NoFileExists(t, mktempCall)
						assert.NoFileExists(t, filepath.Join(tmp, "trace"))
						assert.NoFileExists(t, filepath.Join(tmp, "trace-event"))
						if tc.hook == "pre-commit" {
							assertInstalledArchive(t, store, live)
							assert.Contains(t, string(calls), "linkat(", "archival installation actually executed")
							assert.Contains(t, string(calls), "audit-", "trace reaches the audit chunk path")
						}
						if tc.want == "signal" {
							// An uncatchable interruption can retain an S-local stage,
							// but its lock must be released and a normal retry must work.
							retry := exec.Command(filepath.Join(hooksDir, "commit-msg"), msg)
							retry.Dir, retry.Env = checkout, env
							out, err := retry.CombinedOutput()
							require.NoError(t, err, string(out))
							data, err := os.ReadFile(msg)
							require.NoError(t, err)
							assert.Equal(t, "original\n\nMission: m-2026-11-04-043\nDelegation: d-2026-11-04-043\n", string(data))
						}
					})
				}
			})
		}
	}
}

func TestEnabledInstalledCommitMessageRenderWriteFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall injection acceptance is Linux-specific")
	}
	strace, err := exec.LookPath("strace")
	if err != nil {
		t.Skip("failed-write acceptance requires strace; not established on this host")
	}
	_, checkout, hooksDir, env := enabledMessageRepo(t, false)
	bin, trace := t.TempDir(), filepath.Join(t.TempDir(), "write-failure")
	// Trace the helper and its threads, detaching children on exec. Its first
	// write supplies the in-memory original to Git's stdin. A failed pipe write
	// must be reported rather than installing partial output or plain-appending.
	// Git's stdout, the shell root probe and session lookup are not faulted.
	wrapper := "#!/bin/sh\nif [ \"$1 $2\" = 'hook commit-message' ]; then\nexec '" + strace + "' -f -b execve -o '" + trace + "' -e trace=write,fsync,renameat -e inject=write:error=ENOSPC:when=1 '" + filepath.Join(ethosBinDir(t), "ethos") + "' \"$@\"\nfi\nexec '" + filepath.Join(ethosBinDir(t), "ethos") + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ethos"), []byte(wrapper), 0o700))
	msg := filepath.Join(checkout, "MSG")
	writeFile(t, msg, "original bytes\n")
	cmd := exec.Command(filepath.Join(hooksDir, "commit-msg"), msg)
	cmd.Dir = checkout
	cmd.Env = append(env, "PATH="+bin+":"+os.Getenv("PATH"), "MISSION_ID=m-2026-11-04-043", "DELEGATION_ID=d-2026-11-04-043")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))
	// Injection is per thread: it can also fault the error-report write on a
	// different thread. The exit status, injected syscall and unchanged bytes
	// are the evidence here; do not depend on faulted stderr being printable.
	data, err := os.ReadFile(msg)
	require.NoError(t, err)
	assert.Equal(t, "original bytes\n", string(data))
	calls, err := os.ReadFile(trace)
	require.NoError(t, err)
	assert.Contains(t, string(calls), "ENOSPC")
	assert.Contains(t, string(calls), "(INJECTED)")
	assert.NotContains(t, string(calls), "renameat(")
	t.Log(string(calls))
}

func TestEnabledInstalledHooksOrdinaryCommit(t *testing.T) {
	_, checkout, _, env := enabledMessageRepo(t, false)
	cmd := exec.Command("git", "-C", checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "ordinary commit")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "real Git must run the actual enabled hooks: %s", out)
	cmd = exec.Command("git", "-C", checkout, "log", "-1", "--format=%B")
	cmd.Env = env
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, "ordinary commit\n\nMission: m-2026-11-04-043\nDelegation: d-2026-11-04-043\n\n", string(out))
}

func TestEnabledInstalledHooksIgnoreTraceConfigOnStdout(t *testing.T) {
	_, checkout, hooksDir, env := enabledMessageRepo(t, false)
	runGit(t, checkout, "config", "trace2.normalTarget", "/dev/stdout")
	runGit(t, checkout, "config", "trace2.eventTarget", "/dev/stdout")
	msg := filepath.Join(checkout, "MSG")
	writeFile(t, msg, "subject\n")
	for _, name := range []string{"pre-commit", "commit-msg"} {
		cmd := exec.Command(filepath.Join(hooksDir, name), msg)
		cmd.Dir, cmd.Env = checkout, env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		assert.NotContains(t, string(out), "built-in")
		assert.NotContains(t, string(out), `"event":`)
	}
	data, err := os.ReadFile(msg)
	require.NoError(t, err)
	assert.Equal(t, "subject\n\nMission: m-2026-11-04-043\nDelegation: d-2026-11-04-043\n", string(data))
}

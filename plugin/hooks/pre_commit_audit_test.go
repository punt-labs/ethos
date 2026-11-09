//go:build linux || darwin

package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreCommitAuditInstallerFingerprint(t *testing.T) {
	lines := strings.SplitN(string(PreCommit), "\n", 3)
	require.Len(t, lines, 3)
	assert.Contains(t, lines[1], SealIdent, "standalone and chained hook ownership must remain recognizable to the installer")
}

// The shim makes one call, `ethos hook pre-commit`, and maps its status: a
// failure blocks with exit 2 whatever the binary exited with, success returns
// the captured host status. A binary that predates the subcommand fails
// argument validation, and the shim names the remedy.
func TestPreCommitShimDelegatesAndMapsExit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hookExit   string
		hostExit   string
		wantExit   int
		wantStderr string
		wantCalls  string
	}{
		{"success", "0", "0", 0, "fixture policy note", "hook\npre-commit\n"},
		{"policy failure blocks with 2", "2", "0", 2, "fixture policy note", "hook\npre-commit\nhook\npre-commit\n--help\n"},
		{"internal failure blocks with 2", "1", "0", 2, "fixture policy note", "hook\npre-commit\nhook\npre-commit\n--help\n"},
		{"success preserves host failure", "0", "7", 7, "fixture policy note", "hook\npre-commit\n"},
		{"old binary blocks and names the remedy", "unknown", "0", 2, "upgrade ethos: make install", "hook\npre-commit\nhook\npre-commit\n--help\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := t.TempDir(), t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos", "enabled"), nil, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf '%s\\n' \"$FIXTURE_ROOT\"\n"), 0o700))
			// The stub plays the new binary, which prints its help naming the
			// subcommand, or an old one, which rejects it as an unknown command.
			stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$FIXTURE_CALL\"\n" +
				"if [ \"$FIXTURE_HOOK_EXIT\" = unknown ]; then\n  printf 'ethos: unknown command \"pre-commit\" for \"ethos hook\"\\n' >&2\n  exit 2\nfi\n" +
				"case \"$*\" in *--help*) printf 'Usage:\\n  ethos hook pre-commit [flags]\\n'; exit 0 ;; esac\n" +
				"printf 'fixture policy note\\n' >&2\nexit \"$FIXTURE_HOOK_EXIT\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "ethos"), []byte(stub), 0o700))
			call := filepath.Join(root, "call")
			script := filepath.Join(t.TempDir(), "pre-commit")
			body := append([]byte("#!/bin/sh\n/bin/sh -c 'exit "+tc.hostExit+"'\n"), PreCommit...)
			require.NoError(t, os.WriteFile(script, body, 0o700))
			cmd := exec.Command("/bin/sh", script)
			cmd.Dir = root
			cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + bin + ":/usr/bin:/bin", "FIXTURE_ROOT=" + root, "FIXTURE_CALL=" + call, "FIXTURE_HOOK_EXIT=" + tc.hookExit}
			out, err := cmd.CombinedOutput()
			if tc.wantExit != 0 {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				assert.Equal(t, tc.wantExit, exit.ExitCode())
			} else {
				require.NoError(t, err)
			}
			assert.Contains(t, string(out), tc.wantStderr)
			args, err := os.ReadFile(call)
			require.NoError(t, err)
			assert.Equal(t, tc.wantCalls, string(args))
		})
	}
}

func TestHooksRootProbeHasNoTemporaryWritesOrRetry(t *testing.T) {
	for _, hook := range []struct {
		name string
		body []byte
	}{{"pre-commit", PreCommit}, {"commit-msg", CommitMsg}} {
		for _, fail := range []bool{false, true} {
			t.Run(hook.name+"/failure="+map[bool]string{false: "false", true: "true"}[fail], func(t *testing.T) {
				root, bin, home := t.TempDir(), t.TempDir(), t.TempDir()
				writeFile(t, filepath.Join(root, ".punt-labs", "ethos", "enabled"), "")
				writeFile(t, filepath.Join(root, "MSG"), "subject\n")
				calls := filepath.Join(t.TempDir(), "probes")
				git := "#!/bin/sh\nprintf 'probe\\n' >> '" + calls + "'\nprintf 'fixture warning\\n' >&2\nprintf '%s\\n' '" + root + "'\n"
				if fail {
					git += "exit 128\n"
				}
				require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o700))
				writeFile(t, filepath.Join(bin, "mktemp"), "#!/bin/sh\necho forbidden-mktemp >&2\nexit 99\n")
				require.NoError(t, os.Chmod(filepath.Join(bin, "mktemp"), 0o700))
				writeFile(t, filepath.Join(bin, "ethos"), "#!/bin/sh\nexit 0\n")
				require.NoError(t, os.Chmod(filepath.Join(bin, "ethos"), 0o700))
				script := filepath.Join(t.TempDir(), hook.name)
				require.NoError(t, os.WriteFile(script, hook.body, 0o700))
				cmd := exec.Command(script, filepath.Join(root, "MSG"))
				cmd.Dir = root
				cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + filepath.Join(root, ".punt-labs", "ethos"), "MISSION_ID=m-2026-11-04-043"}
				out, err := cmd.CombinedOutput()
				if fail {
					require.Error(t, err)
					assert.Contains(t, string(out), hook.name+": git rev-parse --show-toplevel failed")
				} else {
					require.NoError(t, err, string(out))
				}
				assert.Contains(t, string(out), "fixture warning", "successful Git stderr remains visible too")
				assert.NotContains(t, string(out), "forbidden-mktemp")
				data, err := os.ReadFile(calls)
				require.NoError(t, err)
				assert.Equal(t, "probe\n", string(data), "exactly one stdout-only root probe")
			})
		}
	}
}

//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	hooks "github.com/punt-labs/ethos/v4/plugin/hooks"
	"github.com/stretchr/testify/require"
)

func TestAuditBlameCommitBlocksCapturedPromptRetryFailure(t *testing.T) {
	for _, archiveFailure := range []bool{false, true} {
		name := "prompt conflict"
		if archiveFailure {
			name = "prompt conflict joined with archive failure"
		}
		t.Run(name, func(t *testing.T) {
			p, stream := repositoryAuditCLI(t, "blame")
			root := filepath.Dir(filepath.Dir(p.RecordRoot()))
			full, err := layout.NewRepository(root, root, layout.RecordFull)
			require.NoError(t, err)
			const mid, did = "m-2026-11-05-904", "d-2026-11-05-904"
			body := []byte("captured full prompt")
			_, err = mission.WriteDelegationSkeletonWithPolicy(full, mid, did, mission.DelegationSkeleton{Tier: mission.TierB, Prompt: body})
			require.NoError(t, err)
			published := filepath.Join(p.CheckoutRecordRoot(), "missions", mid, "delegations", did, "prompt.md")
			require.NoError(t, os.WriteFile(published, []byte("divergent publication"), 0o600))
			if archiveFailure {
				dir, err := audit.ArchiveDir(p, stream, "2026-11-05")
				require.NoError(t, err)
				require.NoError(t, audit.WriteLocalAtomic(p, filepath.Join(dir, audit.SessionChunkFile(100, 100)), []byte("corrupt\n")))
			}
			cmd := exec.Command(ethosBinary, "audit", "seal", "--commit-context")
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "%s", out)
			require.Equal(t, 2, exit.ExitCode(), "%s", out)
			require.Contains(t, string(out), "captured prompt publication retry incomplete")
			require.NotContains(t, string(out), "allowing the source commit")

			// Exercise the installed pre-commit script through a real source commit.
			require.NoError(t, os.WriteFile(filepath.Join(p.RecordRoot(), "enabled"), nil, 0o600))
			bin, hookDir := t.TempDir(), t.TempDir()
			require.NoError(t, os.Symlink(ethosBinary, filepath.Join(bin, "ethos")))
			require.NoError(t, os.WriteFile(filepath.Join(hookDir, "pre-commit"), hooks.PreCommit, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(root, "source.go"), []byte("package fixture\n"), 0o600))
			git := func(args ...string) ([]byte, error) {
				command := exec.Command("git", args...)
				command.Dir = root
				command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
				return command.CombinedOutput()
			}
			out, err = git("add", "--", "source.go")
			require.NoError(t, err, "%s", out)
			before, err := git("diff", "--cached", "--raw", "--no-abbrev")
			require.NoError(t, err)
			out, err = git("-c", "core.hooksPath="+hookDir, "-c", "commit.gpgsign=false", "-c", "user.name=fixture", "commit", "-m", "fixture source")
			require.Error(t, err, "prompt conflict must block the commit: %s", out)
			require.Contains(t, string(out), "captured prompt publication retry incomplete")
			require.NotContains(t, string(out), "allowing the source commit")
			after, err := git("diff", "--cached", "--raw", "--no-abbrev")
			require.NoError(t, err)
			require.Equal(t, before, after)
			retained, err := os.ReadFile(filepath.Join(p.LocalRoot(), "trace", "missions", mid, "delegations", did, "prompt.md"))
			require.NoError(t, err)
			require.Equal(t, body, retained)

			// An equal copy clears only the prompt obligation; an independent
			// retained-data archival warning remains nonblocking in blame mode.
			require.NoError(t, os.WriteFile(published, body, 0o600))
			cmd = exec.Command(ethosBinary, "audit", "seal", "--commit-context")
			cmd.Dir = root
			out, err = cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		})
	}
}

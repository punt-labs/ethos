package hook

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandoffWorktreePreservesPermissionsAndKeysChmod(t *testing.T) {
	root, run := handoffMetadataFixture(t, `set -eu
mode="$(stat -c %a Formula)"
if [ "$mode" != 644 ]; then echo "formula mode: $mode"; exit 42; fi
if (printf 'write\n' > Formula) || chmod 600 Formula; then exit 43; fi
`)
	formula := filepath.Join(root, "Formula")
	writeFile(t, formula, "formula\n")
	require.NoError(t, os.Chmod(formula, 0o644))
	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)
	require.NoError(t, os.Chmod(formula, 0o600))
	feedback = run()
	require.Contains(t, feedback.Blocking, "exit status 42")
	require.Contains(t, feedback.Blocking, "formula mode: 600")
}

func TestHandoffWorktreeSyntheticAccount(t *testing.T) {
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	require.NoError(t, err)
	group, err := user.LookupGroupId(strconv.Itoa(os.Getegid()))
	require.NoError(t, err)
	_, run := handoffMetadataFixture(t, fmt.Sprintf(`set -eu
test "$(id -un)" = %q
test "$(id -gn)" = %q
test "$(getent passwd %d | cut -d: -f6)" = /scratch/home
test "$(wc -l < /etc/passwd)" -eq 1
test "$(wc -l < /etc/group)" -eq 1
if (echo write > /etc/passwd) || (echo write > /etc/group); then exit 43; fi
`, account.Username, group.Name, os.Geteuid()))
	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)
}

func TestHandoffWorktreeMountsOSRelease(t *testing.T) {
	release, err := os.ReadFile("/etc/os-release")
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("no /etc/os-release on host")
	}
	require.NoError(t, err)
	_, run := handoffMetadataFixture(t, fmt.Sprintf(`set -eu
printf '%x  /etc/os-release\n' | sha256sum --check
if (echo write > /etc/os-release); then exit 43; fi
`, sha256.Sum256(release)))
	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)
}

func handoffMetadataFixture(t *testing.T, script string) (string, func() StageFeedback) {
	t.Helper()
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: metadata\n    stage: handoff\n    files: all\n    run: [sh, gate.sh]\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), `checks:
  metadata:
    profile: linux-worktree-v1
    epoch: metadata-v1
    sources: {mode: worktree}
`)
	writeFile(t, filepath.Join(root, "gate.sh"), script)
	return root, func() StageFeedback { return RunStageChecks(context.Background(), root, "worker", "handoff") }
}

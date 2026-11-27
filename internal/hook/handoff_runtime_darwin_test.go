package hook

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDarwinHandoffPinnedRuntime(t *testing.T) {
	root, _ := postEditFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(root, ".gitignore"), ".punt-labs/local/\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", `  - name: pinned
    stage: handoff
    files: all
    run: [/bin/sh, gate.sh]
`)
	writeFile(t, filepath.Join(root, "gate.sh"), `set -eu
test "$(runtime-proof)" = pinned
test "$PROOF_RUNTIME" = "$ETHOS_HANDOFF_RUNTIME"
if echo write > "$ETHOS_HANDOFF_RUNTIME/bin/runtime-proof"; then exit 42; fi
`)
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	script := "#!/bin/sh\necho pinned\n"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "bin/runtime-proof", Mode: 0o755, Size: int64(len(script))}))
	_, err := tw.Write([]byte(script))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	digest := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), fmt.Sprintf(`checks:
  pinned:
    profile: linux-worktree-v1
    epoch: pinned
    sources: {mode: worktree}
    runtime:
      platforms:
        darwin-%s:
          bundle: pinned
          sha256: "%s"
          url: https://example.com/runtime.tar.gz
          path: ["{runtime}/bin"]
          env: {PROOF_RUNTIME: "{runtime}"}
`, runtime.GOARCH, digest))
	run := func() StageFeedback { return RunStageChecks(t.Context(), root, "worker", "handoff") }
	require.Contains(t, run().Notice, "runtime-missing")
	asset := filepath.Join(home, ".punt-labs/ethos/runtimes", digest+".tar.gz")
	writeFile(t, asset, "mismatched")
	require.Contains(t, run().Notice, "runtime-digest-mismatch")
	require.NoError(t, os.WriteFile(asset, archive.Bytes(), 0o600))
	require.Equal(t, StageFeedback{}, run())
}

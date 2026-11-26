package hook

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandoffWorktreePrehydrate(t *testing.T) {
	root, run := handoffMetadataFixture(t, `set -eu
test "$(cat /scratch/hydrated)" = ready
test ! -e "/proc/$(cat /scratch/child)"
`)
	writeFile(t, filepath.Join(root, "hydrate.sh"), `set -eu
test "$PWD" = /checkout
test "$(cat /runtime/value)" = runtime
test "$(cat /scratch/input)" = checkout
test "$#" = 1
test "$1" != failure || exit 19
sleep 60 &
printf '%s' "$!" > /scratch/child
printf '%s' "$1" > /scratch/hydrated
`)
	writeFile(t, filepath.Join(root, "input"), "checkout\n")
	writeProfile := func(arg string) {
		t.Helper()
		writeHandoffScratchProfile(t, root, "    scratch: {seed: {'{scratch}/input': '{root}/input'}}\n"+
			"    prehydrate: {run: [sh, '{root}/hydrate.sh', "+arg+"]}\n", map[string]string{"value": "runtime\n"})
	}
	writeProfile("ready")
	pass := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(pass.Blocking))
	require.Equal(t, StageFeedback{}, pass)

	writeProfile("failure")
	for range 2 {
		failure := run()
		require.Contains(t, failure.Blocking, "prehydrate-failed")
		require.Contains(t, failure.Notice, "Run the explicit project gate")
		require.NotContains(t, failure.Blocking, "metadata: exit status")
	}
	require.NoFileExists(t, filepath.Join(root, "hydrated"))
}

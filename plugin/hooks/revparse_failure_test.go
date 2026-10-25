//go:build linux || darwin

package hooks

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A git that fails `rev-parse --show-toplevel` (dubious ownership, a corrupt
// .git) must not make the hooks skip silently with the host's status. Each
// prints git's own error to stderr and exits 1, so the commit is blocked and
// the operator sees why.
func TestHooksFailLoudWhenRevParseFails(t *testing.T) {
	for _, hook := range []string{"pre-commit.sh", "commit-msg.sh"} {
		t.Run(hook, func(t *testing.T) {
			bin := t.TempDir()
			fake := "#!/bin/sh\necho 'fatal: detected dubious ownership in repository at /x' >&2\nexit 128\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755))
			msg := filepath.Join(t.TempDir(), "MSG")
			require.NoError(t, os.WriteFile(msg, []byte("subject\n"), 0o644))

			cmd := exec.Command("/bin/sh", hook, msg)
			cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + bin + ":/usr/bin:/bin"}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, "the hook must fail, not exit with the host status 0")
			assert.Equal(t, 1, ee.ExitCode())
			assert.Contains(t, stderr.String(), "dubious ownership", "git's own error is shown")
			assert.Contains(t, stderr.String(), hook[:len(hook)-3], "the hook names itself")
		})
	}
}

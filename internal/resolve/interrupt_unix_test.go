//go:build unix

package resolve

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelperLocate is the child of TestInterruptedProbeEndsTheProcess, not a
// test: it locates a repository, then writes a file. The file exists only if
// locate returned to its caller.
func TestHelperLocate(t *testing.T) {
	after := os.Getenv("ETHOS_HELPER_AFTER")
	if after == "" {
		t.Skip("helper process")
	}
	locate(filepath.Dir(after))
	_ = os.WriteFile(after, []byte("continued"), 0o644)
}

// A signal that arrives while git is probed ends the process as that signal
// demands. It is never turned into "git does not run", which would let the
// command go on (enable would write its marker) after the user pressed Ctrl-C.
func TestInterruptedProbeEndsTheProcess(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := t.TempDir()
			started := filepath.Join(dir, "started")
			after := filepath.Join(dir, "after")
			gitDir := fakeGit(t, "case \"$1\" in --version) echo $$ > '"+started+"'; exec sleep 60;; esac\nexit 1\n")
			c := exec.Command(os.Args[0], "-test.run=^TestHelperLocate$")
			c.Env = append(os.Environ(), "ETHOS_HELPER_AFTER="+after, "PATH="+gitDir+":/usr/bin:/bin")
			require.NoError(t, c.Start())
			done := make(chan error, 1)
			go func() { done <- c.Wait() }()
			require.Eventually(t, func() bool {
				data, err := os.ReadFile(started)
				return err == nil && strings.TrimSpace(string(data)) != ""
			}, 5*time.Second, 20*time.Millisecond, "the probe started")
			require.NoError(t, c.Process.Signal(sig))
			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				_ = c.Process.Kill()
				t.Fatal("the helper did not exit on the signal")
			}
			var ee *exec.ExitError
			require.True(t, errors.As(err, &ee), "the helper exited cleanly: %v", err)
			ws, ok := ee.Sys().(syscall.WaitStatus)
			require.True(t, ok)
			assert.True(t, ws.Signaled(), "the helper exited with status %d, not from %v", ws.ExitStatus(), sig)
			assert.Equal(t, sig, ws.Signal())
			_, serr := os.Stat(after)
			assert.True(t, os.IsNotExist(serr), "locate returned to its caller after %v", sig)
		})
	}
}

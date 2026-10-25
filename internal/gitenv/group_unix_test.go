//go:build unix

package gitenv

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// On a timeout the whole process group dies: a clean or smudge filter that git
// started, and that ignores TERM, does not outlive git.
func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "filter.pid")
	git := fakeGitScript(t, "trap '' TERM\nsh -c \"trap '' TERM; echo \\$\\$ > '"+pidFile+"'; exec sleep 60\" &\nwhile [ ! -s '"+pidFile+"' ]; do sleep 0.05; done\nwait\n")
	testhelpersTimeout(t, 500*time.Millisecond)
	_, err := OutputEnv(dir, git, nil, "add")
	require.ErrorIs(t, err, ErrTimeout)
	data, rerr := os.ReadFile(pidFile)
	require.NoError(t, rerr)
	pid, aerr := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, aerr)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 3*time.Second, 50*time.Millisecond,
		"the filter (pid %d) outlived git", pid)
}

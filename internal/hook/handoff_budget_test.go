//go:build linux || darwin

package hook

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/stretchr/testify/require"
)

func TestHandoffRenameUntrackedBudget(t *testing.T) {
	for _, mode := range []string{"count", "output", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			if mode == "count" {
				for i := range 513 {
					writeFile(t, filepath.Join(root, fmt.Sprintf("generated/%04d.txt", i)), "unrelated\n")
				}
			} else {
				realGit, err := exec.LookPath("git")
				require.NoError(t, err)
				realGit, err = filepath.EvalSymlinks(realGit)
				require.NoError(t, err)
				require.NoError(t, os.Remove(filepath.Join(bin, "git")))
				behavior := "printf '%065537d' 0; exit 0"
				if mode == "timeout" {
					behavior = "/bin/sleep 2"
				}
				postEditTool(t, bin, "git", "for arg do if [ \"$arg\" = --others ]; then "+behavior+"; fi; done\nexec "+fmt.Sprintf("%q", realGit)+" \"$@\"\n")
			}
			renamed, err := handoffRenames(root)
			require.Error(t, err)
			require.Empty(t, renamed)
			if mode == "timeout" {
				require.ErrorIs(t, err, gitenv.ErrTimeout)
			} else {
				require.Contains(t, err.Error(), "budget")
			}
			entries, readErr := os.ReadDir(filepath.Join(root, ".punt-labs/local/ethos/tmp"))
			require.NoError(t, readErr)
			require.Empty(t, entries, "candidate indexes must be cleaned up on every budget failure")
		})
	}
}

func TestHandoffUntrackedBuffer(t *testing.T) {
	for _, size := range []int{0, 3, handoffUntrackedBytes, handoffUntrackedBytes + 1, 2 * handoffUntrackedBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var buffer handoffUntrackedBuffer
			input := strings.Repeat("x", size)
			n, err := io.Copy(&buffer, io.LimitReader(strings.NewReader(input), int64(size)))
			require.NoError(t, err)
			require.Equal(t, int64(size), n)
			require.Equal(t, input[:min(size, handoffUntrackedBytes)], buffer.data.String())
			require.Equal(t, size > handoffUntrackedBytes, buffer.exceeded)
		})
	}
}

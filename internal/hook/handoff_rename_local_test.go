//go:build linux || darwin

package hook

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandoffRenameIndexUsesCanonicalLocalTemp(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("linked=%t/failure=%t", linked, fail), func(t *testing.T) {
				store, bin := postEditFixture(t)
				t.Setenv("HOME", t.TempDir())
				log := filepath.Join(t.TempDir(), "index-path")
				ambient := filepath.Join(t.TempDir(), "missing")
				writeFile(t, filepath.Join(store, "a.go"), "package example\n"+strings.Repeat("// source\n", 40))
				stageGit(t, store, "add", "a.go")
				stageGit(t, store, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "source")
				root := store
				if linked {
					root = filepath.Join(store, ".claude/worktrees/linked")
					stageGit(t, store, "worktree", "add", "--detach", root)
				}
				require.NoError(t, os.Rename(filepath.Join(root, "a.go"), filepath.Join(root, "b.go")))
				realGit, err := exec.LookPath("git")
				require.NoError(t, err)
				realGit, err = filepath.EvalSymlinks(realGit)
				require.NoError(t, err)
				require.NoError(t, os.Remove(filepath.Join(bin, "git")))
				failure := ""
				if fail {
					failure = "exit 86\n"
				}
				postEditTool(t, bin, "git", "if [ \"$1\" = diff ]; then printf '%s' \"$GIT_INDEX_FILE\" > \"$LOCAL_INDEX_LOG\"\n"+failure+"fi\nexec "+fmt.Sprintf("%q", realGit)+" \"$@\"\n")
				t.Setenv("LOCAL_INDEX_LOG", log)
				t.Setenv("TMPDIR", ambient)
				renamed, err := handoffRenames(root)
				if fail {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.Equal(t, "b.go", renamed["a.go"])
				}
				data, err := os.ReadFile(log)
				require.NoError(t, err)
				index := string(data)
				local := filepath.Join(store, ".punt-labs/local/ethos/tmp")
				rel, err := filepath.Rel(local, index)
				require.NoError(t, err)
				require.True(t, filepath.IsLocal(rel), "index %s must be below canonical local temp %s", index, local)
				require.NoFileExists(t, index)
				require.NoDirExists(t, filepath.Dir(index), "private candidate directory must be removed on success and failure")
				require.NoDirExists(t, ambient)
			})
		}
	}
}

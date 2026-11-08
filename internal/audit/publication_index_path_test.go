//go:build linux || darwin

package audit

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

func TestPublicationIndexPathBytes(t *testing.T) {
	for _, name := range []string{"index ", "index\t", " index", "\tindex", "index\r", "index\n", "index\n\n", "in\ndex", `"index"`, `index\literal`} {
		for _, absolute := range []bool{false, true} {
			for _, state := range []string{"empty-absent-sibling", "empty-valid-sibling", "valid"} {
				t.Run(strconv.Quote(name)+"/"+map[bool]string{false: "relative", true: "absolute"}[absolute]+"/"+state, func(t *testing.T) {
					p, publication, ordinary := publicationFixture(t)
					root := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
					runGit(t, root, "-c", "core.hooksPath=", "-c", "commit.gpgsign=false", "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "baseline source")
					siblingBytes, err := os.ReadFile(ordinary)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("staged unrelated\n"), 0o600))
					runGit(t, root, "add", "--", "unrelated.txt")
					valid, err := os.ReadFile(ordinary)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("unstaged unrelated\n"), 0o600))
					index := filepath.Join(root, name)
					before := []byte{}
					if state == "valid" {
						before = valid
					}
					require.NoError(t, os.WriteFile(index, before, 0o640))
					original, err := os.Stat(index)
					require.NoError(t, err)
					sibling := strings.TrimSpace(index)
					if !absolute {
						sibling = filepath.Join(root, strings.TrimSpace(name))
					}
					hasSibling := sibling != index && state != "empty-absent-sibling"
					if hasSibling {
						require.NoError(t, os.WriteFile(sibling, siblingBytes, 0o640))
					}
					selected := name
					if absolute {
						selected = index
					}
					t.Setenv("GIT_INDEX_FILE", selected)
					env, err := gitenv.ActualIndexEnv()
					require.NoError(t, err)
					out, err := gitenv.Run(gitenv.Spec{Git: "git", Dir: root, Env: env, Args: []string{"rev-parse", "--git-path", "index"}})
					require.NoError(t, err)
					require.Equal(t, selected+"\n", string(out), "Git reports a raw path plus one LF, not a quoted/trimmed path")
					t.Logf("selected=%q Git output=%q", selected, out)
					err = StagePublication(p, []string{publication})
					if state != "valid" {
						assert.ErrorContains(t, err, "empty/corrupt")
						if err != nil {
							assert.Contains(t, err.Error(), index)
						}
						after, err := os.ReadFile(index)
						require.NoError(t, err)
						assert.Equal(t, before, after)
						info, err := os.Stat(index)
						require.NoError(t, err)
						assert.Equal(t, original.Mode(), info.Mode())
						assert.True(t, os.SameFile(original, info))
					} else {
						require.NoError(t, err)
						for file, want := range map[string]string{publication: "", filepath.Join(root, "unrelated.txt"): "staged unrelated\n"} {
							data, present, err := IndexBlob(root, file)
							require.NoError(t, err)
							assert.True(t, present, "stage the exact selected index: %s", file)
							if want != "" {
								assert.Equal(t, want, string(data))
							}
						}
					}
					if sibling != index {
						if hasSibling {
							data, err := os.ReadFile(sibling)
							require.NoError(t, err)
							assert.Equal(t, siblingBytes, data, "do not mutate the TrimSpace sibling")
						} else {
							_, err := os.Lstat(sibling)
							assert.ErrorIs(t, err, os.ErrNotExist, "do not create the TrimSpace sibling")
						}
						_, err := os.Lstat(sibling + ".lock")
						assert.ErrorIs(t, err, os.ErrNotExist)
					}
					data, err := os.ReadFile(ordinary)
					require.NoError(t, err)
					assert.Equal(t, valid, data, "ordinary index untouched")
					assertPublicationIndexClean(t, p, index, publication)
				})
			}
		}
	}
}

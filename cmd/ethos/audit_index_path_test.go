//go:build linux || darwin

package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

func TestAuditIndexPathActualCLI(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "actual CLI must build")
	binary, err := os.ReadFile(ethosBinary)
	require.NoError(t, err)
	t.Logf("actual CLI sha256=%x", sha256.Sum256(binary))
	for _, name := range []string{"index ", "index\t", " index", "index\n"} {
		for _, absolute := range []bool{false, true} {
			for _, state := range []string{"empty-absent-sibling", "empty-valid-sibling", "valid"} {
				t.Run(strconv.Quote(name)+"/"+map[bool]string{false: "relative", true: "absolute"}[absolute]+"/"+state, func(t *testing.T) {
					p, stream := repositoryAuditCLI(t, "full")
					root := filepath.Dir(filepath.Dir(p.RecordRoot()))
					require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("committed source\n"), 0o600))
					runGit(t, root, "add", "--", "source.txt")
					runGit(t, root, "-c", "core.hooksPath=", "commit", "-qm", "baseline source")
					ordinary := filepath.Join(root, ".git", "index")
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
					// Audit asks Git from the checkout root even when invoked below it.
					// Model the pre-fix sibling exactly for each Git output spelling.
					sibling := strings.TrimSpace(index)
					selected := index
					if !absolute {
						selected = name
						sibling = filepath.Join(root, strings.TrimSpace(name))
					}
					hasSibling := sibling != index && state != "empty-absent-sibling"
					if hasSibling {
						require.NoError(t, os.WriteFile(sibling, siblingBytes, 0o640))
					}
					t.Setenv("GIT_INDEX_FILE", selected)
					env := append(os.Environ(), "ETHOS_REPO_ROOT=")
					cmd := exec.Command(ethosBinary, "hook", "audit-log")
					cmd.Dir, cmd.Env = root, env
					cmd.Stdin = bytes.NewBufferString(`{"session_id":"` + stream.Session + `","tool_name":"Read","delegation_id":"index-path-d","tool_input":{"file_path":"source.txt"}}`)
					out, err := cmd.CombinedOutput()
					require.NoError(t, err, "%s", out)
					nested := filepath.Join(root, "nested")
					require.NoError(t, os.Mkdir(nested, 0o700))
					cmd = exec.Command(ethosBinary, "audit", "seal", "--verbose")
					cmd.Dir, cmd.Env = nested, env
					out, err = cmd.CombinedOutput()
					t.Logf("selected=%q error=%v output=%q", selected, err, out)
					if state == "valid" {
						require.NoError(t, err, "%s", out)
						assert.Contains(t, string(out), "staged 1 chunk(s)")
						chunks, err := filepath.Glob(filepath.Join(p.RecordRoot(), "sessions", "*", "audit-*.jsonl"))
						require.NoError(t, err)
						require.Len(t, chunks, 1)
						_, present, err := audit.IndexBlob(root, chunks[0])
						require.NoError(t, err)
						assert.True(t, present, "stage the exact selected index")
						data, present, err := audit.IndexBlob(root, filepath.Join(root, "unrelated.txt"))
						require.NoError(t, err)
						assert.True(t, present)
						assert.Equal(t, "staged unrelated\n", string(data))
					} else {
						var exit *exec.ExitError
						if assert.ErrorAs(t, err, &exit, "%s", out) {
							assert.Equal(t, 2, exit.ExitCode())
						}
						assert.Contains(t, string(out), "empty/corrupt")
						assert.Contains(t, string(out), index)
						assert.Contains(t, string(out), "local archive retained")
						assert.NotContains(t, string(out), "staged 1 chunk(s)")
						after, err := os.ReadFile(index)
						require.NoError(t, err)
						assert.Equal(t, before, after)
						info, err := os.Stat(index)
						require.NoError(t, err)
						assert.Equal(t, original.Mode(), info.Mode())
						assert.True(t, os.SameFile(original, info))
					}
					if sibling != index {
						if hasSibling {
							data, err := os.ReadFile(sibling)
							require.NoError(t, err)
							assert.Equal(t, siblingBytes, data)
						} else {
							_, err := os.Lstat(sibling)
							assert.ErrorIs(t, err, os.ErrNotExist)
						}
						assert.NoFileExists(t, sibling+".lock")
					}
					data, err := os.ReadFile(ordinary)
					require.NoError(t, err)
					assert.Equal(t, valid, data)
					assert.NoFileExists(t, index+".lock")
					entries, err := os.ReadDir(p.TempDir())
					require.NoError(t, err)
					assert.Empty(t, entries)
				})
			}
		}
	}
}

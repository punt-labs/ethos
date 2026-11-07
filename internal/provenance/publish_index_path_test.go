//go:build linux || darwin

package provenance

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

func TestPublicationActualIndexPathBytes(t *testing.T) {
	for _, name := range []string{"index ", "index\t", " index", "\tindex", "index\r", "index\n", "index\n\n", "in\ndex", `"index"`, `index\literal`} {
		for _, absolute := range []bool{false, true} {
			for _, state := range []string{"empty-absent-sibling", "empty-valid-sibling", "valid"} {
				t.Run(strconv.Quote(name)+"/"+map[bool]string{false: "relative", true: "absolute"}[absolute]+"/"+state, func(t *testing.T) {
					p, base := repositoryTest(t)
					r := p.Repository
					ordinary, siblingBytes := indexBytesTest(t, r)
					writeTest(t, r.Root, "unrelated.txt", []byte("staged unrelated\n"))
					gitTest(t, r, "add", "unrelated.txt")
					valid, err := os.ReadFile(ordinary)
					require.NoError(t, err)
					writeTest(t, r.Root, "unrelated.txt", []byte("unstaged unrelated\n"))
					index := filepath.Join(r.Root, name)
					before := valid
					if state != "valid" {
						before = []byte{}
					}
					require.NoError(t, os.WriteFile(index, before, 0o640))
					original, err := os.Stat(index)
					require.NoError(t, err)
					// Provenance requests absolute output, so only trailing whitespace
					// is vulnerable here; leading basename whitespace stays embedded.
					sibling := strings.TrimSpace(index)
					hasSibling := sibling != index && state != "empty-absent-sibling"
					if hasSibling {
						require.NoError(t, os.WriteFile(sibling, siblingBytes, 0o640))
					}
					selected := name
					if absolute {
						selected = index
					}
					t.Setenv("GIT_INDEX_FILE", selected)
					r.Env, err = gitenv.ActualIndexEnv()
					require.NoError(t, err)
					p.Repository = r
					out, err := r.git(t.Context(), "rev-parse", "--path-format=absolute", "--git-path", "index")
					require.NoError(t, err)
					require.Equal(t, index+"\n", string(out), "Git reports raw pathname bytes plus one LF")
					t.Logf("selected=%q Git output=%q", selected, out)
					receipt, err := p.Publish(t.Context(), selectedTest(t), base)
					assert.Equal(t, index, receipt.Index, "receipt names the exact actual index on success and failure")
					if state != "valid" {
						assert.ErrorContains(t, err, "index file smaller than expected", "existing empty index remains an error, not initialization")
						assert.False(t, receipt.IndexInstalled)
						assert.Empty(t, receipt.Copied)
						after, err := os.ReadFile(index)
						require.NoError(t, err)
						assert.Equal(t, before, after)
						info, err := os.Stat(index)
						require.NoError(t, err)
						assert.Equal(t, original.Mode(), info.Mode())
						assert.True(t, os.SameFile(original, info))
					} else {
						require.NoError(t, err)
						assert.True(t, receipt.IndexInstalled)
						require.NoError(t, r.VerifyIndex(t.Context(), []string{receipt.Manifest}))
						data, err := r.read(t.Context(), ":", "unrelated.txt")
						require.NoError(t, err)
						assert.Equal(t, "staged unrelated\n", string(data))
					}
					if sibling != index {
						if hasSibling {
							data, err := os.ReadFile(sibling)
							require.NoError(t, err)
							assert.Equal(t, siblingBytes, data, "trimmed sibling unchanged")
						} else {
							_, err := os.Lstat(sibling)
							assert.ErrorIs(t, err, os.ErrNotExist, "do not create the trimmed sibling")
						}
						assert.NoFileExists(t, sibling+".lock")
					}
					data, err := os.ReadFile(ordinary)
					require.NoError(t, err)
					assert.Equal(t, valid, data, "ordinary index untouched")
					assert.NoFileExists(t, index+".lock")
					entries, err := os.ReadDir(p.Policy.TempDir())
					require.NoError(t, err)
					assert.Empty(t, entries)
				})
			}
		}
	}
}

func TestPublicationSharedIndexPathBytes(t *testing.T) {
	for _, admin := range []string{" admin", "\tadmin", "ad\nmin", `"admin`, `admin\literal`} {
		t.Run(strconv.Quote(admin), func(t *testing.T) {
			p, base := repositoryTest(t)
			r := p.Repository
			// Git accepts this relative gitfile spelling and emits the shared
			// index path with its leading/embedded bytes intact, without quotes.
			require.NoError(t, os.Rename(filepath.Join(r.Root, ".git"), filepath.Join(r.Root, admin)))
			require.NoError(t, os.WriteFile(filepath.Join(r.Root, ".git"), []byte("gitdir: "+admin+"\n"), 0o600))
			gitTest(t, r, "update-index", "--split-index")
			out, err := r.git(t.Context(), "rev-parse", "--shared-index-path")
			require.NoError(t, err)
			dirEntries, err := os.ReadDir(filepath.Join(r.Root, admin))
			require.NoError(t, err)
			var entries []string
			for _, entry := range dirEntries {
				if strings.HasPrefix(entry.Name(), "sharedindex.") {
					entries = append(entries, filepath.Join(r.Root, admin, entry.Name()))
				}
			}
			require.Len(t, entries, 1)
			rel := filepath.Join(admin, filepath.Base(entries[0]))
			require.Equal(t, rel+"\n", string(out))
			t.Logf("shared-index output=%q", out)
			shared, err := os.ReadFile(entries[0])
			require.NoError(t, err)
			receipt, err := p.Publish(t.Context(), selectedTest(t), base)
			require.NoError(t, err)
			assert.True(t, receipt.IndexInstalled)
			assert.Equal(t, filepath.Join(r.Root, admin, "index"), receipt.Index)
			require.NoError(t, r.VerifyIndex(t.Context(), []string{receipt.Manifest}))
			after, err := os.ReadFile(entries[0])
			require.NoError(t, err)
			assert.Equal(t, shared, after, "original shared index remains unchanged")
			dirEntries, err = os.ReadDir(p.Policy.TempDir())
			require.NoError(t, err)
			assert.Empty(t, dirEntries)
		})
	}
}

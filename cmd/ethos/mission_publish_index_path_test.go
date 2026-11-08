//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/provenance"
)

func TestMissionPublishIndexPathActualCLI(t *testing.T) {
	for _, name := range []string{"index ", "index\t", " index", "index\n"} {
		for _, absolute := range []bool{false, true} {
			for _, state := range []string{"empty-absent-sibling", "empty-valid-sibling", "valid"} {
				t.Run(strconv.Quote(name)+"/"+map[bool]string{false: "relative", true: "absolute"}[absolute]+"/"+state, func(t *testing.T) {
					se, base, id := publicationFixtureCLI(t)
					checkout := se.repo
					ordinary := publicationGitCLI(t, se, "rev-parse", "--path-format=absolute", "--git-path", "index")
					siblingBytes, err := os.ReadFile(ordinary)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(checkout, "unrelated.txt"), []byte("staged unrelated\n"), 0o600))
					publicationGitCLI(t, se, "add", "unrelated.txt")
					valid, err := os.ReadFile(ordinary)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(checkout, "unrelated.txt"), []byte("unstaged unrelated\n"), 0o600))
					index := filepath.Join(checkout, name)
					before := valid
					if state != "valid" {
						before = []byte{}
					}
					require.NoError(t, os.WriteFile(index, before, 0o640))
					original, err := os.Stat(index)
					require.NoError(t, err)
					sibling := strings.TrimSpace(index)
					hasSibling := sibling != index && state != "empty-absent-sibling"
					if hasSibling {
						require.NoError(t, os.WriteFile(sibling, siblingBytes, 0o640))
					}
					selected := name
					if absolute {
						selected = index
					}
					se.env = append(se.env, "GIT_INDEX_FILE="+selected)
					se.repo = filepath.Join(checkout, "nested")
					require.NoError(t, os.Mkdir(se.repo, 0o700))
					stdout, stderr, exit := runCLI(t, se, "mission", "publish", id, "--base", base, "--json")
					t.Logf("selected=%q child exit=%d stdout=%s stderr=%s", selected, exit, stdout, stderr)
					var output struct {
						Receipt provenance.Receipt `json:"receipt"`
						Error   string             `json:"error"`
					}
					require.NoError(t, json.Unmarshal([]byte(stdout), &output))
					assert.Equal(t, index, output.Receipt.Index, "JSON receipt preserves exact pathname bytes")
					if state != "valid" {
						assert.Equal(t, 1, exit)
						assert.Contains(t, output.Error, "index file smaller than expected")
						assert.False(t, output.Receipt.IndexInstalled)
						assert.Empty(t, output.Receipt.Copied)
						after, err := os.ReadFile(index)
						require.NoError(t, err)
						assert.Equal(t, before, after)
						info, err := os.Stat(index)
						require.NoError(t, err)
						assert.Equal(t, original.Mode(), info.Mode())
						assert.True(t, os.SameFile(original, info))
					} else {
						require.Zero(t, exit, "%s", stderr)
						assert.True(t, output.Receipt.IndexInstalled)
						assert.Empty(t, output.Error)
						_, stderr, exit = runCLI(t, se, "provenance", "verify-index", output.Receipt.Manifest, "--json")
						require.Zero(t, exit, "%s", stderr)
						se.repo = checkout
						assert.Equal(t, "staged unrelated", publicationGitCLI(t, se, "show", ":unrelated.txt"))
					}
					if sibling != index {
						if hasSibling {
							data, err := os.ReadFile(sibling)
							require.NoError(t, err)
							assert.Equal(t, siblingBytes, data, "trimmed sibling unchanged")
						} else {
							_, err := os.Lstat(sibling)
							assert.ErrorIs(t, err, os.ErrNotExist)
						}
						assert.NoFileExists(t, sibling+".lock")
					}
					data, err := os.ReadFile(ordinary)
					require.NoError(t, err)
					assert.Equal(t, valid, data, "worktree index untouched")
					assert.NoFileExists(t, index+".lock")
				})
			}
		}
	}
}

//go:build linux || darwin

package main

import (
	"bytes"
	"crypto/sha256"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

func TestAuditEmptyIndexActualCLI(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the actual CLI must build; no skip")
	binary, err := os.ReadFile(ethosBinary)
	require.NoError(t, err)
	t.Logf("actual CLI sha256=%x", sha256.Sum256(binary))
	for _, selection := range []string{"default", "alternate-absolute", "alternate-relative"} {
		t.Run(selection, func(t *testing.T) {
			p, stream := repositoryAuditCLI(t, "full")
			root := filepath.Dir(filepath.Dir(p.RecordRoot()))
			require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("committed source\n"), 0o600))
			runGit(t, root, "add", "--", "source.txt")
			runGit(t, root, "-c", "core.hooksPath=", "-c", "commit.gpgsign=false", "commit", "-qm", "source baseline")
			require.NoError(t, os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("staged unrelated\n"), 0o600))
			runGit(t, root, "add", "--", "unrelated.txt")
			require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("unstaged source\n"), 0o600))
			ordinary := filepath.Join(root, ".git", "index")
			valid, err := os.ReadFile(ordinary)
			require.NoError(t, err)
			index := ordinary
			env := append(os.Environ(), "ETHOS_REPO_ROOT=")
			if selection != "default" {
				index = filepath.Join(t.TempDir(), "alternate-index")
				selected := index
				if selection == "alternate-relative" {
					selected, err = filepath.Rel(root, index)
					require.NoError(t, err)
				}
				env = append(env, "GIT_INDEX_FILE="+selected)
			}
			// Use the real writer to capture full consent before damaging the
			// fixture index. Manual sealing must retain that archive on refusal.
			cmd := exec.Command(ethosBinary, "hook", "audit-log")
			cmd.Dir, cmd.Env = root, env
			cmd.Stdin = bytes.NewBufferString(`{"session_id":"` + stream.Session + `","tool_name":"Read","delegation_id":"empty-index-d","tool_input":{"file_path":"source.txt"}}`)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.NoError(t, os.WriteFile(index, nil, 0o640))
			require.NoError(t, os.Chmod(index, 0o640))
			original, err := os.Stat(index)
			require.NoError(t, err)
			// A relative alternate index remains checkout-root-relative even
			// when the user invokes the CLI from a nested working directory.
			nested := filepath.Join(root, "nested")
			require.NoError(t, os.Mkdir(nested, 0o700))
			for attempt := 1; attempt <= 2; attempt++ {
				cmd = exec.Command(ethosBinary, "audit", "seal", "--verbose")
				cmd.Dir, cmd.Env = nested, env
				out, err = cmd.CombinedOutput()
				var exit *exec.ExitError
				if assert.ErrorAs(t, err, &exit, "manual seal must fail: %s", out) {
					assert.Equal(t, 2, exit.ExitCode(), "%s", out)
				}
				t.Logf("attempt %d: error=%v; output=%s", attempt, err, out)
				assert.Contains(t, string(out), "full audit publication incomplete; local archive retained")
				assert.Contains(t, string(out), "is empty or corrupt")
				assert.Contains(t, string(out), index)
				assert.NotContains(t, string(out), "staged 1 chunk(s)", "no staging success after failure")
				assert.NotContains(t, string(out), "allowing the source commit")
				after, err := os.ReadFile(index)
				require.NoError(t, err)
				assert.Empty(t, after, "do not repair or initialize an existing empty index")
				info, err := os.Stat(index)
				require.NoError(t, err)
				assert.Equal(t, original.Mode(), info.Mode())
				assert.True(t, os.SameFile(original, info), "failed manual seal replaced the actual index")
				t.Logf("actual index bytes=%d sha256=%x", len(after), sha256.Sum256(after))
				_, err = os.Lstat(index + ".lock")
				assert.ErrorIs(t, err, os.ErrNotExist)
				entries, err := os.ReadDir(p.TempDir())
				require.NoError(t, err)
				assert.Empty(t, entries, "no candidate/lock debris")
			}
			archives, err := filepath.Glob(filepath.Join(audit.ArchiveRoot(p), "sessions", "*", "audit-*.jsonl"))
			require.NoError(t, err)
			require.Len(t, archives, 1, "publication failure must retain the local archive")
			archive, err := os.ReadFile(archives[0])
			require.NoError(t, err)
			assert.Contains(t, string(archive), `"record":"full"`)
			require.FileExists(t, stream.LivePath(p))
			var records []string
			require.NoError(t, filepath.WalkDir(p.RecordRoot(), func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					records = append(records, path)
				}
				return nil
			}))
			require.Len(t, records, 1, "only the installed publication copy, no record-tree temp")
			published, err := os.ReadFile(records[0])
			require.NoError(t, err)
			assert.Equal(t, archive, published, "checkout installation is not index success")
			if selection != "default" {
				data, err := os.ReadFile(ordinary)
				require.NoError(t, err)
				assert.Equal(t, valid, data, "alternate publication must not touch the default index")
			}
			// Explicit fixture repair restores the saved index. A real retry
			// must stage the pending archive and preserve unrelated staged blobs.
			require.NoError(t, os.WriteFile(index, valid, 0o640))
			cmd = exec.Command(ethosBinary, "audit", "seal", "--verbose")
			cmd.Dir, cmd.Env = nested, env
			out, err = cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			assert.Contains(t, string(out), "staged 1 chunk(s)")
			t.Logf("after explicit fixture index repair: exit 0; output=%s", out)
			rel, err := filepath.Rel(root, records[0])
			require.NoError(t, err)
			cmd = exec.Command("git", "show", ":"+filepath.ToSlash(rel))
			cmd.Dir, cmd.Env = root, env
			out, err = cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			assert.Equal(t, archive, out, "the retry must stage the retained eligible chunk")
			for file, want := range map[string]string{"source.txt": "committed source\n", "unrelated.txt": "staged unrelated\n"} {
				cmd = exec.Command("git", "show", ":"+file)
				cmd.Dir, cmd.Env = root, env
				out, err = cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
				assert.Equal(t, want, string(out))
			}
			_, err = os.Lstat(index + ".lock")
			assert.ErrorIs(t, err, os.ErrNotExist)
			entries, err := os.ReadDir(p.TempDir())
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

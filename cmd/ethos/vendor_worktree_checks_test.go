//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/vendor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVendorWorktreeProjectCheckDestination(t *testing.T) {
	for _, surface := range []string{"cli", "mcp"} {
		for _, scenario := range []string{"conflict apply", "conflict preview", "identical", "absent", "explicit"} {
			t.Run(surface+"/"+scenario, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				t.Setenv("ETHOS_REPO_ROOT", "")
				previous := cmdRoots.Load()
				t.Cleanup(func() { cmdRoots.Store(previous) })
				root := realPath(t, t.TempDir())
				runGit(t, root, "init", "-q")
				write := func(path, body string) {
					require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
					require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
				}
				const rel = ".punt-labs/ethos/checks/portable.yaml"
				const mainPolicy = "name: portable\nchecks: []\ndescription: main policy\n"
				const branchPolicy = "name: portable\nchecks: []\ndescription: branch policy\n"
				write(filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: portable\n")
				write(filepath.Join(root, ".punt-labs/ethos/identities/worker.yaml"), "name: Worker\nhandle: worker\nkind: human\n")
				write(filepath.Join(root, rel), mainPolicy)
				runGit(t, root, "add", ".")
				runGit(t, root, "commit", "-qm", "initial")
				linked := filepath.Join(realPath(t, t.TempDir()), "linked")
				runGit(t, root, "worktree", "add", "--detach", linked)
				want := mainPolicy
				if scenario != "identical" {
					write(filepath.Join(linked, rel), branchPolicy)
				}
				if scenario == "absent" {
					require.NoError(t, os.Remove(filepath.Join(root, rel)))
					want = branchPolicy
				}
				apply := scenario != "conflict preview"
				explicit := scenario == "explicit"
				dest := filepath.Join(root, ".punt-labs/ethos")
				if explicit {
					dest = realPath(t, t.TempDir())
				}
				t.Chdir(linked)
				cmdRoots.Store(locateRoots())
				var err error
				if surface == "cli" {
					args := []string{"worker"}
					if apply {
						args = append(args, "--apply")
					}
					if explicit {
						args = append(args, "--to", dest)
					}
					_, _, err = runVendorCmd(t, args...)
				} else {
					opts := vendor.Options{Handles: []string{"worker"}, Apply: apply}
					if explicit {
						opts.Dest = dest
					}
					_, err = vendorRunner()(opts)
				}
				got, readErr := os.ReadFile(filepath.Join(root, rel))
				require.NoError(t, readErr)
				assert.Equal(t, want, string(got), "canonical project policy must not be replaced")
				if scenario == "conflict apply" || scenario == "conflict preview" {
					require.Error(t, err)
					require.Contains(t, err.Error(), filepath.Join(root, rel))
					require.Contains(t, err.Error(), filepath.Join(linked, rel))
					require.Contains(t, err.Error(), "--to")
				} else {
					require.NoError(t, err)
					if explicit {
						copied, err := os.ReadFile(filepath.Join(dest, "checks/portable.yaml"))
						require.NoError(t, err)
						require.Equal(t, branchPolicy, string(copied))
					}
				}
			})
		}
	}
}

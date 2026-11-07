//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryDiscoveryTypeActualCLI(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the actual CLI must build; no skip")
	for _, layer := range []string{"archive", "shared-record", "checkout-record", "local-live"} {
		for _, position := range []string{"root", "sessions", "missions", "mission-directory", "session-directory"} {
			if layer == "local-live" && position == "session-directory" {
				continue
			}
			for _, kind := range []string{"existing-target", "outside-target", "dangling-link", "regular-file"} {
				t.Run(layer+"/"+position+"/"+kind, func(t *testing.T) {
					_, root, _ := auditMigrateEnv(t)
					checkout := root
					if layer == "shared-record" || layer == "checkout-record" {
						// Only these disposable fixtures receive Git writes. Distinct
						// S/C roots exercise discovery through the real linked resolver.
						runGit(t, root, "-c", "core.hooksPath=", "commit", "--allow-empty", "-m", "discovery fixture")
						checkout = filepath.Join(t.TempDir(), "linked")
						runGit(t, root, "worktree", "add", "--detach", checkout)
					}
					p, err := layout.NewRepository(root, checkout, layout.RecordBlame)
					require.NoError(t, err)
					base := map[string]string{"archive": audit.ArchiveRoot(p), "shared-record": p.RecordRoot(), "checkout-record": p.CheckoutRecordRoot(), "local-live": p.LocalRoot()}[layer]
					path := base
					switch position {
					case "sessions", "missions":
						path = filepath.Join(base, position)
					case "session-directory":
						path = filepath.Join(base, "sessions", "2026-11-04-fixture")
					case "mission-directory":
						path = filepath.Join(base, "missions", "m-2026-11-04-001")
					}
					// Supply a valid independent copy, so a reader cannot merely
					// choose it and hide the redirected source or its conflict.
					mirror := p.RecordRoot()
					if layer == "shared-record" {
						mirror = p.CheckoutRecordRoot()
					}
					rel, name := "sessions/2026-11-04-fixture", "audit.jsonl"
					if position == "missions" || position == "mission-directory" {
						rel, name = "missions/m-2026-11-04-001", "log.jsonl"
					}
					dir := filepath.Join(mirror, rel)
					require.NoError(t, os.MkdirAll(dir, 0o700))
					mirrorPath := filepath.Join(dir, name)
					mirrorBytes := []byte(`{"ts":"2026-11-04T00:00:00Z","tool":"Read","delegation_id":"d"}` + "\n")
					require.NoError(t, os.WriteFile(mirrorPath, mirrorBytes, 0o600))
					require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
					target := filepath.Join(root, "target")
					if kind == "outside-target" {
						target = filepath.Join(t.TempDir(), "target")
					}
					if kind == "regular-file" {
						require.NoError(t, os.WriteFile(path, []byte("source evidence\n"), 0o600))
					} else {
						if kind != "dangling-link" {
							require.NoError(t, os.MkdirAll(target, 0o700))
							require.NoError(t, os.WriteFile(filepath.Join(target, "sentinel"), []byte("target evidence\n"), 0o600))
						}
						require.NoError(t, os.Symlink(target, path))
					}
					for _, args := range [][]string{{"audit", "show", "--delegation", "d", "--format", "json"}, {"audit", "seal"}} {
						cmd := exec.Command(ethosBinary, args...)
						cmd.Dir = checkout
						cmd.Env = append(os.Environ(), "ETHOS_REPO_ROOT=")
						out, err := cmd.CombinedOutput()
						var exit *exec.ExitError
						if assert.ErrorAs(t, err, &exit, "%v: %s", args, out) {
							t.Logf("%v: exit %d; refused %s", args, exit.ExitCode(), path)
							assert.NotZero(t, exit.ExitCode(), "%v: %s", args, out)
							if args[1] == "seal" {
								assert.Equal(t, 2, exit.ExitCode(), "%v: %s", args, out)
							}
						}
						assert.Contains(t, string(out), path, "%v must name the refused source", args)
						assert.NotContains(t, string(out), "nothing pending")
						assert.NotContains(t, string(out), "audit seal: complete")
					}
					b, err := os.ReadFile(mirrorPath)
					require.NoError(t, err)
					assert.Equal(t, mirrorBytes, b)
					if kind == "regular-file" {
						b, err := os.ReadFile(path)
						require.NoError(t, err)
						assert.Equal(t, "source evidence\n", string(b))
					} else {
						link, err := os.Readlink(path)
						require.NoError(t, err)
						assert.Equal(t, target, link)
						if kind == "dangling-link" {
							_, err := os.Lstat(target)
							assert.True(t, os.IsNotExist(err), "reader must not create the target")
						} else {
							b, err := os.ReadFile(filepath.Join(target, "sentinel"))
							require.NoError(t, err)
							assert.Equal(t, "target evidence\n", string(b))
							entries, err := os.ReadDir(target)
							require.NoError(t, err)
							assert.Len(t, entries, 1, "no recovery or quarantine under a redirected source")
						}
					}
				})
			}
		}
	}
}

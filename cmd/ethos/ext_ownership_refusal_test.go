//go:build linux || darwin

package main

import (
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cliExtOwnershipFixture(t *testing.T, se *cliSubprocessEnv) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"),
		[]byte("resolution: repo-only\nactive_bundle: ownership\n"), 0o600))
	bundle := filepath.Join(se.repo, ".punt-labs", "ethos-bundles", "ownership")
	require.NoError(t, os.MkdirAll(bundle, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "bundle.yaml"), []byte("name: ownership\n"), 0o600))
	record := filepath.Join(se.repo, ".punt-labs", "ethos")
	for _, layer := range []struct{ root, value string }{
		{record, "repo-base"}, {bundle, "bundle-base"}, {filepath.Join(se.home, ".punt-labs", "ethos"), "global-base"},
	} {
		ids := filepath.Join(layer.root, "identities")
		require.NoError(t, os.MkdirAll(filepath.Join(ids, "test-agent.ext"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(ids, "test-agent.yaml"),
			[]byte("name: Agent\nhandle: test-agent\nkind: agent\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(ids, "test-agent.ext", "quarry.yaml"),
			[]byte("collection: "+layer.value+"\n"), 0o600))
	}
	return filepath.Join(record, "identities", "test-agent.yaml")
}

func cliExtOwnershipSnapshot(t *testing.T, roots ...string) map[string][sha256.Size]byte {
	t.Helper()
	result := make(map[string][sha256.Size]byte)
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			result[path] = sha256.Sum256([]byte(entry.Type().String()))
			if entry.Type().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				result[path] = sha256.Sum256(data)
			} else if entry.Type()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				result[path] = sha256.Sum256([]byte(target))
			}
			return nil
		}))
	}
	return result
}

func TestCLI_ExtensionOwnershipRefusal(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "regression requires the actual CLI")
	for _, kind := range []string{"inside-symlink", "outside-symlink", "dangling-symlink", "directory", "ancestor-file", "ancestor-symlink"} {
		t.Run(kind, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			path := cliExtOwnershipFixture(t, se)
			stdout, _, exit := runCLI(t, se, "ext", "get", "test-agent", "quarry", "collection")
			require.Equal(t, 0, exit)
			require.True(t, strings.Contains(stdout, "repo-base"), "control must read the repository, not the active bundle")
			_, _, exit = runCLI(t, se, "ext", "set", "test-agent", "quarry", "collection", "repo-overlay", "--local")
			require.Equal(t, 0, exit)
			badPath, reason := path, "symlink"
			switch kind {
			case "inside-symlink", "outside-symlink":
				target := filepath.Join(filepath.Dir(path), "original.yaml")
				if kind == "outside-symlink" {
					target = filepath.Join(se.home, "outside.yaml")
				}
				require.NoError(t, os.Rename(path, target))
				require.NoError(t, os.Symlink(target, path))
			case "dangling-symlink":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink(filepath.Join(se.home, "absent.yaml"), path))
			case "directory":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700))
				reason = "not a regular file"
			case "ancestor-file", "ancestor-symlink":
				badPath = filepath.Dir(path)
				target := filepath.Join(se.home, "original-identities")
				require.NoError(t, os.Rename(badPath, target))
				if kind == "ancestor-file" {
					require.NoError(t, os.WriteFile(badPath, []byte("not a directory"), 0o600))
					reason = "not a directory"
				} else {
					require.NoError(t, os.Symlink(target, badPath))
				}
			}
			before := cliExtOwnershipSnapshot(t, se.repo, se.home)
			for _, args := range [][]string{
				{"show", "test-agent"},
				{"ext", "get", "test-agent", "quarry"},
				{"ext", "list", "test-agent"},
				{"ext", "set", "test-agent", "quarry", "collection", "changed"},
				{"ext", "set", "test-agent", "quarry", "collection", "changed", "--local"},
				{"ext", "del", "test-agent", "quarry", "collection"},
				{"ext", "delete", "test-agent", "quarry"},
				{"ext", "delete", "test-agent", "quarry", "--local"},
			} {
				stdout, stderr, exit := runCLI(t, se, args...)
				assert.Equal(t, 1, exit, "command must refuse")
				assert.Empty(t, stdout, "no alternate-layer data on refusal")
				assert.Contains(t, stderr, badPath)
				assert.Contains(t, stderr, reason)
				assert.NotContains(t, stderr, "active bundle", "the error must retain the rejected owner")
				assert.False(t, strings.Contains(stdout+stderr, "repo-overlay"), "private fixture value must not appear")
			}
			assert.Equal(t, before, cliExtOwnershipSnapshot(t, se.repo, se.home), "all layers and redirected targets must remain unchanged")
		})
	}
}

func TestCLI_ExtensionOwnershipSelection(t *testing.T) {
	require.NotEmpty(t, ethosBinary)
	for _, mode := range []string{"repo-only", "layered"} {
		t.Run(mode, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			path := cliExtOwnershipFixture(t, se)
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"),
				[]byte("resolution: "+mode+"\nactive_bundle: ownership\n"), 0o600))
			_, _, exit := runCLI(t, se, "ext", "set", "test-agent", "quarry", "collection", "selected-overlay", "--local")
			require.Equal(t, 0, exit)
			stdout, _, exit := runCLI(t, se, "ext", "get", "test-agent", "quarry")
			require.Equal(t, 0, exit)
			assert.True(t, strings.Contains(stdout, "selected-overlay"), "selected local overlay must win")
			_, _, exit = runCLI(t, se, "ext", "set", "test-agent", "quarry", "other", "public")
			require.Equal(t, 0, exit)
			base := filepath.Join(filepath.Dir(path), "test-agent.ext", "quarry.yaml")
			if mode == "layered" {
				base = filepath.Join(se.home, ".punt-labs", "ethos", "identities", "test-agent.ext", "quarry.yaml")
			}
			data, err := os.ReadFile(base)
			require.NoError(t, err)
			assert.True(t, strings.Contains(string(data), "other: public"))
			assert.False(t, strings.Contains(string(data), "selected-overlay"), "base writes must not serialize overlays")
			require.NoError(t, os.Remove(path))
			stdout, _, exit = runCLI(t, se, "ext", "get", "test-agent", "quarry")
			require.Equal(t, 0, exit)
			want := "bundle-base"
			if mode == "layered" {
				want = "selected-overlay"
			}
			assert.True(t, strings.Contains(stdout, want), "only actual absence permits bundle fallback in repo-only mode")
			for _, args := range [][]string{
				{"ext", "set", "test-agent", "quarry", "other", "public"},
				{"ext", "delete", "test-agent", "quarry", "other"},
			} {
				_, stderr, exit := runCLI(t, se, args...)
				if mode == "repo-only" {
					assert.Equal(t, 1, exit)
					assert.Contains(t, stderr, "active bundle")
				} else {
					assert.Equal(t, 0, exit)
				}
			}
		})
	}
}

func TestCLI_ExtensionListCheckedOwnership(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "regression requires the actual CLI")
	for _, mode := range []string{"repo-only", "layered"} {
		for _, lowerCopy := range []bool{false, true} {
			copyName := "no-lower-copy"
			if lowerCopy {
				copyName = "same-handle-bundle-and-global"
			}
			for _, kind := range []string{"inside-symlink", "outside-symlink", "dangling-symlink", "directory", "ancestor-file", "ancestor-symlink"} {
				t.Run(mode+"/"+copyName+"/"+kind, func(t *testing.T) {
					se := setupCLISubprocessEnv(t)
					path := cliExtOwnershipFixture(t, se)
					require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"),
						[]byte("resolution: "+mode+"\nactive_bundle: ownership\n"), 0o600))
					if !lowerCopy {
						for _, root := range []string{
							filepath.Join(se.repo, ".punt-labs", "ethos-bundles", "ownership"),
							filepath.Join(se.home, ".punt-labs", "ethos"),
						} {
							require.NoError(t, os.Remove(filepath.Join(root, "identities", "test-agent.yaml")))
							require.NoError(t, os.RemoveAll(filepath.Join(root, "identities", "test-agent.ext")))
						}
					}
					// A private local overlay must never be printed or modified by
					// the ownership check, whether another identity copy exists or not.
					local := filepath.Join(se.repo, ".punt-labs", "local", "ethos", "identities", "test-agent.ext")
					require.NoError(t, os.MkdirAll(local, 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(local, "private.local.yaml"), []byte("key: private-fixture-value\n"), 0o600))
					badPath, reason := path, "symlink"
					switch kind {
					case "inside-symlink", "outside-symlink":
						target := filepath.Join(filepath.Dir(path), "original.yaml")
						if kind == "outside-symlink" {
							target = filepath.Join(se.home, "outside.yaml")
						}
						require.NoError(t, os.Rename(path, target))
						require.NoError(t, os.Symlink(target, path))
					case "dangling-symlink":
						require.NoError(t, os.Remove(path))
						require.NoError(t, os.Symlink(filepath.Join(se.home, "absent.yaml"), path))
					case "directory":
						require.NoError(t, os.Remove(path))
						require.NoError(t, os.Mkdir(path, 0o700))
						reason = "not a regular file"
					case "ancestor-file", "ancestor-symlink":
						badPath = filepath.Dir(path)
						target := filepath.Join(se.home, "original-identities")
						require.NoError(t, os.Rename(badPath, target))
						if kind == "ancestor-file" {
							require.NoError(t, os.WriteFile(badPath, []byte("not a directory"), 0o600))
							reason = "not a directory"
						} else {
							require.NoError(t, os.Symlink(target, badPath))
						}
					}
					before := cliExtOwnershipSnapshot(t, se.repo, se.home)
					for _, output := range []string{"human", "json"} {
						t.Run(output, func(t *testing.T) {
							args := []string{"ext", "list", "test-agent"}
							if output == "json" {
								args = append(args, "--json")
							}
							stdout, stderr, exit := runCLI(t, se, args...)
							assert.Equal(t, 1, exit)
							assert.Empty(t, stdout, "refusal must not list any layer's namespaces")
							assert.Contains(t, stderr, "identity path "+badPath+": "+reason)
							assert.NotContains(t, stderr, "does not exist", "a rejected owner is not genuine absence")
							for _, value := range []string{"private-fixture-value", "repo-base", "bundle-base", "global-base"} {
								assert.False(t, strings.Contains(stdout+stderr, value), "diagnostics must not contain extension values")
							}
						})
					}
					assert.Equal(t, before, cliExtOwnershipSnapshot(t, se.repo, se.home), "listing must not mutate any layer or redirected target")
				})
			}
		}
	}
}

func TestCLI_ExtensionListOwnershipControls(t *testing.T) {
	require.NotEmpty(t, ethosBinary)
	for _, mode := range []string{"repo-only", "layered"} {
		for _, state := range []string{"empty", "missing", "malformed", "repository-owned", "bundle-owned", "global-owned"} {
			t.Run(mode+"/"+state, func(t *testing.T) {
				se := setupCLISubprocessEnv(t)
				path := cliExtOwnershipFixture(t, se)
				require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"),
					[]byte("resolution: "+mode+"\nactive_bundle: ownership\n"), 0o600))
				roots := []struct{ root, marker string }{
					{filepath.Join(se.repo, ".punt-labs", "ethos"), "repo-only"},
					{filepath.Join(se.repo, ".punt-labs", "ethos-bundles", "ownership"), "bundle-only"},
					{filepath.Join(se.home, ".punt-labs", "ethos"), "global-only"},
				}
				for _, layer := range roots {
					extDir := filepath.Join(layer.root, "identities", "test-agent.ext")
					if state == "empty" || state == "malformed" {
						require.NoError(t, os.RemoveAll(extDir))
					} else {
						require.NoError(t, os.WriteFile(filepath.Join(extDir, layer.marker+".yaml"), []byte("key: public\n"), 0o600))
					}
				}
				// Listing must neither resolve unrelated attributes nor migrate
				// legacy voice data, even when the selected identity is valid.
				legacy := []byte("name: Agent\nhandle: test-agent\nkind: agent\npersonality: absent-personality\nvoice:\n  provider: fixture-provider\n  voice_id: fixture-id\n")
				for _, layer := range roots {
					require.NoError(t, os.WriteFile(filepath.Join(layer.root, "identities", "test-agent.yaml"), legacy, 0o600))
				}
				switch state {
				case "missing":
					for _, layer := range roots {
						require.NoError(t, os.Remove(filepath.Join(layer.root, "identities", "test-agent.yaml")))
					}
				case "malformed":
					require.NoError(t, os.WriteFile(path, []byte("name: [unclosed\n"), 0o600))
				case "bundle-owned", "global-owned":
					require.NoError(t, os.Remove(path))
					if state == "global-owned" {
						require.NoError(t, os.Remove(filepath.Join(roots[1].root, "identities", "test-agent.yaml")))
					}
				}
				before := cliExtOwnershipSnapshot(t, se.repo, se.home)
				for _, output := range []string{"human", "json"} {
					t.Run(output, func(t *testing.T) {
						args := []string{"ext", "list", "test-agent"}
						if output == "json" {
							args = append(args, "--json")
						}
						stdout, stderr, exit := runCLI(t, se, args...)
						if state == "missing" || state == "global-owned" && mode == "repo-only" {
							assert.Equal(t, 1, exit)
							assert.Empty(t, stdout)
							assert.Contains(t, stderr, "handle \"test-agent\" does not exist")
							return
						}
						if state == "malformed" {
							assert.Equal(t, 1, exit)
							assert.Empty(t, stdout)
							assert.Contains(t, stderr, "invalid identity file "+path)
							assert.NotContains(t, stderr, "does not exist")
							return
						}
						require.Equal(t, 0, exit)
						assert.Empty(t, stderr)
						if state == "empty" {
							want := "No extensions for \"test-agent\"."
							if output == "json" {
								want = "[]"
							}
							assert.Equal(t, want, strings.TrimSpace(stdout))
							return
						}
						marker := "repo-only"
						if state == "bundle-owned" {
							marker = "bundle-only"
						}
						if mode == "layered" {
							marker = "global-only"
						}
						names := strings.Fields(stdout)
						if output == "json" {
							require.NoError(t, json.Unmarshal([]byte(stdout), &names))
						}
						assert.ElementsMatch(t, []string{marker, "quarry"}, names)
					})
				}
				assert.Equal(t, before, cliExtOwnershipSnapshot(t, se.repo, se.home), "listing must not migrate legacy voice or create locks")
			})
		}
	}
}

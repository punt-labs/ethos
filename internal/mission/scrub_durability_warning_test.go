package mission

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestScrubInstalledDurabilityWarning(t *testing.T) {
	const mission = ".punt-labs/ethos/missions/m-2026-01-01-001/"
	for _, target := range []struct{ name, rel, body string }{
		{"summary", ".punt-labs/ethos/missions.jsonl", "{\"id\":\"m-2026-01-01-001\",\"success_criteria\":[\"a@b.org\"]}\n"},
		{"contract", mission + "contract.yaml", "mission_id: m-2026-01-01-001\ncontext: a@b.org\nsuccess_criteria: [works]\n"},
		{"results", mission + "results.yaml", "results:\n  - prose: a@b.org\n"},
		{"reflections", mission + "reflections.yaml", "reflections:\n  - reason: a@b.org\n"},
		{"delegation record", mission + "delegations/d-1/record.yaml", "id: d-1\nsession: a@b.org\n"},
		{"prompt", mission + "delegations/d-1/prompt.md", "read a@b.org\n"},
		{"contract export", ".punt-labs/ethos/missions/plain.contract.yaml", "mission_id: m-2026-01-01-002\ncontext: a@b.org\nsuccess_criteria: [works]\n"},
		{"result export", ".punt-labs/ethos/missions/plain.result.yaml", "prose: a@b.org\n"},
	} {
		for _, failSync := range []bool{false, true} {
			t.Run(target.name+"/"+map[bool]string{false: "healthy", true: "directory sync fails"}[failSync], func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				root, global := t.TempDir(), t.TempDir()
				writeTree(t, root, map[string]string{target.rel: target.body})
				dest := filepath.Join(root, filepath.FromSlash(target.rel))
				real := syncDir
				t.Cleanup(func() { syncDir = real })
				calls := 0
				var installed []byte
				syncDir = func(dir string) error {
					if dir == filepath.Dir(dest) {
						calls++
						var err error
						installed, err = os.ReadFile(dest)
						require.NoError(t, err, "rename must already have installed the scrubbed bytes")
						if failSync {
							return errors.New("injected scrub directory sync failure")
						}
					}
					return real(dir)
				}
				var res ScrubResult
				var err error
				stderr := captureStderr(t, func() {
					// Only this target is scrubbed; summary coverage cannot be
					// satisfied by an unrelated delegation-directory failure.
					res, err = ScrubFiles(root, global, []string{target.rel}, false)
				})
				require.NoError(t, err, "post-rename sync failure is not a not-applied error")
				assert.Equal(t, []string{target.rel}, res.Changed)
				assert.Empty(t, res.Skipped)
				assert.Equal(t, 1, calls, "the target directory must actually reach syncDir")
				after := readFileRel(t, root, target.rel)
				assert.NotEqual(t, target.body, after)
				assert.NotContains(t, after, "a@b.org")
				assert.Contains(t, after, "[redacted-email]")
				assert.Equal(t, after, string(installed))
				if failSync {
					assert.Equal(t, "ethos: mission records: "+dest+": rewrite installed; durability unconfirmed: injected scrub directory sync failure\n", stderr)
					assert.Equal(t, 1, strings.Count(stderr, "durability unconfirmed"))
					assert.NotContains(t, stderr, "ethos: delegation:")
					if target.name == "summary" {
						assert.NotContains(t, stderr, "delegation")
					}
				} else {
					assert.Empty(t, stderr, "a healthy scrub must not warn")
				}
				assert.NotContains(t, stderr, "a@b.org", "diagnostics must not include record bodies")
				assert.NotContains(t, stderr, "not applied")

				stderr = captureStderr(t, func() { res, err = ScrubFiles(root, global, []string{target.rel}, false) })
				require.NoError(t, err)
				assert.Empty(t, res.Changed, "already canonical bytes are not reinstalled")
				assert.Empty(t, stderr)
				assert.Equal(t, 1, calls, "no second installation or warning")
			})
		}
	}
}

func TestScrubDurabilityWarningWithoutInstallation(t *testing.T) {
	for _, mode := range []string{"dry run", "malformed", "staging obstructed"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			const rel = ".punt-labs/ethos/missions.jsonl"
			body := "{\"id\":\"m-2026-01-01-001\",\"success_criteria\":[\"a@b.org\"]}\n"
			if mode == "malformed" {
				body = "not JSON a@b.org\n"
			}
			writeTree(t, root, map[string]string{rel: body})
			if mode == "staging obstructed" {
				p, err := layout.NewRepository(root, root, layout.RecordBlame)
				require.NoError(t, err)
				require.NoError(t, os.MkdirAll(filepath.Dir(p.TempDir()), 0o700))
				require.NoError(t, os.WriteFile(p.TempDir(), []byte("blocked"), 0o600))
			}
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			calls := 0
			syncDir = func(string) error {
				calls++
				return errors.New("unexpected directory sync")
			}
			var res ScrubResult
			var err error
			stderr := captureStderr(t, func() { res, err = ScrubFiles(root, t.TempDir(), []string{rel}, mode == "dry run") })
			if mode == "dry run" {
				require.NoError(t, err)
				assert.Equal(t, []string{rel}, res.Changed)
			} else {
				require.Error(t, err)
				assert.Empty(t, res.Changed)
			}
			assert.Zero(t, calls)
			assert.Empty(t, stderr, "no installed-write warning without installation")
			assert.Equal(t, body, readFileRel(t, root, rel))
		})
	}
}

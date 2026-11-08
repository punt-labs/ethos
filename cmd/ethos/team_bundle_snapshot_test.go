//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func capturedTeamCommand(t *testing.T, env *bundleTestEnv, initial, replacement string) (*cobra.Command, *bytes.Buffer, resolve.Located) {
	t.Helper()
	if initial != "" {
		writeRepoConfigFile(t, env.repo, initial)
	}
	entry := resolve.Roots()
	require.NoError(t, entry.Err())
	cmdRoots.Store(&repoRoots{Located: entry, Global: true})
	t.Cleanup(func() { cmdRoots.Store(nil) })
	writeRepoConfigFile(t, env.repo, replacement)
	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })
	cmd := &cobra.Command{}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd, out, entry
}

func TestTeamAvailableSnapshot_ConfigReplacement(t *testing.T) {
	for _, initial := range []string{"", "active_bundle: foundation\n"} {
		for _, replacement := range []string{"active_bundle: later\n", "active_bundle: [invalid\n"} {
			t.Run(initial+"/"+replacement, func(t *testing.T) {
				env := setupBundleTestEnv(t)
				mkBundleDir(t, filepath.Join(env.globalRoot, "bundles"), "foundation", true)
				selected := mkBundleDir(t, filepath.Join(env.repo, ".punt-labs", "ethos-bundles"), "foundation", true)
				mkBundleDir(t, filepath.Join(env.globalRoot, "bundles"), "later", true)
				cmd, out, entry := capturedTeamCommand(t, env, initial, replacement)
				require.NoError(t, runTeamAvailable(cmd), out.String())
				var rows []availableRow
				require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
				require.Len(t, rows, 3)
				for _, row := range rows {
					assert.Equal(t, initial != "" && row.Path == selected, row.Active, "only the captured repo bundle is marked: %+v", row)
				}
				if initial == "" {
					assert.Nil(t, entry.Config)
				} else {
					assert.Equal(t, "foundation", entry.Config.ActiveBundle)
				}
			})
		}
	}
}

func TestTeamActiveSnapshot_ConfigReplacement(t *testing.T) {
	for _, initial := range []string{"", "active_bundle: foundation\n"} {
		for _, replacement := range []string{"active_bundle: later\n", "active_bundle: [invalid\n"} {
			t.Run(initial+"/"+replacement, func(t *testing.T) {
				env := setupBundleTestEnv(t)
				mkBundleDir(t, filepath.Join(env.globalRoot, "bundles"), "foundation", true)
				mkBundleDir(t, filepath.Join(env.globalRoot, "bundles"), "later", true)
				cmd, out, entry := capturedTeamCommand(t, env, initial, replacement)
				require.NoError(t, runTeamActive(cmd), out.String())
				if initial == "" {
					assert.Nil(t, entry.Config)
					assert.Equal(t, "null\n", out.String(), "nil config does not adopt a later activation")
				} else {
					var row availableRow
					require.NoError(t, json.Unmarshal(out.Bytes(), &row))
					assert.Equal(t, "foundation", row.Name)
					assert.True(t, row.Active)
					assert.Equal(t, "foundation", entry.Config.ActiveBundle)
				}
			})
		}
	}
}

func TestTeamDeactivateSnapshot_ConfigReplacement(t *testing.T) {
	for _, initial := range []string{"", "agent: claude\n", "active_bundle: foundation\n"} {
		t.Run(initial, func(t *testing.T) {
			env := setupBundleTestEnv(t)
			replacement := "active_bundle: later\nteam: later\ncustom: preserved\n"
			cmd, out, entry := capturedTeamCommand(t, env, initial, replacement)
			require.NoError(t, runTeamDeactivate(cmd), out.String())
			var result map[string]string
			require.NoError(t, json.Unmarshal(out.Bytes(), &result))
			if initial == "active_bundle: foundation\n" {
				assert.Equal(t, "foundation", result["deactivated"], "report the captured selection, not the later activation")
				assert.Equal(t, "foundation", entry.Config.ActiveBundle)
				body := readRepoConfigFile(t, env.repo)
				assert.NotContains(t, body, "active_bundle")
				assert.Contains(t, body, "team: later")
				assert.Contains(t, body, "custom: preserved")
			} else {
				assert.Equal(t, "no-active-bundle", result["status"])
				assert.Equal(t, replacement, readRepoConfigFile(t, env.repo), "a no-op must not deactivate a bundle activated after capture")
			}
		})
	}
}

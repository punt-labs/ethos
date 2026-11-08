package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func breakAgentOwnershipIdentity(t *testing.T, path, failure string) {
	t.Helper()
	switch failure {
	case "malformed":
		writeFile(t, path, "kind: [unterminated\n")
	case "unreadable":
		// A directory fails ReadFile even when tests run as root. Unlike
		// chmod(000), this exercises the read error on every supported OS.
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Mkdir(path, 0o700))
	case "missing":
		require.NoError(t, os.Remove(path))
	default:
		t.Fatalf("unknown identity failure %q", failure)
	}
}

func TestSessionStartRefusesStubsWhenMemberClassificationFails(t *testing.T) {
	for _, failure := range []string{"malformed", "unreadable", "missing"} {
		t.Run(failure, func(t *testing.T) {
			root, ids, teams, roles := setupTestRepo(t)
			isolateGitConfig(t, "test-human")
			// Use a distinct checkout to exercise the same S/C split as a
			// linked worktree without rediscovering roots during the hook.
			checkout := physicalAgentTempDir(t)
			require.NoError(t, GenerateAgentFilesTo(root, checkout, ids, teams, roles))
			path := filepath.Join(checkout, ".claude", "agents", "bwk.md")
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(before), "You are Brian K (bwk)")

			ethos := filepath.Join(root, ".punt-labs", "ethos")
			writeFile(t, filepath.Join(ethos, "agents", "bwk.md"), "conflicting legacy stub\n")
			writeFile(t, filepath.Join(ethos, "agents", "legacy-only.md"), "unowned legacy stub\n")
			breakAgentOwnershipIdentity(t, filepath.Join(ethos, "identities", "bwk.yaml"), failure)
			// Only the member identity is broken: SessionStart must reach
			// planning, not stop at its main-persona fallback.
			main, err := ids.Load("claude")
			require.NoError(t, err)
			require.Equal(t, "Claude Agento", main.Name)
			require.NotEmpty(t, main.PersonalityContent)
			cfg, err := resolve.ConfigOf(root)
			require.NoError(t, err)
			loc := resolve.Located{Store: root, Checkout: checkout, Ethos: ethos, Config: cfg}
			deps := SessionStartDeps{Located: &loc, Store: ids, Sessions: session.NewStore(physicalAgentTempDir(t)), Teams: teams, Roles: roles}
			var hookErr error
			stderr := captureStderr(t, func() {
				_, hookErr = captureStdout(t, func() error {
					return HandleSessionStart(strings.NewReader(`{"session_id":"ownership-error"}`), deps)
				})
			})
			t.Logf("SessionStart error: %v\nstderr:\n%s", hookErr, stderr)

			assert.ErrorContains(t, hookErr, `classifying team member "bwk" for generated agent ownership`)
			assert.Contains(t, stderr, `classifying team member "bwk" for generated agent ownership`)
			assert.Contains(t, stderr, "installing no stubs")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after, "an unknown ownership set must never replace a generated agent with its stub")
			assert.NoFileExists(t, filepath.Join(checkout, ".claude", "agents", "legacy-only.md"), "classification failure must refuse all stub installation")
			assert.NoDirExists(t, filepath.Join(root, ".claude"), "checkout effects must not target the shared store")
		})
	}
}

func TestGeneratedAgentHandlesRefusesUnclassifiedMembers(t *testing.T) {
	for _, member := range []string{"bwk", "test-human"} {
		for _, failure := range []string{"malformed", "unreadable", "missing"} {
			t.Run(member+"/"+failure, func(t *testing.T) {
				root, ids, teams, _ := setupTestRepo(t)
				breakAgentOwnershipIdentity(t, filepath.Join(root, ".punt-labs", "ethos", "identities", member+".yaml"), failure)
				_, err := GeneratedAgentHandles(root, ids, teams)
				assert.ErrorContains(t, err, `classifying team member "`+member+`" for generated agent ownership`)
				if failure == "missing" {
					assert.ErrorIs(t, err, os.ErrNotExist, "classification errors must preserve the underlying cause")
				}
			})
		}
	}
}

func TestGeneratedAgentHandlesRefusesUnavailableIdentityStore(t *testing.T) {
	root, _, teams, _ := setupTestRepo(t)
	_, err := GeneratedAgentHandles(root, nil, teams)
	assert.ErrorContains(t, err, `classifying team member "bwk" for generated agent ownership: identity store is unavailable`)
}

func TestSessionStartKeepsOwnershipForRenderingSkips(t *testing.T) {
	for _, skipped := range []string{"missing role", "no tools", "empty personality"} {
		t.Run(skipped, func(t *testing.T) {
			root, ids, teams, roles := setupTestRepo(t)
			isolateGitConfig(t, "test-human")
			require.NoError(t, GenerateAgentFilesTo(root, root, ids, teams, roles))
			path := filepath.Join(root, ".claude", "agents", "bwk.md")
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			ethos := filepath.Join(root, ".punt-labs", "ethos")
			writeFile(t, filepath.Join(ethos, "agents", "bwk.md"), "conflicting legacy stub\n")
			writeFile(t, filepath.Join(ethos, "agents", "legacy-only.md"), "unowned legacy stub\n")
			switch skipped {
			case "missing role":
				require.NoError(t, os.Remove(filepath.Join(ethos, "roles", "go-specialist.yaml")))
			case "no tools":
				writeFile(t, filepath.Join(ethos, "roles", "go-specialist.yaml"), "name: go-specialist\n")
			case "empty personality":
				writeFile(t, filepath.Join(ethos, "personalities", "kernighan.md"), "")
			}
			cfg, err := resolve.ConfigOf(root)
			require.NoError(t, err)
			loc := resolve.Located{Store: root, Checkout: root, Ethos: ethos, Config: cfg}
			deps := SessionStartDeps{Located: &loc, Store: ids, Sessions: session.NewStore(physicalAgentTempDir(t)), Teams: teams, Roles: roles}
			out, err := captureStdout(t, func() error {
				return HandleSessionStart(strings.NewReader(`{"session_id":"ownership-render-skip"}`), deps)
			})
			require.NoError(t, err, "rendering skips after classification remain intentional")
			assert.Contains(t, out, "Claude Agento")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.FileExists(t, filepath.Join(root, ".claude", "agents", "legacy-only.md"), "successful classification still permits unowned stubs")
		})
	}
}

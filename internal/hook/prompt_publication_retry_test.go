package hook

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/stretchr/testify/require"
)

// Exercise the ordinary repository seal, not the prompt retry helper directly.
// Captured full consent survives a failed first publication and a mode switch;
// retry must neither promote private prompts nor query/change the blame index.
func TestRepositorySealRetriesCapturedPromptPublication(t *testing.T) {
	for _, scenario := range []string{"retry", "dry-run", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			store, checkout := t.TempDir(), t.TempDir()
			store, err := filepath.EvalSymlinks(store)
			require.NoError(t, err)
			checkout, err = filepath.EvalSymlinks(checkout)
			require.NoError(t, err)
			full, err := layout.NewRepository(store, checkout, layout.RecordFull)
			require.NoError(t, err)
			blame, err := layout.NewRepository(store, checkout, layout.RecordBlame)
			require.NoError(t, err)
			const mid, did = "m-2026-11-05-902", "d-2026-11-05-902"
			blocker := filepath.Join(full.CheckoutRecordRoot(), "missions")
			require.NoError(t, os.MkdirAll(filepath.Dir(blocker), 0o700))
			require.NoError(t, os.WriteFile(blocker, []byte("unrelated blocker"), 0o600))
			body := []byte("captured full prompt")
			_, err = mission.WriteDelegationSkeletonWithPolicy(full, mid, did, mission.DelegationSkeleton{Tier: mission.TierB, Prompt: body})
			require.ErrorContains(t, err, "pending")
			localDir := filepath.Join(full.LocalRoot(), "trace", "missions", mid, "delegations", did)
			consent, err := os.ReadFile(filepath.Join(localDir, "prompt-consent.yaml"))
			require.NoError(t, err)
			require.NoError(t, os.Remove(blocker))
			const privateID = "d-2026-11-05-903"
			_, err = mission.WriteDelegationSkeletonWithPolicy(blame, mid, privateID, mission.DelegationSkeleton{Tier: mission.TierB, Prompt: []byte("private prompt")})
			require.NoError(t, err)
			published := filepath.Join(blocker, mid, "delegations", did, "prompt.md")
			if scenario == "conflict" {
				require.NoError(t, os.MkdirAll(filepath.Dir(published), 0o700))
				require.NoError(t, os.WriteFile(published, []byte("unrelated published bytes"), 0o600))
			}
			_, err = SealRepoWithPolicy(blame, time.Now(), SealOptions{DryRun: scenario == "dry-run"})
			if scenario == "conflict" {
				require.ErrorContains(t, err, "prompt")
				got, readErr := os.ReadFile(published)
				require.NoError(t, readErr)
				require.Equal(t, "unrelated published bytes", string(got))
			} else {
				require.NoError(t, err)
				if scenario == "dry-run" {
					require.NoFileExists(t, published)
				} else {
					got, readErr := os.ReadFile(published)
					require.NoError(t, readErr)
					require.Equal(t, body, got)
					_, err = SealRepoWithPolicy(blame, time.Now(), SealOptions{})
					require.NoError(t, err, "equal copies remain retryable")
				}
			}
			retained, err := os.ReadFile(filepath.Join(localDir, "prompt.md"))
			require.NoError(t, err)
			require.Equal(t, body, retained)
			after, err := os.ReadFile(filepath.Join(localDir, "prompt-consent.yaml"))
			require.NoError(t, err)
			require.Equal(t, consent, after)
			require.NoFileExists(t, filepath.Join(blocker, mid, "delegations", privateID, "prompt.md"))
			// No Git repository exists in either root: successful blame retry
			// cannot rely on an index probe or staging operation.
		})
	}
}

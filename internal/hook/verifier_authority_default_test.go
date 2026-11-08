package hook

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

// The gate must apply the same legacy-only round default as Store.Load.
// In particular, an incomplete v1 contract cannot acquire authority merely
// because a verifier reads it rather than another mission-store caller.
func TestSubagentStart_VerifierAuthorityDefault(t *testing.T) {
	tests := []struct {
		name       string
		legacy     bool
		round      int
		omitRound  bool
		truncate   bool
		wantRefuse bool
	}{
		{name: "v1 explicit zero refuses", wantRefuse: true},
		{name: "v1 omitted round refuses", omitRound: true, wantRefuse: true},
		{name: "v1 negative round refuses", round: -1, wantRefuse: true},
		{name: "v1 round beyond budget refuses", round: 4, wantRefuse: true},
		{name: "interrupted YAML refuses", round: 1, truncate: true, wantRefuse: true},
		{name: "v1 round one remains valid", round: 1},
		{name: "legacy explicit zero defaults", legacy: true},
		{name: "legacy omitted round defaults", legacy: true, omitRound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, ids, _, sessions, hash := setupVerifierTest(t, "djb")
			repo := physicalAgentTempDir(t)
			policy, err := layout.NewRepository(repo, repo, layout.RecordBlame)
			require.NoError(t, err)
			missions := mission.NewRepositoryStore(policy, dir)
			t.Setenv("DELEGATION_ID", "") // Refusal must not close an ambient skeleton.
			c := validVerifierContract("djb")
			require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
			require.NoError(t, missions.Create(&c))
			created, err := missions.Load(c.MissionID)
			require.NoError(t, err)
			require.NotNil(t, created.RoundDecisions)
			c = *created
			c.CurrentRound = tt.round
			if tt.legacy {
				c.RoundDecisions = nil
			}
			raw, err := yaml.Marshal(&c)
			require.NoError(t, err)
			if tt.round == 0 {
				require.NotContains(t, string(raw), "current_round:", "zero is omitted by the struct tag")
				if !tt.omitRound {
					raw = append([]byte("current_round: 0\n"), raw...)
				}
			}
			// Comments must survive the hook's single read, including when
			// the parsed legacy contract receives its in-memory default.
			raw = append([]byte("# Pinned verifier authority fixture.\n"), raw...)
			if tt.truncate {
				raw = append(raw, []byte("interrupted: [\n")...)
			}
			path, err := missions.ContractPath(c.MissionID)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, raw, 0o600))

			loaded, loadErr := missions.Load(c.MissionID)
			out, hookErr := runHookForVerifier(t, ids, sessions, missions, hash, "djb", c.MissionID)
			roster, err := sessions.Load("verifier-test-djb")
			require.NoError(t, err)
			if tt.wantRefuse {
				require.Error(t, loadErr, "the store must reject this authority fixture")
				assert.Error(t, hookErr, "the verifier must not repair malformed authority")
				if hookErr != nil {
					assert.Contains(t, hookErr.Error(), c.MissionID)
					if tt.round <= 0 {
						assert.Contains(t, hookErr.Error(), "current_round")
					}
				}
				assert.Empty(t, out, "refusal must precede any verifier context or allowlist")
				for _, participant := range roster.Participants {
					assert.NotEqual(t, "sub-verifier", participant.AgentID, "refusal must precede roster join")
				}
			} else {
				require.NoError(t, loadErr)
				require.NoError(t, hookErr)
				assert.Equal(t, 1, loaded.CurrentRound)
				var result SubagentStartResult
				require.NoError(t, json.Unmarshal([]byte(out), &result))
				assert.Contains(t, result.HookSpecificOutput.AdditionalContext, "```yaml\n"+string(raw)+"```")
				assert.NotEmpty(t, result.Env["ETHOS_VERIFIER_ALLOWLIST"])
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, raw, after, "reading must not rewrite pinned authority")
		})
	}
}

package session

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetExistingPersona(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *Store)
		want   string
	}{
		{"existing exact participant", func(*testing.T, *Store) {}, ""},
		{"removed participant", func(t *testing.T, s *Store) {
			t.Helper()
			require.NoError(t, s.Leave("ses_A", "ses_A"))
		}, "not found"},
		{"deleted roster", func(t *testing.T, s *Store) {
			t.Helper()
			require.NoError(t, os.Remove(s.rosterPath("ses_A")))
		}, "not found"},
		{"mismatched roster", func(t *testing.T, s *Store) {
			t.Helper()
			require.NoError(t, os.WriteFile(s.rosterPath("ses_A"), []byte("session: ses_B\n"), 0o600))
		}, "does not match"},
		{"duplicate caller", func(t *testing.T, s *Store) {
			t.Helper()
			require.NoError(t, os.WriteFile(s.rosterPath("ses_A"), []byte("session: ses_A\nparticipants: [{agent_id: ses_A}, {agent_id: ses_A}]\n"), 0o600))
		}, "duplicated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore(t.TempDir())
			require.NoError(t, s.Create("ses_A", Participant{AgentID: "human", Persona: "human"},
				Participant{AgentID: "ses_A", Persona: "alice", Parent: "human", AgentType: "worker", Ext: map[string]any{"key": "value"}}, "", ""))
			tc.mutate(t, s)
			before, readErr := os.ReadFile(s.rosterPath("ses_A"))
			err := s.SetExistingPersona("ses_A", "ses_A", "bob")
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				after, afterErr := os.ReadFile(s.rosterPath("ses_A"))
				assert.Equal(t, before, after, "failed update must not change the roster")
				assert.Equal(t, os.IsNotExist(readErr), os.IsNotExist(afterErr))
				return
			}
			require.NoError(t, err)
			roster, err := s.Load("ses_A")
			require.NoError(t, err)
			require.Len(t, roster.Participants, 2)
			p := roster.FindParticipant("ses_A")
			require.NotNil(t, p)
			assert.Equal(t, "bob", p.Persona)
			assert.Equal(t, "human", p.Parent)
			assert.Equal(t, "worker", p.AgentType)
			assert.Equal(t, map[string]any{"key": "value"}, p.Ext)
			assert.Equal(t, "human", roster.FindParticipant("human").Persona)
		})
	}
}

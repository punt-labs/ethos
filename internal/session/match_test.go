//go:build !windows

package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_MatchByPrefix(t *testing.T) {
	s := testStore(t)

	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("abc-1234-5678", root, primary, "", ""))
	require.NoError(t, s.Create("abc-1234-9999", root, primary, "", ""))
	require.NoError(t, s.Create("def-5678-0000", root, primary, "", ""))
	require.NoError(t, s.Create("abc", root, primary, "", ""))
	require.NoError(t, s.Create("abc-def-1234", root, primary, "", ""))

	tests := []struct {
		name    string
		prefix  string
		want    string
		wantErr string
	}{
		{
			name:   "exact match",
			prefix: "abc-1234-5678",
			want:   "abc-1234-5678",
		},
		{
			name:   "unique prefix",
			prefix: "def",
			want:   "def-5678-0000",
		},
		{
			name:    "ambiguous prefix",
			prefix:  "abc-1234",
			wantErr: "ambiguous prefix",
		},
		{
			name:    "no match",
			prefix:  "zzz",
			wantErr: "no session matching prefix",
		},
		{
			name:   "exact match wins over prefix",
			prefix: "abc",
			want:   "abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.MatchByPrefix(tt.prefix)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

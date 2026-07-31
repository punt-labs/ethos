package hook

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	trailerMission    = "m-2026-07-31-004"
	trailerDelegation = "d-2026-07-31-021"
	trailerSession    = "sess-trailers"
)

// TestWriteCommitTrailers covers what the commit-msg hook reads back
// for the session that is committing (ethos-pobi), and which bindings
// are allowed to produce a trailer at all (the ethos-7vo3 ruling: a
// dispatch names a mission for someone else, so only an explicit
// claim stamps the operator's own commits).
func TestWriteCommitTrailers(t *testing.T) {
	tests := []struct {
		name           string
		missionID      string // active-mission sidecar; empty => no sidecar
		origin         string
		bindingMission string // delegation-binding sidecar; empty => no file
		wantMission    bool
		wantDelegation bool
	}{
		{
			name:      "no active mission yields nothing",
			missionID: "",
		},
		{
			name:        "claim without a delegation binding yields the mission only",
			missionID:   trailerMission,
			origin:      mission.BindOriginClaim,
			wantMission: true,
		},
		{
			name:           "claim with a matching binding yields both",
			missionID:      trailerMission,
			origin:         mission.BindOriginClaim,
			bindingMission: trailerMission,
			wantMission:    true,
			wantDelegation: true,
		},
		{
			// A binding left from a dispatch under another mission
			// must not ride along (ethos-jawp).
			name:           "claim with a binding for another mission drops the delegation",
			missionID:      trailerMission,
			origin:         mission.BindOriginClaim,
			bindingMission: "m-2026-07-31-999",
			wantMission:    true,
			wantDelegation: false,
		},
		{
			// The ruling: dispatch binds for delegation filing only.
			name:           "dispatch binding yields nothing",
			missionID:      trailerMission,
			origin:         mission.BindOriginDispatch,
			bindingMission: trailerMission,
			wantMission:    false,
			wantDelegation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.missionID != "" {
				require.NoError(t, mission.WriteActiveMissionOrigin(
					root, trailerSession, tt.missionID, tt.origin))
			}
			if tt.bindingMission != "" {
				require.NoError(t, mission.WriteDelegationBinding(root, trailerSession,
					mission.DelegationBinding{
						DelegationID:  trailerDelegation,
						MissionID:     tt.bindingMission,
						ParentSession: trailerSession,
					}))
			}

			var out bytes.Buffer
			require.NoError(t, WriteCommitTrailers(&out, root, trailerSession))

			got := out.String()
			assert.Equal(t, tt.wantMission, strings.Contains(got, "MISSION_ID="),
				"MISSION_ID line, got: %q", got)
			if tt.wantMission {
				assert.Contains(t, got, "MISSION_ID="+tt.missionID)
			}
			assert.Equal(t, tt.wantDelegation, strings.Contains(got, "DELEGATION_ID="),
				"DELEGATION_ID line, got: %q", got)
			if tt.wantDelegation {
				assert.Contains(t, got, "DELEGATION_ID="+trailerDelegation)
			}
		})
	}
}

// TestWriteCommitTrailers_LegacySidecarStillTrails asserts the
// compatibility rule: a sidecar written before the origin line existed
// came from `ethos mission claim`, so it keeps producing trailers
// across the upgrade.
func TestWriteCommitTrailers_LegacySidecarStillTrails(t *testing.T) {
	root := t.TempDir()
	path := mission.ActiveMissionPath(root, trailerSession)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(trailerMission+"\n"), 0o600))

	var out bytes.Buffer
	require.NoError(t, WriteCommitTrailers(&out, root, trailerSession))
	assert.Contains(t, out.String(), "MISSION_ID="+trailerMission)
}

// TestWriteCommitTrailers_NoSessionIsSilent asserts the fail-safe: an
// unresolved session or root writes nothing at all rather than
// guessing at a mission (ethos-pobi).
func TestWriteCommitTrailers_NoSessionIsSilent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, mission.WriteActiveMission(root, trailerSession, trailerMission))

	var out bytes.Buffer
	require.NoError(t, WriteCommitTrailers(&out, root, ""))
	assert.Empty(t, out.String(), "an unresolved session must produce no trailer")

	out.Reset()
	require.NoError(t, WriteCommitTrailers(&out, "", trailerSession))
	assert.Empty(t, out.String(), "an unresolved global root must produce no trailer")
}

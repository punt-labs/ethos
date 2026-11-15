//go:build !windows

package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitInitDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	out, err := exec.Command("git", "-C", d, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))
	return d
}

// writeForeignLive writes one live audit line under writer's live zone and, when
// sealed, a chunk covering it under writer's local trace archive (DES-079).
func writeForeignLive(t *testing.T, writer, sessionID string, sealed bool) {
	t.Helper()
	live := audit.LiveAuditPath(writer, sessionID)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	body := `{"ts":"` + audit.FormatLineTS(100) + `","session":"` + sessionID + `","tool":"Read"}` + "\n"
	require.NoError(t, os.WriteFile(live, []byte(body), 0o600))
	if !sealed {
		return
	}
	dir := filepath.Join(audit.LocalZoneBase(writer), "trace", "sessions", "2026-07-21-"+sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, audit.SessionChunkFile(100, 100)), []byte(body), 0o600))
}

// A roster with no repo identity, written by another checkout B, is probed
// against B's sealed chunks, not those of the checkout A the purge runs in.
func TestPurgeTombstoned_SealedInWriterStoreOfOtherCheckout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sealed bool
	}{{"sealed in writer store purges", true}, {"unsealed in writer store is kept", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			a, b := gitInitDir(t), gitInitDir(t)
			root := Participant{AgentID: "user1"}
			primary := Participant{AgentID: "9999999", Parent: "user1"}
			require.NoError(t, s.CreateInCheckout("sess-foreign", root, primary, "", b, ""))
			writeForeignLive(t, b, "sess-foreign", tc.sealed)

			purged, refused, err := s.PurgeTombstoned(a, a, "", false)
			require.NoError(t, err)
			if tc.sealed {
				assert.Contains(t, purged, "sess-foreign")
				assert.Empty(t, refused)
			} else {
				assert.NotContains(t, purged, "sess-foreign")
				assert.Contains(t, refused, "sess-foreign")
			}
		})
	}
}

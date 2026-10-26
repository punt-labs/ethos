package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/stretchr/testify/require"
)

// Test helpers shared by tests that run on every platform. A test that needs a
// platform feature lives in a file with its own build constraint, and must not
// hold a helper an unconstrained test uses.

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

// writeSealedLive writes a live audit line for sessionID under repoRoot and
// seals it into a tracked chunk, so the session-namespace probe reports the
// file present with nothing unsealed.
func writeSealedLive(t *testing.T, repoRoot, sessionID string) {
	t.Helper()
	live := audit.LiveAuditPath(repoRoot, sessionID)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	body := `{"ts":"` + audit.FormatLineTS(100) + `","session":"` + sessionID + `","tool":"Read"}` + "\n"
	require.NoError(t, os.WriteFile(live, []byte(body), 0o600))

	dir := filepath.Join(audit.SealedSessionsBase(repoRoot), "2026-07-21-"+sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, audit.SessionChunkFile(100, 100)), []byte(body), 0o600))
}

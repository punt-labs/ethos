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

// A roster whose recorded checkout is gone (same repo id) is purged as before,
// with a loss tombstone: its live file went with the checkout.
func TestPurgeTombstoned_DeletedCheckoutIsPurgedWithTombstone(t *testing.T) {
	s := testStore(t)
	a := gitInitDir(t)
	gone := filepath.Join(t.TempDir(), "gone")
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"}
	require.NoError(t, s.CreateInCheckout("sess-gone", root, primary, "", gone, ""))
	// A sealed chunk under the caller's store names the session, so its live
	// file is provably lost rather than never written.
	writeSealedLive(t, a, "sess-gone")
	require.NoError(t, os.Remove(audit.LiveAuditPath(a, "sess-gone")))

	purged, refused, err := s.PurgeTombstoned(a, a, "", false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-gone")
	assert.Empty(t, refused)
}

// A session sealed in a linked worktree whose worktree was then removed is
// purged from the main checkout: the chunks live under the main store.
func TestPurgeTombstoned_RemovedLinkedWorktreePurges(t *testing.T) {
	s := testStore(t)
	main := gitInitDir(t)
	gitRun(t, main, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(t.TempDir(), "wt")
	gitRun(t, main, "worktree", "add", "-q", "--detach", wt)
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"}
	require.NoError(t, s.CreateInCheckout("sess-wt", root, primary, "", wt, ""))
	writeForeignLive(t, wt, "sess-wt", false)
	// seal under the main store, as the real seal path does
	writeForeignLive(t, main, "sess-wt", true)
	require.NoError(t, os.Remove(audit.LiveAuditPath(main, "sess-wt")))
	gitRun(t, main, "worktree", "remove", "--force", wt)

	purged, refused, err := s.PurgeTombstoned(main, main, "", false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-wt")
	assert.Empty(t, refused)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
}

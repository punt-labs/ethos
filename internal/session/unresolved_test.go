package session

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A roster whose repository identity could not be resolved records why in
// repo_unresolved, and records no Repo: an empty Repo means "a checkout with
// no origin", which is a different fact.
func TestCreateUnresolvedRecordsReason(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "u"}
	primary := Participant{AgentID: "9999999", Parent: "u"}
	require.NoError(t, s.CreateUnresolved("sess-un", root, primary, "git did not answer", "/co", "h"))
	r, err := s.Load("sess-un")
	require.NoError(t, err)
	assert.Empty(t, r.Repo)
	assert.Equal(t, "git did not answer", r.RepoUnresolved)
	assert.Equal(t, "/co", r.Checkout)

	require.NoError(t, s.CreateInCheckout("sess-no-origin", root, primary, "", "/co", "h"))
	r, err = s.Load("sess-no-origin")
	require.NoError(t, err)
	assert.Empty(t, r.Repo)
	assert.Empty(t, r.RepoUnresolved, "a no-origin checkout still records Repo \"\" and nothing else")
}

// Purge skips a roster whose repository identity is unresolved, with a notice;
// it is never matched against a no-origin checkout (repoID "").
func TestPurgeSkipsUnresolvedRepoRosters(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // a dead PID: the roster is stale
	require.NoError(t, s.CreateUnresolved("sess-un", root, primary, "git did not answer", t.TempDir(), "h"))
	require.NoError(t, s.CreateInCheckout("sess-origin-less", root, primary, "", t.TempDir(), "h"))

	var purged, refused []string
	var err error
	out := testhelpers.CaptureStderr(t, func() {
		purged, refused, err = s.PurgeTombstoned(t.TempDir(), "", false)
	})
	require.NoError(t, err)
	assert.NotContains(t, purged, "sess-un")
	assert.NotContains(t, refused, "sess-un")
	assert.Contains(t, out, "sess-un")
	assert.Contains(t, out, "git did not answer")
	assert.Contains(t, purged, "sess-origin-less", "a no-origin roster still matches a no-origin checkout")
	ids, err := s.List()
	require.NoError(t, err)
	assert.Contains(t, ids, "sess-un", "the skipped roster is kept")
}

package mission

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppendResult_ProseWithADashIsNotASlug: open questions that read a little
// like a home-directory slug are stored as written, not refused.
func TestAppendResult_ProseWithADashIsNotASlug(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-150")
	require.NoError(t, s.Create(c))
	qs := []string{
		"- add the get-users-list endpoint",
		"- fix the user-home-page view",
		"-v --home-dir x",
		"-- see list-users-api for details",
	}
	r := resultFor(c, VerdictFail)
	r.OpenQuestions = qs
	require.NoError(t, s.AppendResult(c.MissionID, r))
	got, err := s.LoadResults(c.MissionID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, qs, got[0].OpenQuestions)
}

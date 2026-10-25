package ui

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
)

// A blame whose git does not answer yields no lines and says why on stderr:
// the page shows no attribution, and the log shows the timeout.
func TestGitBlameGitTimeout(t *testing.T) {
	testhelpers.HangingGit(t)
	s := &Server{repoRoot: t.TempDir()}
	out := testhelpers.CaptureStderr(t, func() {
		assert.Nil(t, s.gitBlameWithTrailers("a.go"))
	})
	assert.Contains(t, out, "did not answer")
}

package hook

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
)

// ResolveRepo returns "" when git does not answer, and the notice on stderr
// says the git command timed out.
func TestResolveRepoGitTimeout(t *testing.T) {
	testhelpers.HangingGit(t)
	t.Chdir(t.TempDir())
	var got string
	out := captureStderr(t, func() { got = ResolveRepo() })
	assert.Empty(t, got)
	assert.Contains(t, out, "did not answer")
}

package doctor

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A git that does not answer when doctor lists tracked machine-local files is
// an error naming the timeout, not an empty (clean) list.
func TestTrackedLocalExtGitTimeout(t *testing.T) {
	testhelpers.HangingGit(t)
	files, err := trackedLocalExt(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), gitenv.ErrTimeout.Error())
	assert.Empty(t, files)
}

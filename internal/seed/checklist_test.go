package seed

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// brokenFS.Open always fails, simulating a build-broken embed.
type brokenFS struct{}

func (brokenFS) Open(string) (fs.File, error) {
	return nil, errors.New("broken fs")
}

func TestChecklistAgentNames(t *testing.T) {
	t.Run("the real embed resolves the three seeded handles", func(t *testing.T) {
		names, err := ChecklistAgentNames(Agents, "sidecar/agents")
		require.NoError(t, err)
		assert.Contains(t, names, "code-reviewer")
		assert.Contains(t, names, "silent-failure-hunter")
		assert.Contains(t, names, "invariant-completeness-reviewer")
		assert.NotContains(t, names, "README",
			"README.md must not be treated as an exempted agent handle")
	})

	t.Run("a ReadDir failure is surfaced, not swallowed", func(t *testing.T) {
		names, err := ChecklistAgentNames(brokenFS{}, "sidecar/agents")
		require.Error(t, err)
		assert.Nil(t, names)
	})
}

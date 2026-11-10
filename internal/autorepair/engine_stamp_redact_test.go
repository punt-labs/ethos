package autorepair

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A residual's text is redacted before it reaches a stamp: neither a home path
// nor a credential lands in layout.yaml.
func TestEnsureStampResidualIsRedacted(t *testing.T) {
	r := newRig(t, false)
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	r.step("leftovers", ScopeGlobal, Outcome{Residuals: []Residual{{
		Step:   "leftovers",
		ID:     "/Users/alice/.punt-labs/ethos/x.lock",
		Reason: "cannot delete: remove /Users/alice/.punt-labs/ethos/x.lock: token=" + token + " permission denied",
	}}}, nil)
	r.ensure()
	raw, err := os.ReadFile(GlobalStampPath(r.global))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), token)
	assert.NotContains(t, string(raw), "/Users/alice")
	assert.Contains(t, string(raw), "leftovers", "the residual is still recorded")
}

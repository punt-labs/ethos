package autorepair

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A residual a person can act on must clear once they have acted: the doctor
// pass re-runs the step that owns it and rewrites the stamp.
func TestRecheckClearsActionableResidual(t *testing.T) {
	r := newRig(t, true)
	var fixed atomic.Bool
	r.opts.steps = append(r.opts.steps,
		Step{Name: "gitignore", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) {
			if fixed.Load() {
				return Outcome{}, nil
			}
			return Outcome{Residuals: []Residual{actionableResidual("gitignore", ".gitignore: x", "line 3 ignores sessions", "delete line 3")}}, nil
		}},
		Step{Name: "round-decisions", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) {
			return Outcome{Residuals: []Residual{settledResidual("round-decisions", "m-1", "x")}}, nil
		}})
	r.opts.order = []string{"gitignore", "round-decisions"}
	r.ensure()
	rs, err := r.repoStamp()
	require.NoError(t, err)
	require.Len(t, rs.Residual, 2)

	// Without recheck (an ordinary command) nothing re-runs.
	r.ensure()
	rs, _ = r.repoStamp()
	assert.Len(t, rs.Residual, 2)

	fixed.Store(true)
	r.ensure()
	rs, _ = r.repoStamp()
	assert.Len(t, rs.Residual, 2, "an ordinary command does not retry")

	r.opts.recheck = true
	r.ensure()
	rs, err = r.repoStamp()
	require.NoError(t, err)
	require.Len(t, rs.Residual, 1, "the fixed residual is gone, the settled one kept")
	assert.Equal(t, "round-decisions", rs.Residual[0].Step)
}

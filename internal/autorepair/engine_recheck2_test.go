package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (r *rig) writeRepoStamp(s Stamp) {
	r.t.Helper()
	p := filepath.Join(r.store, ".punt-labs", "local", "ethos", StampName)
	require.NoError(r.t, WriteStamp(p, s, func(d string) error { return os.MkdirAll(d, 0o755) }))
}

func stepIDs(rs []Residual) []string {
	var out []string
	for _, x := range rs {
		out = append(out, x.Step+"/"+x.ID)
	}
	return out
}

// A ScopeBoth step that does not name the store of its residual (sessions
// does not) must not have a recheck move the global store's residual into the
// repository stamp or drop it: the stamp that held it keeps it while it holds.
func TestRecheckKeepsGlobalResidualOfBothStoreStep(t *testing.T) {
	r := newRig(t, true)
	held := actionableResidual("both", "roster-1", "unreadable", "end session roster-1")
	r.opts.steps = []Step{
		{Name: "both", Scope: ScopeBoth, Run: func(context.Context, Env) (Outcome, error) {
			return Outcome{Residuals: []Residual{held}}, nil
		}},
		{Name: "repoonly", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) {
			return Outcome{Residuals: []Residual{actionableResidual("repoonly", "x", "bad", "fix x")}}, nil
		}},
	}
	r.opts.order = []string{"both", "repoonly"}
	require.NoError(t, WriteStamp(GlobalStampPath(r.global), Stamp{Version: LayoutVersion, Residual: []Residual{held}}, mkdirPrivate))
	r.writeRepoStamp(Stamp{Version: LayoutVersion, Residual: []Residual{actionableResidual("repoonly", "x", "bad", "fix x")}})

	r.opts.recheck = true
	r.ensure()
	g, err := r.globalStamp()
	require.NoError(t, err)
	assert.Equal(t, []string{"both/roster-1"}, stepIDs(g.Residual), "the condition holds, so the global stamp keeps it")
	rs, err := r.repoStamp()
	require.NoError(t, err)
	assert.Equal(t, []string{"repoonly/x"}, stepIDs(rs.Residual), "and the repository stamp does not gain it")
}

// A stamp from a newer binary is never rewritten, and a residual of a step
// this binary does not carry is kept when its neighbours are rechecked.
func TestRecheckLeavesNewerStampsAndUnknownStepsAlone(t *testing.T) {
	r := newRig(t, false)
	r.step("known", ScopeGlobal, Outcome{}, nil)
	g := Stamp{Version: LayoutVersion + 1, Residual: []Residual{{Class: ClassActionable, Step: "known", ID: "a", Reason: "r", Action: "do"}}}
	require.NoError(t, WriteStamp(GlobalStampPath(r.global), g, mkdirPrivate))
	before, err := os.ReadFile(GlobalStampPath(r.global))
	require.NoError(t, err)
	r.opts.recheck = true
	r.ensure()
	after, err := os.ReadFile(GlobalStampPath(r.global))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a v6 stamp is byte-identical")
	assert.Empty(t, r.ran, "no step runs for a newer stamp")

	g = Stamp{Version: LayoutVersion, Residual: []Residual{
		{Class: ClassActionable, Step: "known", ID: "a", Reason: "r", Action: "do"},
		{Class: ClassActionable, Step: "ghost", ID: "b", Reason: "r", Action: "do"},
	}}
	require.NoError(t, WriteStamp(GlobalStampPath(r.global), g, mkdirPrivate))
	r.ensure()
	got, err := r.globalStamp()
	require.NoError(t, err)
	assert.Equal(t, []string{"ghost/b"}, stepIDs(got.Residual), "known cleared; the step this binary lacks keeps its record")
}

// A residual written before classes existed for an incomplete history may
// have been a damaged reflections file: one re-run re-evaluates and classes it.
func TestRecheckReevaluatesUnclassedIncompleteHistory(t *testing.T) {
	r := newRig(t, true)
	r.opts.steps = []Step{{Name: "round-decisions", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) {
		return Outcome{Residuals: []Residual{actionableResidual("round-decisions", "m-1", "reflections file cannot be read or decoded"+fencedSuffix, "repair or restore reflections.yaml")}}, nil
	}}}
	r.opts.order = []string{"round-decisions"}
	require.NoError(t, WriteStamp(GlobalStampPath(r.global), Stamp{Version: LayoutVersion}, mkdirPrivate))
	r.writeRepoStamp(Stamp{Version: LayoutVersion, Residual: []Residual{{Step: "round-decisions", ID: "m-1", Reason: "incomplete legacy history" + fencedSuffix}}})
	r.opts.recheck = true
	r.ensure()
	rs, err := r.repoStamp()
	require.NoError(t, err)
	require.Len(t, rs.Residual, 1)
	assert.Equal(t, ClassActionable, rs.Residual[0].Class)
	assert.NotEmpty(t, rs.Residual[0].Action)
}

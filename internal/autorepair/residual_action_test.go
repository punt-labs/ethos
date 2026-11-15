package autorepair

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An actionable residual read from a stamp written before actions existed
// still names something to do.
func TestEffectiveActionFallsBackForOldActionable(t *testing.T) {
	old := Residual{Step: "gitignore", ID: "x", Reason: "r"}
	assert.Equal(t, "cause recorded in ~/.punt-labs/ethos/logs/repair.log for step gitignore; clears when the condition no longer holds", old.EffectiveAction())
	assert.Equal(t, "do it", Residual{Class: ClassActionable, Step: "s", Action: "do it"}.EffectiveAction())
	assert.Empty(t, Residual{Class: ClassSettled, Step: "s"}.EffectiveAction(), "a settled residual has no action")
}

// The action names files, so it is redacted like the reason before it reaches
// a stamp or a screen.
func TestEnsureStampActionIsRedacted(t *testing.T) {
	r := newRig(t, false)
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	res := actionableResidual("leftovers", "x", "cannot delete", "delete /Users/alice/.punt-labs/ethos/"+token+".lock by hand")
	r.step("leftovers", ScopeGlobal, Outcome{Residuals: []Residual{res}}, nil)
	r.ensure()
	raw, err := os.ReadFile(GlobalStampPath(r.global))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), token)
	assert.NotContains(t, string(raw), "/Users/alice")
	assert.Contains(t, string(raw), "by hand")
	shown := Residual{Class: ClassActionable, Action: res.Action}.DisplayAction()
	assert.NotContains(t, shown, token, "display redacts too")
	assert.NotContains(t, shown, "/Users/alice")
}

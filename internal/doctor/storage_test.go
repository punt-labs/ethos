package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

func writeGlobalStamp(t *testing.T, global string, s autorepair.Stamp) {
	t.Helper()
	require.NoError(t, autorepair.WriteStamp(autorepair.GlobalStampPath(global), s, func(d string) error { return os.MkdirAll(d, 0o700) }))
}

func TestCheckStorage(t *testing.T) {
	phys := func(t *testing.T) string {
		d, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		return d
	}
	t.Run("no stamp warns", func(t *testing.T) {
		r := CheckStorage(resolve.Located{}, filepath.Join(phys(t), "g"))
		assert.Equal(t, "Storage", r.Name)
		assert.Equal(t, "WARN", r.Status)
		assert.Contains(t, r.Detail, "no layout stamp")
	})
	t.Run("current passes", func(t *testing.T) {
		g := filepath.Join(phys(t), "g")
		writeGlobalStamp(t, g, autorepair.Stamp{Version: autorepair.LayoutVersion, RepairedAt: "2026-11-09T10:00:00Z", Items: 4})
		r := CheckStorage(resolve.Located{}, g)
		assert.Equal(t, "PASS", r.Status)
		assert.Equal(t, "layout v5 (upgraded 2026-11-09T10:00:00Z, 4 items)", r.Detail)
	})
	t.Run("residuals are not a pass", func(t *testing.T) {
		g := filepath.Join(phys(t), "g")
		writeGlobalStamp(t, g, autorepair.Stamp{Version: autorepair.LayoutVersion, Residual: []autorepair.Residual{
			{Step: "round-decisions", ID: "m-1", Reason: "incomplete history"},
			{Step: "global-audit", ID: "s-1", Reason: "unreadable"},
		}})
		r := CheckStorage(resolve.Located{}, g)
		assert.Equal(t, "WARN", r.Status)
		assert.Contains(t, r.Detail, "2 condition(s) need action")
	})
	t.Run("old stamp warns", func(t *testing.T) {
		g := filepath.Join(phys(t), "g")
		writeGlobalStamp(t, g, autorepair.Stamp{Version: 4})
		assert.Equal(t, "WARN", CheckStorage(resolve.Located{}, g).Status)
	})
	t.Run("repository stamp missing warns", func(t *testing.T) {
		g := filepath.Join(phys(t), "g")
		writeGlobalStamp(t, g, autorepair.Stamp{Version: autorepair.LayoutVersion})
		store := phys(t)
		r := CheckStorage(resolve.Located{Store: store, Checkout: store}, g)
		assert.Equal(t, "WARN", r.Status)
		assert.Contains(t, r.Detail, "repository")
	})
	t.Run("repository residuals count", func(t *testing.T) {
		g := filepath.Join(phys(t), "g")
		writeGlobalStamp(t, g, autorepair.Stamp{Version: autorepair.LayoutVersion, Items: 1})
		store := phys(t)
		rs := autorepair.Stamp{Version: autorepair.LayoutVersion, Items: 2, Residual: []autorepair.Residual{{Step: "x", ID: "y", Reason: "z"}}}
		path := filepath.Join(store, ".punt-labs", "local", "ethos", autorepair.StampName)
		require.NoError(t, autorepair.WriteStamp(path, rs, func(d string) error { return os.MkdirAll(d, 0o700) }))
		r := CheckStorage(resolve.Located{Store: store, Checkout: store}, g)
		assert.Equal(t, "WARN", r.Status)
		assert.Contains(t, r.Detail, "1 condition(s) need action")
	})
	t.Run("repository current sums items", func(t *testing.T) {
		g := filepath.Join(phys(t), "g")
		writeGlobalStamp(t, g, autorepair.Stamp{Version: autorepair.LayoutVersion, RepairedAt: "2026-11-09T10:00:00Z", Items: 1})
		store := phys(t)
		rs := autorepair.Stamp{Version: autorepair.LayoutVersion, RepairedAt: "2026-11-09T10:00:01Z", Items: 2}
		path := filepath.Join(store, ".punt-labs", "local", "ethos", autorepair.StampName)
		require.NoError(t, autorepair.WriteStamp(path, rs, func(d string) error { return os.MkdirAll(d, 0o700) }))
		r := CheckStorage(resolve.Located{Store: store, Checkout: store}, g)
		assert.Equal(t, "PASS", r.Status)
		assert.Contains(t, r.Detail, "3 items")
	})
}

// Windows is not a shipped target: Ensure compiles and does nothing there, so
// the row must not promise that the next command repairs anything.
func TestCheckStorageOnWindowsSaysNotSupported(t *testing.T) {
	old := storageGOOS
	storageGOOS = "windows"
	t.Cleanup(func() { storageGOOS = old })
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	r := CheckStorage(resolve.Located{}, filepath.Join(d, "g"))
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, "not supported")
	assert.NotContains(t, r.Detail, "repairs it")
}

// The row states what it found and points at the log; it never promises what a
// later command will do, because a step may defer.
func TestCheckStorageMessagesPromiseNothing(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	missing := CheckStorage(resolve.Located{}, filepath.Join(d, "g"))
	assert.Equal(t, "no layout stamp for the global store; see ~/.punt-labs/ethos/logs/ethos.log", missing.Detail)

	g := filepath.Join(d, "old")
	writeGlobalStamp(t, g, autorepair.Stamp{Version: 4})
	old := CheckStorage(resolve.Located{}, g)
	assert.Equal(t, "the global store is at layout v4, not v5; see ~/.punt-labs/ethos/logs/ethos.log", old.Detail)
}

// A repository whose only residuals are the final state of pre-v4.21 mission
// history must read PASS: WARN is for a condition a person can act on.
func TestCheckStorageSettledResidualsPass(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	g := filepath.Join(d, "g")
	writeGlobalStamp(t, g, autorepair.Stamp{Version: autorepair.LayoutVersion, Residual: []autorepair.Residual{
		{Step: "round-decisions", ID: "m-1", Reason: "incomplete legacy history, mission fenced from lifecycle writes"},
		{Step: "round-decisions", ID: "m-2", Reason: "incomplete legacy history, mission fenced from lifecycle writes"},
	}})
	r := CheckStorage(resolve.Located{}, g)
	assert.Equal(t, "PASS", r.Status)
	assert.Contains(t, r.Detail, "2 legacy missions fenced, no action needed")
}

func TestCheckStorageActionableWarnsWithAction(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	g := filepath.Join(d, "g")
	writeGlobalStamp(t, g, autorepair.Stamp{Version: autorepair.LayoutVersion, Residual: []autorepair.Residual{
		{Step: "round-decisions", ID: "m-1", Class: autorepair.ClassSettled, Reason: "incomplete legacy history, mission fenced from lifecycle writes"},
		{Step: "gitignore", ID: ".gitignore: .punt-labs/ethos/sessions", Class: autorepair.ClassActionable, Reason: ".gitignore line 7 ignores the sessions record directory; the line is safe to delete", Action: "delete line 7 of .gitignore"},
	}})
	r := CheckStorage(resolve.Located{}, g)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, "1 condition(s) need action")
	assert.Contains(t, r.Detail, ".gitignore line 7")
	assert.Contains(t, r.Detail, "safe to delete")
	assert.Contains(t, r.Detail, "delete line 7 of .gitignore", "the action is in the WARN")
	require.Len(t, r.Lines, 2)
	assert.True(t, strings.HasPrefix(r.Lines[0], "action needed: "))
	assert.True(t, strings.HasPrefix(r.Lines[1], "settled: "))
}

// A stamp written by an older binary has no class; the known fenced reason
// must read as settled so existing repositories pass without a re-repair.
func TestCheckStorageOldStampFencedPasses(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	g := filepath.Join(d, "g")
	raw := "version: 5\nresidual:\n- step: round-decisions\n  id: m-1\n  reason: incomplete legacy history, mission fenced from lifecycle writes\n"
	require.NoError(t, os.MkdirAll(g, 0o700))
	require.NoError(t, os.WriteFile(autorepair.GlobalStampPath(g), []byte(raw), 0o600))
	r := CheckStorage(resolve.Located{}, g)
	assert.Equal(t, "PASS", r.Status)
	assert.Contains(t, r.Detail, "1 legacy missions fenced, no action needed")
}

func TestResidualEffectiveClass(t *testing.T) {
	for _, c := range []struct {
		name string
		r    autorepair.Residual
		want autorepair.Class
	}{
		{"explicit wins", autorepair.Residual{Class: autorepair.ClassActionable, Step: "round-decisions", Reason: "x, mission fenced from lifecycle writes"}, autorepair.ClassActionable},
		{"old fenced", autorepair.Residual{Step: "round-decisions", Reason: "legacy evidence refused by redaction, mission fenced from lifecycle writes"}, autorepair.ClassSettled},
		{"old imported", autorepair.Residual{Step: "global-missions", Reason: "imported; global copy kept"}, autorepair.ClassSettled},
		{"old unloadable", autorepair.Residual{Step: "round-decisions", Reason: "contract cannot be loaded, mission fenced from lifecycle writes"}, autorepair.ClassActionable},
		{"old write failure", autorepair.Residual{Step: "round-decisions", Reason: "mission authority cannot be written, mission fenced from lifecycle writes"}, autorepair.ClassActionable},
		{"old other step", autorepair.Residual{Step: "sessions", Reason: "x, mission fenced from lifecycle writes"}, autorepair.ClassActionable},
		{"old unknown", autorepair.Residual{Step: "gitignore", Reason: "z"}, autorepair.ClassActionable},
	} {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, c.r.EffectiveClass()) })
	}
}

// A repository whose layout cannot be resolved must not PASS on the global
// stamp alone: its own stamp was not checked.
func TestCheckStorageLayoutErrorWarns(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	g := filepath.Join(d, "g")
	writeGlobalStamp(t, g, autorepair.Stamp{Version: autorepair.LayoutVersion})
	store := t.TempDir()
	loc := resolve.Located{Store: store, Checkout: store, ConfigErr: errors.New("record: must be blame or full")}
	r := CheckStorage(loc, g)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, "repository layout stamp not checked")
	assert.Contains(t, r.Detail, "record: must be blame or full")
}

// Every actionable residual names its action in the WARN; none hides behind a
// count.
func TestCheckStorageWarnNamesEveryAction(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	g := filepath.Join(d, "g")
	var rs []autorepair.Residual
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		rs = append(rs, autorepair.Residual{Step: "gitignore", ID: id, Class: autorepair.ClassActionable, Reason: "reason-" + id, Action: "action-" + id})
	}
	writeGlobalStamp(t, g, autorepair.Stamp{Version: autorepair.LayoutVersion, Residual: rs})
	r := CheckStorage(resolve.Located{}, g)
	assert.Equal(t, "WARN", r.Status)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		assert.Contains(t, r.Detail, "reason-"+id)
		assert.Contains(t, r.Detail, "action-"+id)
	}
	assert.NotContains(t, r.Detail, "and 2 more")
}

// An old actionable residual with no action still names one.
func TestCheckStorageOldActionableGetsFallbackAction(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	g := filepath.Join(d, "g")
	raw := "version: 5\nresidual:\n- step: gitignore\n  id: x\n  reason: old thing\n"
	require.NoError(t, os.MkdirAll(g, 0o700))
	require.NoError(t, os.WriteFile(autorepair.GlobalStampPath(g), []byte(raw), 0o600))
	r := CheckStorage(resolve.Located{}, g)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, "repair.log for step gitignore; clears when the condition no longer holds")
}

// A credential-shaped name in an action does not reach the screen even from a
// stamp another tool wrote.
func TestCheckStorageRedactsAction(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	g := filepath.Join(d, "g")
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	raw := "version: 5\nresidual:\n- class: actionable\n  step: leftovers\n  id: x\n  reason: r\n  action: delete " + token + ".lock\n"
	require.NoError(t, os.MkdirAll(g, 0o700))
	require.NoError(t, os.WriteFile(autorepair.GlobalStampPath(g), []byte(raw), 0o600))
	r := CheckStorage(resolve.Located{}, g)
	assert.NotContains(t, r.Detail, token)
	for _, l := range r.Lines {
		assert.NotContains(t, l, token)
	}
}

// A repository-layout WARN still lists the global stamp's residuals under
// --verbose.
func TestCheckStorageLayoutWarnKeepsGlobalLines(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	g := filepath.Join(d, "g")
	writeGlobalStamp(t, g, autorepair.Stamp{Version: autorepair.LayoutVersion, Residual: []autorepair.Residual{
		{Step: "gitignore", ID: "x", Class: autorepair.ClassActionable, Reason: "r", Action: "do x"},
	}})
	store := t.TempDir()
	loc := resolve.Located{Store: store, Checkout: store, ConfigErr: errors.New("record: must be blame or full")}
	r := CheckStorage(loc, g)
	assert.Equal(t, "WARN", r.Status)
	require.Len(t, r.Lines, 1)
	assert.Contains(t, r.Lines[0], "do x")
}

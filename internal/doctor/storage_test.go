package doctor

import (
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
	assert.Equal(t, "no layout stamp for the global store; see ~/.punt-labs/ethos/logs/repair.log", missing.Detail)

	g := filepath.Join(d, "old")
	writeGlobalStamp(t, g, autorepair.Stamp{Version: 4})
	old := CheckStorage(resolve.Located{}, g)
	assert.Equal(t, "the global store is at layout v4, not v5; see ~/.punt-labs/ethos/logs/repair.log", old.Detail)
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
		{Step: "gitignore", ID: ".gitignore: .punt-labs/ethos/sessions", Class: autorepair.ClassActionable, Reason: ".gitignore line 7 ignores the sessions record directory; the line is safe to delete"},
	}})
	r := CheckStorage(resolve.Located{}, g)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, "1 condition(s) need action")
	assert.Contains(t, r.Detail, ".gitignore line 7")
	assert.Contains(t, r.Detail, "safe to delete")
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
		{"old unloadable", autorepair.Residual{Step: "round-decisions", Reason: "contract cannot be loaded, mission fenced from lifecycle writes"}, autorepair.ClassActionable},
		{"old write failure", autorepair.Residual{Step: "round-decisions", Reason: "mission authority cannot be written, mission fenced from lifecycle writes"}, autorepair.ClassActionable},
		{"old other step", autorepair.Residual{Step: "sessions", Reason: "x, mission fenced from lifecycle writes"}, autorepair.ClassActionable},
		{"old unknown", autorepair.Residual{Step: "gitignore", Reason: "z"}, autorepair.ClassActionable},
	} {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, c.r.EffectiveClass()) })
	}
}

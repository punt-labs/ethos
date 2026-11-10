package doctor

import (
	"os"
	"path/filepath"
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
		assert.Contains(t, r.Detail, "2 residual")
		assert.Contains(t, r.Detail, "repair.log")
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
		assert.Contains(t, r.Detail, "1 residual")
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

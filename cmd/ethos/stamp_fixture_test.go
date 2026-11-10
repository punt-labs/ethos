//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// stampFixture marks the fixture HOME, and the repository at repo when it has
// used ethos, as already upgraded, so the storage upgrade every command runs
// first converts nothing and writes nothing: the state the test plants is what
// the command under test sees. Call it before planting that state. A repo
// with no .punt-labs/ is one the upgrade never serves, and stamping it would
// create the very tree a fresh-repo test asserts absent, so it is left alone.
func stampFixture(t *testing.T, home, repo string) {
	t.Helper()
	mk := func(dir string) error { return os.MkdirAll(dir, 0o700) }
	global := filepath.Join(home, ".punt-labs", "ethos")
	require.NoError(t, autorepair.WriteStamp(autorepair.GlobalStampPath(global), autorepair.Stamp{Version: autorepair.LayoutVersion}, mk))
	if _, err := os.Stat(filepath.Join(repo, ".punt-labs")); err != nil {
		return
	}
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	stampCurrent(t, p)
}

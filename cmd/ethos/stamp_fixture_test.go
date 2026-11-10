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

// stampFixture marks both the fixture HOME and the repository at repo as
// already upgraded, so the storage upgrade every command runs first converts
// nothing and writes nothing: the state the test plants is what the command
// under test sees. Call it before planting that state.
func stampFixture(t *testing.T, home, repo string) {
	t.Helper()
	mk := func(dir string) error { return os.MkdirAll(dir, 0o700) }
	global := filepath.Join(home, ".punt-labs", "ethos")
	require.NoError(t, autorepair.WriteStamp(autorepair.GlobalStampPath(global), autorepair.Stamp{Version: autorepair.LayoutVersion}, mk))
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	stampCurrent(t, p)
}

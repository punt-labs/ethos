//go:build linux || darwin

package repair

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A busy lock defers its own items and leaves every other item to be removed.
func TestApplySkipsBusyLockAndDeletesTheRest(t *testing.T) {
	p := fixture(t)
	busyTrace := filepath.Join(p.RecordRoot(), "missions", "m-busy", "log.jsonl")
	freeTrace := filepath.Join(p.RecordRoot(), "missions", "m-free", "log.jsonl")
	for _, id := range []string{"m-busy", "m-free"} {
		put(t, filepath.Join(p.RecordRoot(), "missions", id, "contract.yaml"), "mission_id: "+id+"\n")
	}
	put(t, busyTrace, "obsolete")
	put(t, freeTrace, "obsolete")
	lock := filepath.Join(p.RecordRoot(), "missions", "m-busy", ".lock")
	put(t, lock, "")
	f, err := os.OpenFile(lock, os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))

	plan, err := Inventory(context.Background(), p)
	require.NoError(t, err)
	removed, err := plan.Apply(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{freeTrace}, removed)
	assert.ElementsMatch(t, []string{busyTrace, lock}, plan.Skipped)
	assert.FileExists(t, busyTrace)
	assert.FileExists(t, lock)
	assert.NoFileExists(t, freeTrace)
}

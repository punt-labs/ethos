package autorepair

import (
	"os"

	"github.com/punt-labs/ethos/v4/internal/repair"
)

// lockHeld reports whether another process holds the lock file at path. It
// opens the existing file (never creating one), tries the lock without
// waiting, and releases it by closing. It writes nothing.
func lockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	return repair.TryLock(f) != nil, nil
}

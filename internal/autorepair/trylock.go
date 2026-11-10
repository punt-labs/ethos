package autorepair

import (
	"errors"
	"time"
)

// TryGlobalLock takes the per-user repair lock under the global store root for
// at most wait. ok is false when another holder kept it for the whole wait, in
// which case release is nil. A caller that writes the repair log or retires
// session state outside the engine holds this lock, because log rotation and
// the purge both assume a single writer.
func TryGlobalLock(global string, wait time.Duration) (release func(), ok bool, err error) {
	if !supported {
		return nil, false, nil
	}
	release, err = acquire(globalLockPath(global), wait)
	if errors.Is(err, errBusy) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return release, true, nil
}

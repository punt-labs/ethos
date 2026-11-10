package autorepair

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// errBusy means the lock stayed held for the whole wait.
var errBusy = errors.New("lock busy")

// globalLockPath is the per-user repair lock under the global store, named by
// the SHA-256 of the store root as internal/claudemd names its locks, so it
// does not depend on TMPDIR.
func globalLockPath(global string) string {
	sum := sha256.Sum256([]byte("autorepair\x00" + global))
	return filepath.Join(global, "locks", hex.EncodeToString(sum[:])+".lock")
}

// acquire takes the exclusive lock at path, creating its directory, and
// returns the release function. It polls for at most wait and returns
// errBusy if the lock is still held then.
func acquire(path string, wait time.Duration) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating lock directory %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", path, err)
	}
	deadline := time.Now().Add(wait)
	for {
		ok, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if ok {
			return func() { _ = unlock(f); _ = f.Close() }, nil
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, errBusy
		}
		time.Sleep(10 * time.Millisecond)
	}
}

//go:build !windows

package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// counterDateFormat is the per-day suffix on the counter file.
const counterDateFormat = "2006-01-02"

// NewID generates a new mission ID for the given date and root directory.
//
// Format: m-YYYY-MM-DD-NNN where NNN is a zero-padded 3-digit counter.
// The counter is per-day and persisted at <root>/missions/.counter-YYYY-MM-DD.
// Concurrent NewID calls are serialized via flock on a separate stable
// lock file (<root>/missions/.counter-YYYY-MM-DD.lock), not on the
// counter file itself — so the counter file can be replaced atomically
// via temp+rename without racing a concurrent flock acquirer on the
// unlinked inode.
//
// Two callers on the same day produce strictly distinct IDs (NNN-1 and NNN).
// Two callers on different days each start at 001.
//
// Atomicity: the counter update is a read-modify-write under the lock,
// persisted via WriteFile(tmp) + Rename(tmp, counter). A partial write
// during the temp write leaves the counter file unchanged; a rename is
// atomic on POSIX. There is no transient empty-file window (which would
// otherwise make the next caller reset to 1 and collide with an existing
// mission 001).
func NewID(root string, now time.Time) (string, error) {
	missionsDir := filepath.Join(root, "missions")
	if err := os.MkdirAll(missionsDir, 0o700); err != nil {
		return "", fmt.Errorf("creating missions directory: %w", err)
	}

	day := now.UTC().Format(counterDateFormat)
	counterPath := filepath.Join(missionsDir, ".counter-"+day)
	lockPath := counterPath + ".lock"

	// Acquire the flock on a stable file that is never renamed or
	// unlinked by NewID. Locking the counter file directly would race
	// with the temp+rename pattern below: a concurrent caller could
	// open the post-rename counter file, get a fresh unlocked inode,
	// and re-lock it while this call still holds the lock on the
	// pre-rename (now unlinked) inode.
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("opening counter lock file: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return "", fmt.Errorf("acquiring counter lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	// Read the current counter value. Missing file is treated as 0
	// (first caller of the day).
	data, err := os.ReadFile(counterPath)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("reading counter file: %w", err)
	}
	current := 0
	if s := strings.TrimSpace(string(data)); s != "" {
		v, parseErr := strconv.Atoi(s)
		if parseErr != nil {
			return "", fmt.Errorf("parsing counter file %q: %w", counterPath, parseErr)
		}
		current = v
	}
	next := current + 1
	// Bound to [1, 999] so the resulting m-YYYY-MM-DD-NNN ID always
	// matches the schema regex. Detects both daily exhaustion and
	// poisoned counter files (negative or huge values written by an
	// attacker with local write access to the missions dir).
	if next < 1 || next > 999 {
		return "", fmt.Errorf(
			"mission ID counter for %s is %d, outside valid range [1, 999]",
			day, next,
		)
	}

	// Atomic write via temp + rename. A partial write inside WriteFile
	// leaves the counter file unchanged; os.Rename is atomic on POSIX.
	// No transient empty-file state — the next caller either sees the
	// old value (if this call failed before rename) or the new value
	// (if rename committed).
	tmp := counterPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d\n", next)), 0o600); err != nil {
		return "", fmt.Errorf("writing temp counter: %w", err)
	}
	if err := os.Rename(tmp, counterPath); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("renaming counter file: %w", err)
	}

	return fmt.Sprintf("m-%s-%03d", day, next), nil
}

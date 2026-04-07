//go:build !windows

package mission

import (
	"fmt"
	"io"
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
// Concurrent NewID calls are serialized via flock — the counter file is the
// lock target as well as the persistence target.
//
// Two callers on the same day produce strictly distinct IDs (NNN-1 and NNN).
// Two callers on different days each start at 001.
func NewID(root string, now time.Time) (string, error) {
	missionsDir := filepath.Join(root, "missions")
	if err := os.MkdirAll(missionsDir, 0o700); err != nil {
		return "", fmt.Errorf("creating missions directory: %w", err)
	}

	day := now.UTC().Format(counterDateFormat)
	counterPath := filepath.Join(missionsDir, ".counter-"+day)

	// Open with O_RDWR|O_CREATE so we can both read the prior counter and
	// rewrite it under the same lock.
	f, err := os.OpenFile(counterPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return "", fmt.Errorf("opening counter file: %w", err)
	}
	defer f.Close()

	// Acquire an exclusive flock on the counter file. The OS releases
	// the lock when the file descriptor closes; the explicit LOCK_UN
	// defer makes the lock/unlock pair symmetric for readers of the
	// code.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return "", fmt.Errorf("acquiring counter lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	// Read whatever is currently in the file.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seeking counter file: %w", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("reading counter file: %w", err)
	}

	current := 0
	if s := strings.TrimSpace(string(data)); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			return "", fmt.Errorf("parsing counter file %q: %w", counterPath, err)
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

	// Truncate and rewrite under the same lock.
	if err := f.Truncate(0); err != nil {
		return "", fmt.Errorf("truncating counter file: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seeking counter file: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%d\n", next); err != nil {
		return "", fmt.Errorf("writing counter file: %w", err)
	}

	return fmt.Sprintf("m-%s-%03d", day, next), nil
}

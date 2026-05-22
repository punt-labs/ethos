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

// NamespaceMissions and NamespaceDelegations are the two ID namespaces
// the counters/ directory carries today. Each namespace owns a sibling
// per-date counter file — adding a new namespace adds a new file and
// touches no existing one (DES-054 invariant I9-counter).
const (
	NamespaceMissions    = "missions"
	NamespaceDelegations = "delegations"
)

// defaultCounterRoot is the production root for the counters/ tree.
// Tests use NewIDAt with t.TempDir() so production state is never
// touched.
func defaultCounterRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Falling back to "." would race other test packages and
		// hide configuration bugs in production. An empty string
		// surfaces the failure at counterPath construction time.
		return ""
	}
	return filepath.Join(home, ".punt-labs", "ethos")
}

// NewID allocates the next ID for the given namespace and date.
//
// The counter file lives at
// <defaultCounterRoot>/counters/<namespace>-YYYY-MM-DD as a single
// integer, flock-guarded. New namespaces add sibling files; existing
// files never change shape (DES-054 I9-counter).
//
// The returned release function is the rollback API. The caller MUST
// invoke it exactly once — typically `defer release(committed)`. A
// commit=true call is a no-op (the increment already happened);
// commit=false decrements the counter so the allocated ID is not
// permanently burned. Idempotent: a second call does nothing.
//
// IDs are formatted as m-YYYY-MM-DD-NNN for the missions namespace
// and d-YYYY-MM-DD-NNN for delegations. Other namespaces use the
// generic <namespace>-YYYY-MM-DD-NNN shape.
//
// Concurrency: the read-modify-write is serialized through a flock
// on a separate stable lock file (<root>/counters/<namespace>-DATE.lock).
// Locking the counter file itself would race the temp+rename pattern
// since the post-rename file lives on a different inode.
func NewID(namespace string, now time.Time) (string, func(commit bool), error) {
	return NewIDAt(defaultCounterRoot(), namespace, now)
}

// NewIDAt is the testable form of NewID: the caller supplies the root
// directory under which counters/ lives. Production code uses NewID,
// which substitutes ~/.punt-labs/ethos/ automatically. Tests pass
// t.TempDir() so concurrent test runs do not collide on shared state.
func NewIDAt(counterRoot, namespace string, now time.Time) (string, func(commit bool), error) {
	if strings.TrimSpace(counterRoot) == "" {
		return "", noopRelease, fmt.Errorf("counter root is required")
	}
	if err := validateNamespace(namespace); err != nil {
		return "", noopRelease, err
	}
	countersDir := filepath.Join(counterRoot, "counters")
	if err := os.MkdirAll(countersDir, 0o700); err != nil {
		return "", noopRelease, fmt.Errorf("creating counters directory: %w", err)
	}

	day := now.UTC().Format(counterDateFormat)
	counterPath := filepath.Join(countersDir, namespace+"-"+day)
	lockPath := counterPath + ".lock"

	next, err := allocateCounter(counterPath, lockPath)
	if err != nil {
		return "", noopRelease, err
	}

	id := formatID(namespace, day, next)
	rel := newReleaseFunc(counterPath, lockPath, next)
	return id, rel, nil
}

// allocateCounter performs the locked read-modify-write of the
// per-date counter file. Returns the newly-allocated number.
func allocateCounter(counterPath, lockPath string) (int, error) {
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, fmt.Errorf("opening counter lock file: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return 0, fmt.Errorf("acquiring counter lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	current, err := readCounter(counterPath)
	if err != nil {
		return 0, err
	}
	next := current + 1
	if next < 1 || next > 999 {
		return 0, fmt.Errorf(
			"id counter at %q is %d, outside valid range [1, 999]",
			counterPath, next,
		)
	}
	if err := writeCounterAtomic(counterPath, next); err != nil {
		return 0, err
	}
	return next, nil
}

// readCounter returns the current value from a counter file. Missing
// file is treated as 0 (first caller of the day).
func readCounter(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading counter file %q: %w", path, err)
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parsing counter file %q: %w", path, err)
	}
	return v, nil
}

// writeCounterAtomic rewrites the counter file via temp+rename so a
// partial write leaves the file unchanged.
func writeCounterAtomic(path string, value int) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d\n", value)), 0o600); err != nil {
		return fmt.Errorf("writing temp counter: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming counter file: %w", err)
	}
	return nil
}

// newReleaseFunc builds the rollback closure for a freshly-allocated
// counter value. The caller invokes it once via `defer
// release(committed)`. On commit=true the function is a no-op (the
// allocation persists). On commit=false the counter is decremented
// back to its pre-allocation value, but only if a concurrent caller
// has not already advanced past `allocated` — in that case the
// release is idempotent and leaves the higher value alone.
func newReleaseFunc(counterPath, lockPath string, allocated int) func(commit bool) {
	called := false
	return func(commit bool) {
		if called {
			return
		}
		called = true
		if commit {
			return
		}
		// Best-effort rollback. A failure to decrement is non-fatal —
		// the worst case is one burned ID, not a correctness problem.
		// Errors go to stderr so an operator running with -v sees the
		// drift, but a programmatic caller still sees its primary
		// error path unchanged.
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: id release: opening lock %q: %v\n", lockPath, err)
			return
		}
		defer lockFile.Close()
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: id release: acquiring lock %q: %v\n", lockPath, err)
			return
		}
		defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

		current, err := readCounter(counterPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: id release: reading counter %q: %v\n", counterPath, err)
			return
		}
		// Idempotency: only decrement when the on-disk value is still
		// the one we allocated. A concurrent caller may have advanced
		// past it — in which case rolling back here would leave the
		// counter pointing at an ID someone else already returned.
		if current != allocated {
			return
		}
		if err := writeCounterAtomic(counterPath, allocated-1); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: id release: writing counter %q: %v\n", counterPath, err)
		}
	}
}

// noopRelease is the release returned on error paths so callers can
// `defer release(false)` unconditionally without nil-checking.
func noopRelease(bool) {}

// validateNamespace refuses values that would let a caller inject
// path segments into the counter filename or collide with the lock
// suffix. Namespace is part of a filename, not user-facing text.
func validateNamespace(ns string) error {
	if ns == "" {
		return fmt.Errorf("namespace is required")
	}
	for _, r := range ns {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return fmt.Errorf("namespace %q: only lowercase letters, digits, and hyphens allowed", ns)
		}
	}
	return nil
}

// formatID renders a namespace + date + sequence into the canonical
// ID shape. Missions and delegations get short prefixes (m-/d-) per
// DES-054; other namespaces fall back to the generic shape.
func formatID(namespace, day string, seq int) string {
	switch namespace {
	case NamespaceMissions:
		return fmt.Sprintf("m-%s-%03d", day, seq)
	case NamespaceDelegations:
		return fmt.Sprintf("d-%s-%03d", day, seq)
	}
	return fmt.Sprintf("%s-%s-%03d", namespace, day, seq)
}

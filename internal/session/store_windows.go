//go:build windows

package session

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// wholeFile is the byte range LockFileEx locks when neither bound is
// meaningful to us — the whole file, regardless of its actual size. This is
// the standard idiom (also used by e.g. gofrs/flock): LockFileEx takes the
// range to lock as a 64-bit value split across bytesLow/bytesHigh, and
// 0xFFFFFFFF in both covers any file up to 16 exabytes.
const wholeFile = 0xFFFFFFFF

// withLock executes fn while holding an exclusive, whole-file lock on the
// session's lock file. Windows has no flock; LockFileEx is the equivalent,
// and the way to make it a synchronous, blocking call (rather than
// requiring a completion port or event) is this exact idiom: a zeroed
// OVERLAPPED struct on a handle that was NOT opened with
// FILE_FLAG_OVERLAPPED. See store.go's doc comment on withLock/
// isProcessAlive for the re-entrancy warning shared with the Unix
// implementation.
func (s *Store) withLock(sessionID string, fn func() error) error {
	if err := os.MkdirAll(s.sessionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating sessions directory: %w", err)
	}
	lockFile := s.lockPath(sessionID)
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer f.Close()

	h := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, wholeFile, wholeFile, &overlapped); err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer windows.UnlockFileEx(h, 0, wholeFile, wholeFile, &overlapped)

	return fn()
}

// isProcessAlive checks whether a process with the given PID is running.
// Calls OpenProcess directly — rather than os.FindProcess, which wraps the
// same call but obscures the errno — so ERROR_ACCESS_DENIED (a live
// process this caller lacks permission to open, e.g. one owned by another
// user) can be told apart from ERROR_INVALID_PARAMETER (what OpenProcess
// returns for a PID that does not exist at all).
//
// This distinction is not cosmetic: isProcessAlive gates PurgeCurrent's
// removal of a session pointer file, the guard against purging a LIVE
// session's pointer out from under it. Treating access-denied as "dead"
// would open that guard for a process that is very much alive — ethos-
// vqwn's failure class (a session silently loses its identity) arriving
// through a new door on a new platform. The two wrong answers are not
// symmetric in cost: a false "dead" purges a live pointer with no
// recovery; a false "alive" only leaves a stale pointer that self-heals
// at the next SessionStart. Fail closed on that asymmetry — every error
// OTHER than the confirmed-not-found signal is treated as alive.
//
// Limitations: a PID whose process has just exited can briefly still open
// successfully if another handle to it is still held elsewhere (the
// kernel object outlives the last process exit until every handle
// closes) — the same "recently dead but still answers" caveat the Unix
// zombie case carries, just via a different mechanism.
func isProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == nil {
		_ = windows.CloseHandle(h)
		return true
	}
	return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
}

//go:build !windows

package mission

import "os"

// syncDir fsyncs dir so a preceding rename's directory-entry update is
// durable, not merely the renamed file's own contents (F2, PR #508
// round 2 review of ethos-ouy9). POSIX fsync(2) on a directory file
// descriptor is the standard mechanism for this; os.Open succeeds on a
// directory (read-only, which is all Sync needs) on every platform
// this build tag covers.
//
// A package-level var, not a plain func, so
// TestWriteContractFile_SyncDirFailureIsWarnedNotErrored can inject a
// failure deterministically — a real fsync failure on a directory is
// not something a portable test can otherwise engineer. The name
// reflects the current contract (PR #508 round 3, G2/G3): the rename
// this follows is the commit point, so a syncDir failure is warned to
// stderr, not returned as an error — dest already holds the correct,
// complete contract regardless of what syncDir reports.
var syncDir = func(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		// Sync()'s error is the one that actually means something:
		// fsync failed, so the directory-entry update is unconfirmed.
		// Prefer it over closeErr even when both fire.
		return syncErr
	}
	// Defensive, not load-bearing: f is a read-only (O_RDONLY) fd with
	// nothing buffered, so there is no delayed-write data for Close to
	// flush and no durability signal for it to carry — unlike the
	// buffered-write case Copilot's rationale describes elsewhere, a
	// Close error here means something went wrong with the fd itself
	// (EBADF, EINTR), not that data failed to reach disk. Checked
	// anyway because it costs nothing and removes a recurring review
	// question, but do not read this as fsync-strength durability
	// checking — it is not.
	return closeErr
}

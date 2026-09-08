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
	defer f.Close()
	return f.Sync()
}

package autorepair

import "math"

// NewAppendLog returns a repair log for a caller that does not hold the
// global repair lock, such as the git pre-commit hook. It only appends:
// every line is one O_APPEND write, and it never renames the file, so two
// commits in two worktrees that share one global root cannot lose each
// other's lines. Rotation stays with the lock-holding engine, which is the
// only caller of newFileLog.
func NewAppendLog(global, store, checkout, ethos string) *FileLog {
	l := newFileLog(global, store, checkout, ethos)
	l.max = math.MaxInt64
	return l
}

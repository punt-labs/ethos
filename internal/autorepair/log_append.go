package autorepair

import "log/slog"

// NewAppendLog adapts hook repair actions to the shared process logger.
func NewAppendLog(store, checkout, ethos string, logger *slog.Logger) *FileLog {
	return newFileLog(store, checkout, ethos, logger)
}

package autorepair

import (
	"log/slog"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// FileLog adapts repair actions to the process-owned shared diagnostic sink.
type FileLog struct {
	logger *slog.Logger
}

func newFileLog(_, _, ethos string, logger *slog.Logger) *FileLog {
	if logger == nil {
		logger = diagnostic.Discard()
	}
	return &FileLog{logger: logger.With("component", "ethos.repair", "ethos", ethos)}
}

// Logf records a repair decision after applying the existing prose redactor.
func (l *FileLog) Logf(step, action string, paths []string, err error) {
	rp := make([]string, len(paths))
	for i, p := range paths {
		rp[i] = redact.Prose(p)
	}
	attrs := []any{"step", redact.Prose(step), "action", redact.Prose(action), "paths", strings.Join(rp, ", ")}
	if err != nil {
		attrs = append(attrs, "error", redact.Prose(err.Error()))
	}
	if action == "note" {
		l.logger.Warn("Roster purge warning", attrs...)
		return
	}
	l.logger.Info("Repair action", attrs...)
}

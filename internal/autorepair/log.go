package autorepair

import (
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// FileLog adapts repair actions to the process-owned shared diagnostic sink.
type FileLog struct {
	logger   *slog.Logger
	roots    []repairLogRoot
	prefixes *strings.Replacer
}

type repairLogRoot struct {
	path   string
	marker string
}

func newFileLog(store, checkout, ethos string, logger *slog.Logger) *FileLog {
	if logger == nil {
		logger = diagnostic.Discard()
	}
	roots := []repairLogRoot{{store, "<store>"}, {checkout, "<checkout>"}}
	sort.SliceStable(roots, func(i, j int) bool { return len(roots[i].path) > len(roots[j].path) })
	l := &FileLog{logger: logger.With("component", "ethos.repair", "ethos", ethos)}
	var prefixes []string
	for _, root := range roots {
		if !filepath.IsAbs(root.path) {
			continue
		}
		root.path = filepath.Clean(root.path)
		l.roots = append(l.roots, root)
		prefixes = append(prefixes, root.path, root.marker)
	}
	l.prefixes = strings.NewReplacer(prefixes...)
	return l
}

func (l *FileLog) relativePath(path string) (string, bool) {
	for _, root := range l.roots {
		rel, err := filepath.Rel(root.path, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return redact.Prose(filepath.ToSlash(rel)), true
		}
	}
	return "", false
}

// Logf records repair metadata after applying the existing prose redactor.
func (l *FileLog) Logf(step, action string, paths []string, err error) {
	relative := make([]string, 0, len(paths))
	for _, path := range paths {
		if rel, ok := l.relativePath(path); ok {
			relative = append(relative, rel)
		}
	}
	attrs := []any{"step", redact.Prose(step), "action", redact.Prose(action), "paths", strings.Join(relative, ","), "path_count", len(paths), "omitted_path_count", len(paths) - len(relative)}
	if err != nil {
		attrs = append(attrs, "error_type", diagnostic.ErrorType(err), "error", redact.Prose(l.prefixes.Replace(err.Error())))
	}
	if action == "note" {
		l.logger.Warn("Roster purge warning", attrs...)
		return
	}
	l.logger.Info("Repair action", attrs...)
}

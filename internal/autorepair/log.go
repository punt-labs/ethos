package autorepair

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// FileLog adapts repair actions to the process-owned shared diagnostic sink.
type FileLog struct {
	logger *slog.Logger
	roots  []string
}

func newFileLog(store, checkout, ethos string, logger *slog.Logger) *FileLog {
	if logger == nil {
		logger = diagnostic.Discard()
	}
	roots := []string{store, checkout}
	sort.SliceStable(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	l := &FileLog{logger: logger.With("component", "ethos.repair", "ethos", ethos)}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			continue
		}
		l.roots = append(l.roots, filepath.Clean(root))
	}
	return l
}

func (l *FileLog) relativePath(path string) (string, bool) {
	if filepath.IsLocal(path) {
		return redact.Prose(filepath.ToSlash(path)), true
	}
	for _, root := range l.roots {
		rel, err := filepath.Rel(root, path)
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
		attrs = append(attrs, "error_type", diagnostic.ErrorType(err))
		var pathErr *fs.PathError
		var linkErr *os.LinkError
		var op string
		switch {
		case errors.As(err, &pathErr):
			op = pathErr.Op
		case errors.As(err, &linkErr):
			op = linkErr.Op
		}
		switch op {
		case "open", "read", "write", "close", "stat", "lstat", "mkdir", "remove", "unlink", "rename", "link", "symlink", "readlink", "chmod", "chown", "lchown", "truncate", "sync", "readdir", "seek", "chtimes", "chdir":
			attrs = append(attrs, "error_op", op)
		}
		var errno syscall.Errno
		if errors.As(err, &errno) {
			attrs = append(attrs, "error_cause", errno.Error())
		}
	}
	if action == "note" {
		l.logger.Warn("Roster purge warning", attrs...)
		return
	}
	l.logger.Info("Repair action", attrs...)
}

package autorepair

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/punt-labs/ethos/v4/internal/redact"
)

// maxLogBytes is the size at which the log rotates to repair.log.1.
const maxLogBytes = 1 << 20

// LogPath is the repair log under the global store root.
func LogPath(global string) string { return filepath.Join(global, "logs", "repair.log") }

// FileLog is the Logger the engine supplies: one JSON line per action,
// appended with O_APPEND. The engine only calls it while holding the global
// lock, which is what makes rotation safe. Paths and errors pass through the
// DES-080 home-path redactor; file contents are never an argument.
type FileLog struct {
	path     string
	store    string
	checkout string
	ethos    string
	max      int64
	mu       sync.Mutex
}

// NewFileLog returns the repair log for the global store root, tagged with
// the repository roots (empty outside one) and the binary version.
func NewFileLog(global, store, checkout, ethos string) *FileLog {
	return &FileLog{path: LogPath(global), store: store, checkout: checkout, ethos: ethos, max: maxLogBytes}
}

type logLine struct {
	Time     string   `json:"time"`
	Store    string   `json:"store"`
	Checkout string   `json:"checkout"`
	Ethos    string   `json:"ethos"`
	Step     string   `json:"step"`
	Action   string   `json:"action"`
	Paths    []string `json:"paths"`
	Error    string   `json:"error,omitempty"`
}

// Logf appends one line. A log that cannot be written is dropped: a repair
// problem never fails the command that triggered the repair.
func (l *FileLog) Logf(step, action string, paths []string, err error) {
	rp := make([]string, len(paths))
	for i, p := range paths {
		rp[i] = redact.HomePaths(p)
	}
	line := logLine{
		Time:     time.Now().UTC().Format(time.RFC3339),
		Store:    redact.HomePaths(l.store),
		Checkout: redact.HomePaths(l.checkout),
		Ethos:    l.ethos,
		Step:     step,
		Action:   action,
		Paths:    rp,
	}
	if err != nil {
		line.Error = redact.HomePaths(err.Error())
	}
	data, merr := json.Marshal(line)
	if merr != nil {
		return
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return
	}
	l.rotate()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(data)
	_ = f.Close()
}

// rotate moves a log at or past the cap to repair.log.1, replacing the
// previous rotation.
func (l *FileLog) rotate() {
	info, err := os.Stat(l.path)
	if err != nil || info.Size() < l.max {
		return
	}
	_ = os.Rename(l.path, l.path+".1")
}

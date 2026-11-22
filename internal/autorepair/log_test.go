package autorepair

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// LogPath names the injected JSON capture, not the production diagnostic sink.
func LogPath(global string) string { return filepath.Join(global, "logs", "captured.jsonl") }

type captureLogWriter struct {
	path string
	mu   sync.Mutex
}

func (w *captureLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.Write(p)
}

func captureRepairLog(global string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(&captureLogWriter{path: LogPath(global)}, nil))
}

func readLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		out = append(out, m)
	}
	require.NoError(t, sc.Err())
	return out
}

func TestFileLogWritesJSONLines(t *testing.T) {
	global := physicalTemp(t)
	l := newFileLog("/repo/store", "/repo/checkout", "v5.0.0", captureRepairLog(global))
	l.Logf("overlays", "move", []string{"/repo/store/.punt-labs/a", "/repo/store/.punt-labs/b"}, nil)
	l.Logf("overlays", "move", nil, errors.New("boom"))

	lines := readLog(t, LogPath(global))
	require.Len(t, lines, 2)
	first := lines[0]
	for _, k := range []string{"time", "store", "checkout", "ethos", "step", "action", "paths"} {
		assert.Contains(t, first, k)
	}
	assert.Equal(t, "overlays", first["step"])
	assert.Equal(t, "move", first["action"])
	assert.Equal(t, "v5.0.0", first["ethos"])
	assert.Equal(t, "/repo/store/.punt-labs/a, /repo/store/.punt-labs/b", first["paths"])
	assert.Equal(t, "ethos.repair", first["component"])
	assert.NotContains(t, first, "error")
	assert.Equal(t, "boom", lines[1]["error"])
}

func TestFileLogRedactsHomePaths(t *testing.T) {
	global := physicalTemp(t)
	l := newFileLog("", "", "v5.0.0", captureRepairLog(global))
	l.Logf("s", "delete", []string{"/Users/alice/secret-project/x"}, errors.New("open /Users/alice/secret-project/x: denied"))
	raw, err := os.ReadFile(LogPath(global))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "alice")
}

// A step error can quote file contents (a yaml decode error includes the
// value), so the whole line goes through the DES-080 prose redactor: secrets
// and addresses as well as home paths, in paths, store, checkout and error.
func TestFileLogRedactsSecretsAndEmails(t *testing.T) {
	global := physicalTemp(t)
	l := newFileLog("/Users/alice/repo", "/Users/alice/repo", "v5.0.0", captureRepairLog(global))
	err := errors.New(`yaml: cannot decode "token: ghp_abcdefghijklmnopqrstuvwxyz0123456789" for alice@example.com in /Users/alice/repo/x`)
	l.Logf("overlays", "move", []string{"/Users/alice/repo/ghp_abcdefghijklmnopqrstuvwxyz0123456789"}, err)
	raw, rerr := os.ReadFile(LogPath(global))
	require.NoError(t, rerr)
	s := string(raw)
	assert.NotContains(t, s, "ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	assert.NotContains(t, s, "alice@example.com")
	assert.NotContains(t, s, "alice")
	assert.Contains(t, s, "[redacted-secret]")
	assert.Contains(t, s, "[redacted-email]")
}

func TestFileLogWithoutInjectedLoggerWritesNothing(t *testing.T) {
	global := physicalTemp(t)
	l := newFileLog("", "", "v5.0.0", nil)
	l.Logf("s", "a", nil, nil)
	entries, err := os.ReadDir(global)
	require.NoError(t, err)
	assert.Empty(t, entries, "only the process entry point owns diagnostic files")
}

func TestRepairSharedSinkLeavesLegacyFileUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".punt-labs", "ethos", "logs")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	legacy := filepath.Join(dir, "repair.log")
	require.NoError(t, os.WriteFile(legacy, []byte("legacy evidence\n"), 0o600))
	logger, closeLog, err := diagnostic.Open("cli")
	require.NoError(t, err)
	t.Cleanup(closeLog)
	l := newFileLog("/Users/alice/repo", "", "test", logger)
	l.Logf("gitignore", "repair", []string{"/Users/alice/repo/a"}, errors.New("failed at /Users/alice/repo/a"))
	l.Logf("sessions", "note", nil, nil)
	closeLog()
	old, err := os.ReadFile(legacy)
	require.NoError(t, err)
	require.Equal(t, "legacy evidence\n", string(old))
	raw, err := os.ReadFile(filepath.Join(dir, "ethos.log"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "[INFO] ethos.repair: Repair action")
	require.Contains(t, string(raw), "[WARNING] ethos.repair: Roster purge warning")
	require.NotContains(t, string(raw), "/Users/alice")
}

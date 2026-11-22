package autorepair

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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
	for _, k := range []string{"time", "ethos", "step", "action", "path_count"} {
		assert.Contains(t, first, k)
	}
	assert.Equal(t, "overlays", first["step"])
	assert.Equal(t, "move", first["action"])
	assert.Equal(t, "v5.0.0", first["ethos"])
	assert.Equal(t, float64(2), first["path_count"])
	assert.Equal(t, ".punt-labs/a,.punt-labs/b", first["paths"])
	assert.Equal(t, "ethos.repair", first["component"])
	assert.NotContains(t, first, "store")
	assert.NotContains(t, first, "checkout")
	assert.NotContains(t, first, "error")
	assert.Equal(t, "*errors.errorString", lines[1]["error_type"])
	assert.NotContains(t, lines[1], "error")
}

func TestFileLogOmitsYAMLRecordContents(t *testing.T) {
	const sentinel = "C0NFID"
	var record struct {
		Count int `yaml:"count"`
	}
	err := yaml.Unmarshal([]byte("count: "+sentinel+"\n"), &record)
	require.Error(t, err)
	require.Contains(t, err.Error(), sentinel)
	global := physicalTemp(t)
	l := newFileLog("/repo/store", "/repo/checkout", "test", captureRepairLog(global))
	l.Logf("bead-alias", "rename", nil, fmt.Errorf("renaming contract: %w", err))
	raw, readErr := os.ReadFile(LogPath(global))
	require.NoError(t, readErr)
	assert.NotContains(t, string(raw), sentinel)
	entry := readLog(t, LogPath(global))[0]
	assert.NotContains(t, entry, "error")
	assert.Equal(t, "*yaml.TypeError", entry["error_type"])
}

func TestFileLogSafeErrorCauses(t *testing.T) {
	const sentinel = "PRIVATE_RECORD_CONTENT"
	for _, tc := range []struct {
		name  string
		err   error
		op    string
		cause string
	}{
		{name: "empty"},
		{name: "untyped", err: errors.New(sentinel)},
		{name: "path", err: fmt.Errorf("%s: %w", sentinel, &fs.PathError{Op: "open", Path: sentinel, Err: syscall.EACCES}), op: "open", cause: syscall.EACCES.Error()},
		{name: "link", err: fmt.Errorf("%s: %w", sentinel, &os.LinkError{Op: "rename", Old: sentinel, New: sentinel, Err: syscall.ENOENT}), op: "rename", cause: syscall.ENOENT.Error()},
		{name: "unsafe op", err: &fs.PathError{Op: sentinel, Path: sentinel, Err: syscall.EACCES}, cause: syscall.EACCES.Error()},
		{name: "unsafe cause", err: &fs.PathError{Op: "read", Path: sentinel, Err: errors.New(sentinel)}, op: "read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global := physicalTemp(t)
			l := newFileLog("", "", "test", captureRepairLog(global))
			l.Logf("gitignore", "rewrite", nil, tc.err)
			raw, err := os.ReadFile(LogPath(global))
			require.NoError(t, err)
			assert.NotContains(t, string(raw), sentinel)
			entry := readLog(t, LogPath(global))[0]
			assert.NotContains(t, entry, "error")
			for key, want := range map[string]string{"error_op": tc.op, "error_cause": tc.cause} {
				if want == "" {
					assert.NotContains(t, entry, key)
				} else {
					assert.Equal(t, want, entry[key])
				}
			}
		})
	}
}

func TestFileLogRedactsHomePaths(t *testing.T) {
	global := physicalTemp(t)
	l := newFileLog("", "", "v5.0.0", captureRepairLog(global))
	l.Logf("s", "delete", []string{"/Users/alice/secret-project/x"}, errors.New("open /Users/alice/secret-project/x: denied"))
	raw, err := os.ReadFile(LogPath(global))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "alice")
}

func TestFileLogOmitsRepositoryRoots(t *testing.T) {
	global := physicalTemp(t)
	l := newFileLog("/home/ubuntu/private-smoke-store", "/home/ubuntu/private-smoke-checkout", "test", captureRepairLog(global))
	l.Logf("sessions", "ran", []string{"/home/ubuntu/private-smoke-store/sessions/a.json"}, errors.New("open /home/ubuntu/private-smoke-checkout/sessions/a.json: denied"))
	raw, err := os.ReadFile(LogPath(global))
	require.NoError(t, err)
	for _, private := range []string{"ubuntu", "private-smoke-store", "private-smoke-checkout"} {
		require.NotContains(t, string(raw), private)
	}
}

func TestResidualLogNamesRelativePathAndCause(t *testing.T) {
	r := newRig(t, true)
	r.store = filepath.Join(r.store, "private-smoke-checkout")
	r.roots.Store, r.roots.Checkout = r.store, r.store
	require.NoError(t, os.MkdirAll(filepath.Join(r.store, ".punt-labs"), 0o700))
	r.opts.order = []string{"gitignore"}
	r.opts.steps = []Step{{Name: "gitignore", Scope: ScopeRepository, Run: func(_ context.Context, env Env) (Outcome, error) {
		path := filepath.Join(env.Roots.Store, ".gitignore")
		env.Log.Logf("gitignore", "blocked", []string{path}, fmt.Errorf("rewriting %s: %w", path, &fs.PathError{Op: "open", Path: path, Err: syscall.EACCES}))
		return Outcome{Residuals: []Residual{actionableResidual("gitignore", ".gitignore", "cannot rewrite", seeLogAction)}}, nil
	}}}
	result := r.ensure()
	require.Len(t, result.Residuals, 1)
	require.Contains(t, result.Residuals[0].EffectiveAction(), "ethos.log")
	var blocked map[string]any
	for _, entry := range readLog(t, LogPath(r.global)) {
		if entry["action"] == "blocked" {
			blocked = entry
		}
	}
	require.NotNil(t, blocked)
	assert.Equal(t, ".gitignore", blocked["paths"])
	assert.NotContains(t, blocked, "error")
	assert.Equal(t, "open", blocked["error_op"])
	assert.Equal(t, syscall.EACCES.Error(), blocked["error_cause"])
	assert.Equal(t, "syscall.Errno", blocked["error_type"])
	raw, err := os.ReadFile(LogPath(r.global))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), r.store)
	assert.NotContains(t, string(raw), "private-smoke-checkout")
}

func TestFileLogRelativePaths(t *testing.T) {
	for _, tc := range []struct {
		name     string
		store    string
		checkout string
		paths    []string
		want     string
		omitted  int
	}{
		{name: "empty"},
		{name: "empty path", store: "/srv/store", paths: []string{""}, omitted: 1},
		{name: "escaping relative", store: "/srv/store", paths: []string{"../private-smoke-path", "nested/../../private-smoke-path"}, omitted: 2},
		{name: "unbound", paths: []string{"/home/ubuntu/private-smoke-path"}, omitted: 1},
		{name: "outside", store: "/srv/store", checkout: "/srv/checkout", paths: []string{"/srv/store-other/file", "/srv/store/../private-smoke-path"}, omitted: 2},
		{name: "both roots", store: "/srv/store", checkout: "/srv/checkout", paths: []string{"/srv/store/missions/a.yaml", "/srv/checkout/.gitignore", "/private-smoke-path"}, want: "missions/a.yaml,.gitignore", omitted: 1},
		{name: "nested checkout", store: "/srv/store", checkout: "/srv/store/private-smoke-checkout", paths: []string{"/srv/store/private-smoke-checkout/.gitignore"}, want: ".gitignore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global := physicalTemp(t)
			l := newFileLog(tc.store, tc.checkout, "test", captureRepairLog(global))
			l.Logf("gitignore", "rewrite", tc.paths, nil)
			raw, err := os.ReadFile(LogPath(global))
			require.NoError(t, err)
			for _, private := range []string{"ubuntu", "private-smoke-path", "private-smoke-checkout"} {
				require.NotContains(t, string(raw), private)
			}
			entry := readLog(t, LogPath(global))[0]
			assert.Equal(t, float64(len(tc.paths)), entry["path_count"])
			assert.Equal(t, tc.want, entry["paths"])
			assert.Equal(t, float64(tc.omitted), entry["omitted_path_count"])
			assert.NotContains(t, entry, "error")
			assert.NotContains(t, entry, "error_type")
		})
	}
}

func TestFileLogKeepsScrubRecordPaths(t *testing.T) {
	const path = ".punt-labs/ethos/missions/m1/contract.yaml"
	for _, roots := range [][2]string{{"/srv/store", "/srv/checkout"}, {"", ""}} {
		global := physicalTemp(t)
		l := newFileLog(roots[0], roots[1], "test", captureRepairLog(global))
		for _, action := range []string{"redact-in-place", "defer-busy"} {
			l.Logf("scrub-records", action, []string{path}, nil)
		}
		entries := readLog(t, LogPath(global))
		require.Len(t, entries, 2)
		for _, entry := range entries {
			assert.Equal(t, path, entry["paths"])
			assert.Equal(t, float64(1), entry["path_count"])
			assert.Equal(t, float64(0), entry["omitted_path_count"])
		}
	}
}

func TestFileLogRedactsSecretsAndEmails(t *testing.T) {
	global := physicalTemp(t)
	l := newFileLog("/Users/alice/repo", "/Users/alice/repo", "v5.0.0", captureRepairLog(global))
	err := errors.New(`yaml: cannot decode "token: ghp_abcdefghijklmnopqrstuvwxyz0123456789" for alice@example.com in /Users/alice/repo/x`)
	l.Logf("overlays alice@example.com", "move ghp_abcdefghijklmnopqrstuvwxyz0123456789", []string{"/Users/alice/repo/ghp_abcdefghijklmnopqrstuvwxyz0123456789"}, err)
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

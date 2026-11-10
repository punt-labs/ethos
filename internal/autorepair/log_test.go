package autorepair

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	l := newFileLog(global, "/repo/store", "/repo/checkout", "v5.0.0")
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
	assert.Len(t, first["paths"], 2)
	assert.NotContains(t, first, "error")
	assert.Equal(t, "boom", lines[1]["error"])
}

func TestFileLogRedactsHomePaths(t *testing.T) {
	global := physicalTemp(t)
	l := newFileLog(global, "", "", "v5.0.0")
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
	l := newFileLog(global, "/Users/alice/repo", "/Users/alice/repo", "v5.0.0")
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

func TestFileLogRotatesOnce(t *testing.T) {
	global := physicalTemp(t)
	l := newFileLog(global, "", "", "v5.0.0")
	l.max = 400
	pad := []string{strings.Repeat("p", 150)}
	for range 20 {
		l.Logf("s", "a", pad, nil)
	}
	info, err := os.Stat(LogPath(global))
	require.NoError(t, err)
	assert.LessOrEqual(t, info.Size(), int64(l.max)+400, "live log stays near the cap")
	_, err = os.Stat(LogPath(global) + ".1")
	require.NoError(t, err, "rotated copy exists")
	entries, err := os.ReadDir(filepath.Dir(LogPath(global)))
	require.NoError(t, err)
	assert.Len(t, entries, 2, "one rotation only")
}

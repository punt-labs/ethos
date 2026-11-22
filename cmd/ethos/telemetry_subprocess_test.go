//go:build linux || darwin

package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func assertUsageRow(t *testing.T, home, name string, fields ...string) {
	t.Helper()
	dir := filepath.Join(home, ".punt-labs", "ethos")
	path := filepath.Join(dir, "telemetry.db")
	for p, mode := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		require.Equal(t, mode, info.Mode().Perm())
	}
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	rows, err := db.Query("SELECT name, schema_version, attributes FROM signals")
	require.NoError(t, err)
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var gotName, raw string
		var version int
		require.NoError(t, rows.Scan(&gotName, &version, &raw))
		require.Equal(t, name, gotName)
		require.Equal(t, 1, version)
		var attrs map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &attrs))
		require.Len(t, attrs, len(fields), raw)
		for _, key := range fields {
			require.Contains(t, attrs, key)
		}
		require.Equal(t, "completed", attrs["outcome"])
		for _, private := range []string{home, "logging-private-login", "private-content", "private-token"} {
			require.NotContains(t, raw, private)
		}
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 1, count)
}

func TestSharedTelemetryCLIAndVetoes(t *testing.T) {
	var baseline []byte
	for _, veto := range []string{"", "ETHOS_TELEMETRY", "PUNT_TELEMETRY"} {
		t.Run("veto="+veto, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			cmd := loggingProcess(t, home, cwd, "version")
			if veto != "" {
				cmd.Env = append(cmd.Env, veto+"=off")
			}
			out, err := cmd.CombinedOutput()
			require.NoError(t, err)
			if veto == "" {
				baseline = out
				assertUsageRow(t, home, "ethos.cli.invoke", "command", "outcome", "exit_code", "duration_ms")
			} else {
				require.Equal(t, string(baseline), string(out))
				_, err := os.Stat(filepath.Join(home, ".punt-labs", "ethos", "telemetry.db"))
				require.True(t, os.IsNotExist(err), "veto must not create telemetry.db: %v", err)
			}
		})
	}
}

func TestSharedTelemetryMCP(t *testing.T) {
	home := runLoggingMCP(t)
	assertUsageRow(t, home, "ethos.mcp.call", "tool", "action", "outcome", "duration_ms")
}

func TestSharedTelemetryMCPVetoes(t *testing.T) {
	_, baseline := runLoggingMCPEnv(t, "")
	for _, veto := range []string{"ETHOS_TELEMETRY", "PUNT_TELEMETRY"} {
		t.Run(veto, func(t *testing.T) {
			home, transcript := runLoggingMCPEnv(t, veto)
			require.Equal(t, string(baseline), string(transcript))
			_, err := os.Stat(filepath.Join(home, ".punt-labs", "ethos", "telemetry.db"))
			require.True(t, os.IsNotExist(err), "veto must not create telemetry.db: %v", err)
		})
	}
}

func TestSharedTelemetryFailurePrivacy(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cmd := loggingProcess(t, home, cwd, "version", "--private-token=ghp_abcdefghijklmnopqrstuvwxyz0123456789", filepath.Join(home, "private-content"))
	require.Error(t, cmd.Run())
	path := filepath.Join(home, ".punt-labs", "ethos", "telemetry.db")
	_, err := os.Stat(path)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	var attrs string
	require.NoError(t, db.QueryRow("SELECT attributes FROM signals").Scan(&attrs))
	require.Contains(t, attrs, `"outcome":"failed"`)
	for _, private := range []string{home, cwd, "logging-private-login", "ghp_", "private-content", "private-token"} {
		require.False(t, strings.Contains(attrs, private), attrs)
	}
}

//go:build !windows

package diagnostic

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestUsageClosedSchemas(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ETHOS_TELEMETRY", "on")
	t.Setenv("PUNT_TELEMETRY", "on")
	u := NewUsage(Discard(), []Command{"version", "mission create", "hook pre-tool-use", "ui"})
	u.Invocation("cli", Invocation{Command: "version", Duration: time.Millisecond})
	u.Invocation("hook", Invocation{Command: "hook pre-tool-use", ExitCode: 2})
	u.Invocation("ui", Invocation{Command: "ui"})
	u.ToolCall(ToolCall{Tool: "identity", Action: "list", Failed: true})
	u.ToolCall(ToolCall{Tool: "doctor"})
	u.Invocation("cli", Invocation{Command: "/private/content"})
	u.Invocation("cli", Invocation{})
	u.Invocation("private", Invocation{Command: "version"})
	u.Invocation("cli", Invocation{Command: "version", Duration: -time.Second})
	u.ToolCall(ToolCall{Tool: "private-content"})
	u.ToolCall(ToolCall{Tool: "identity", Action: "private-content"})
	u.Close()
	db, err := sql.Open("sqlite", filepath.Join(home, ".punt-labs", "ethos", "telemetry.db"))
	require.NoError(t, err)
	defer db.Close()
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM signals").Scan(&count))
	require.Equal(t, 5, count, "unknown enums, empty commands and invalid durations must be dropped")
	var raw string
	require.NoError(t, db.QueryRow("SELECT group_concat(attributes) FROM signals").Scan(&raw))
	require.NotContains(t, raw, "private")
	require.Contains(t, raw, "hook-pre-tool-use")
	require.Contains(t, raw, "failed")
	var absent *Usage
	absent.Invocation("cli", Invocation{})
	absent.ToolCall(ToolCall{})
	absent.Close()
}

func TestUsageInitializationFailureIsSilent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ETHOS_TELEMETRY", "on")
	t.Setenv("PUNT_TELEMETRY", "on")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".punt-labs"), []byte("blocked"), 0o600))
	u := NewUsage(Discard(), []Command{"version"})
	u.Invocation("cli", Invocation{Command: "version"})
	u.Close()
}

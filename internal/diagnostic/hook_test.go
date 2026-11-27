package diagnostic

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHandoffHookCheckDetails(t *testing.T) {
	var logs bytes.Buffer
	ctx, _ := WithHookInvocation(WithLogger(t.Context(), slog.New(slog.NewTextHandler(&logs, nil))))
	checkout := t.TempDir()
	LogHookCheck(ctx, checkout, filepath.Join(checkout, "src"), "handoff", "gate", "completed", time.Millisecond, HookCheckDetails{
		Isolation:            "snapshot",
		IsolationResidual:    "host loopback",
		DeveloperFingerprint: "abc123",
	})
	require.Contains(t, logs.String(), "isolation=snapshot")
	require.Contains(t, logs.String(), `isolation_residual="host loopback"`)
	require.Contains(t, logs.String(), "developer_fingerprint=abc123")
	require.Contains(t, logs.String(), "dir=src")

	logs.Reset()
	LogHookCheck(ctx, checkout, "", "handoff", "gate", "deferred", time.Millisecond)
	require.Contains(t, logs.String(), "outcome=deferred")
	require.NotContains(t, logs.String(), "isolation=")
	require.NotContains(t, logs.String(), "developer_fingerprint=")
}

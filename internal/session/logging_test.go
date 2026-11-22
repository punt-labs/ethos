package session

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreWarningLoggerAndWriterOverride(t *testing.T) {
	var logs, visible bytes.Buffer
	s := NewStore(t.TempDir())
	s.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	s.warnf("cannot purge %s: %v", "/Users/alice/private", "sk-ant-test-secret")
	require.Contains(t, logs.String(), "level=WARN")
	require.Contains(t, logs.String(), "Roster purge warning")
	require.NotContains(t, logs.String(), "/Users/alice")
	before := logs.String()
	s.SetWarnings(&visible)
	s.warnf("visible %s", "warning")
	require.Equal(t, "visible warning", visible.String())
	require.Equal(t, before, logs.String(), "injected warning writer takes precedence")
}

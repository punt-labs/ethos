package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/lineguard"
)

const longAuditLines = 4722

func longAuditFixture(tb testing.TB) []byte {
	tb.Helper()
	var stream bytes.Buffer
	enc := json.NewEncoder(&stream)
	for i := range longAuditLines {
		path := fmt.Sprintf("internal/component%d/handler.go", i%173)
		tool, input := "Read", map[string]any{"file_path": path, "offset": i % 211}
		switch {
		case i%101 == 0:
			tool, input = "Write", map[string]any{"file_path": path, "content": strings.Repeat("func handle() { return }\n", 1170)}
		case i%19 == 0:
			input["file_path"] = "/Users/example/work/" + path
		case i%3 == 1:
			tool, input = "Bash", map[string]any{"command": "go test ./internal/...", "description": "Check repository tests"}
		case i%3 == 2:
			tool, input = "Edit", map[string]any{"file_path": path, "old_string": strings.Repeat("return nil\n", 21), "new_string": strings.Repeat("return result\n", 25)}
		}
		env := map[string]any{"tool_input": input}
		entry := map[string]any{
			"ts": audit.FormatLineTS(1794873600000000000 + int64(i)), "session_id": "long-session",
			"event": "PostToolUse", "tool": tool, "tool_input": input,
			"tool_input_preview": lineguard.ToolInputPreview(env), "tool_input_hash": lineguard.HashToolInput(env),
		}
		if tool == "Bash" {
			entry["tool_response"] = strings.Repeat("ok  example/internal/component\n", 6)
		}
		require.NoError(tb, enc.Encode(entry))
	}
	return stream.Bytes()
}

func longAuditStream(tb testing.TB) (layout.Policy, audit.Stream, []byte) {
	tb.Helper()
	root := tb.TempDir()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(tb, err)
	return p, audit.Stream{Namespace: audit.SessionNS, Session: "long-session"}, longAuditFixture(tb)
}

func BenchmarkRedactLiveLongSession(b *testing.B) {
	p, s, data := longAuditStream(b)
	b.SetBytes(int64(len(data)))
	guard := LiveLineGuard(p, s.Namespace)
	for b.Loop() {
		b.StopTimer()
		require.NoError(b, audit.ReplaceLocalFile(p, s.LivePath(p), data))
		b.StartTimer()
		_, err := audit.RedactLive(context.Background(), p, s, guard, 0)
		require.NoError(b, err)
	}
}

func TestRedactLiveLongSessionBudget(t *testing.T) {
	p, s, data := longAuditStream(t)
	require.NoError(t, audit.ReplaceLocalFile(p, s.LivePath(p), data))
	budget := 8 * time.Second
	if testing.CoverMode() != "" {
		budget = 35 * time.Second
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	res, err := audit.RedactLive(ctx, p, s, LiveLineGuard(p, s.Namespace), 0)
	t.Logf("RedactLive: %d bytes, %d lines, %v", len(data), longAuditLines, time.Since(start))
	require.NoError(t, err)
	require.Less(t, time.Since(start), budget)
	require.True(t, res.Complete)
	require.Positive(t, res.Rewritten)
}

package hook

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/stretchr/testify/require"
)

func TestDarwinHandoffLiveSignalDefers(t *testing.T) {
	for _, run := range []string{`[/bin/sh, -c, 'kill -KILL $$']`, `[/bin/sh, -c, 'exit 137']`} {
		root := approvedDarwinLiveGate(t, run, "")
		for range 2 {
			ctx, _ := diagnostic.WithHookInvocation(t.Context())
			result := RunStageChecks(ctx, root, "worker", "handoff")
			require.Contains(t, result.Advisory, "killed or interrupted", "%+v", result)
			require.Empty(t, result.Blocking)
		}
	}
}

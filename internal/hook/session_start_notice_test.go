package hook

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const repairLine = "ethos: upgraded storage (3 items; see ~/.punt-labs/ethos/logs/repair.log)"

// The repair line reaches the user through the JSON the hook prints, in both
// systemMessage and additionalContext: session-start stderr goes to an unread
// log (DES-084 Output).
func TestSessionStartCarriesRepairNotice(t *testing.T) {
	for _, withNotice := range []bool{true, false} {
		t.Run(map[bool]string{true: "notice", false: "none"}[withNotice], func(t *testing.T) {
			loc, s, ss, ts, rs := snapshotHookStores(t)
			deps := SessionStartDeps{Located: &loc, Store: s, Sessions: ss, Teams: ts, Roles: rs}
			if withNotice {
				deps.Notice = repairLine
			}
			out, err := captureStdout(t, func() error {
				return HandleSessionStart(strings.NewReader(`{"session_id":"notice-session"}`), deps)
			})
			require.NoError(t, err)
			var got SessionStartResult
			require.NoError(t, json.Unmarshal([]byte(out), &got), out)
			if withNotice {
				assert.Contains(t, got.SystemMessage, repairLine)
				assert.Contains(t, got.HookSpecificOutput.AdditionalContext, repairLine)
			} else {
				assert.NotContains(t, out, "upgraded storage")
			}
		})
	}
}

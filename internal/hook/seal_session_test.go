package hook

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// appendSessionLine writes one audit line for session sid through the real
// audit hook under p.
func appendSessionLine(t *testing.T, p layout.Policy, sid string) {
	t.Helper()
	input := `{"session_id":"` + sid + `","tool_name":"Read","tool_input":{"file_path":"example.go"}}`
	require.NoError(t, HandleAuditLogWithPolicy(strings.NewReader(input), p, ""))
}

// TestSealSessionWithPolicySealsOnlyThatSession pins that the per-session seal
// the sessions repair step uses archives the named session and leaves every
// other session's live lines unsealed.
func TestSealSessionWithPolicySealsOnlyThatSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	p := hookPolicy(t, root, root, layout.RecordBlame)
	appendSessionLine(t, p, "s-dead")
	appendSessionLine(t, p, "s-dead")
	appendSessionLine(t, p, "s-live")

	res, err := SealSessionWithPolicy(p, "s-dead", time.Now(), SealOptions{})
	require.NoError(t, err)
	require.Equal(t, 2, res.LinesSealed)

	n, err := audit.UnarchivedCount(p, audit.Stream{Namespace: audit.SessionNS, Session: "s-dead"})
	require.NoError(t, err)
	require.Zero(t, n, "the named session is fully sealed")
	n, err = audit.UnarchivedCount(p, audit.Stream{Namespace: audit.SessionNS, Session: "s-live"})
	require.NoError(t, err)
	require.Equal(t, 1, n, "another session's lines stay unsealed")
}

// TestSealSessionHookIsRegistered pins that the hook package wires the
// autorepair seal, and that the wired function seals through the same path.
func TestSealSessionHookIsRegistered(t *testing.T) {
	require.NotNil(t, autorepair.SealSession, "hook init must register the session seal")
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	p := hookPolicy(t, root, root, layout.RecordBlame)
	appendSessionLine(t, p, "s-x")

	got, err := autorepair.SealSession(p, "s-x", "", time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, got)
	n, err := audit.UnarchivedCount(p, audit.Stream{Namespace: audit.SessionNS, Session: "s-x"})
	require.NoError(t, err)
	require.Zero(t, n)
}

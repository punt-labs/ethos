package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

type blockedHookInput struct {
	io.Reader
	entered chan struct{}
	resume  chan struct{}
}

var _ io.Reader = (*blockedHookInput)(nil)

func TestPreToolUsePreconditionsPreserveLocatedFailures(t *testing.T) {
	root := t.TempDir()
	cause := errors.New("captured location failure")
	for _, part := range []string{"store", "ethos", "checkout", "config"} {
		t.Run(part, func(t *testing.T) {
			loc := resolve.Located{Store: root, Checkout: root}
			switch part {
			case "store":
				loc.StoreErr = cause
			case "ethos":
				loc.EthosErr = cause
			case "checkout":
				loc.CheckoutErr = cause
			case "config":
				loc.ConfigErr = cause
			}
			contract := makeContractWithPreconditions([]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read first"}}, mission.Inputs{}, nil)
			input := map[string]any{"file_path": "file.go"}
			reason, deny := gateLocatedContract(contract, "Write", input, "s", loc)
			require.True(t, deny)
			require.Contains(t, reason, cause.Error())
			_, deny, err := evaluateLocatedPreconditions(contract, "Write", input, "s", loc)
			require.True(t, deny)
			require.ErrorIs(t, err, cause)
			contract.StrictPreconditions = boolPtr(false)
			reason, deny = gateLocatedContract(contract, "Write", input, "s", loc)
			require.False(t, deny, "the explicit non-strict warning policy is unchanged")
			require.Contains(t, reason, cause.Error(), "the allow result retains the unevaluable cause")
		})
	}
}
func TestPreToolUseLinkedMustReadUsesStoreHistoryAndCheckoutPaths(t *testing.T) {
	store, checkout := linkedWorktree(t)
	p := hookPolicy(t, store, checkout, layout.RecordBlame)
	loc := resolve.Located{Store: store, Checkout: checkout}
	contract := makeContractWithPreconditions([]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read it first"}}, mission.Inputs{}, nil)
	payload, err := json.Marshal(map[string]any{"session_id": "s", "tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(checkout, "cfg", "config.yaml")}})
	require.NoError(t, err)
	require.NoError(t, HandleAuditLogWithPolicy(bytes.NewReader(payload), p, ""))
	write := map[string]any{"file_path": "cfg/config.yaml"}
	for _, archived := range []bool{false, true} {
		if archived {
			_, err := SealRepoWithPolicy(p, time.Now(), SealOptions{})
			require.NoError(t, err)
			require.NoError(t, os.Remove((audit.Stream{Namespace: audit.SessionNS, Session: "s"}).LivePath(p)))
		}
		reason, deny := gateLocatedContract(contract, "Write", write, "s", loc)
		require.False(t, deny, "archived=%v: %s", archived, reason)
	}
	reason, deny := gateLocatedContract(contract, "Write", map[string]any{"file_path": filepath.Join(store, "cfg", "config.yaml")}, "s", loc)
	require.True(t, deny, "a Read in C must not authorize a different file at S: %s", reason)
}

func (r *blockedHookInput) Read(b []byte) (int, error) {
	if r.entered != nil {
		close(r.entered)
		r.entered = nil
		<-r.resume
	}
	return r.Reader.Read(b)
}

func TestPreToolUseEntrySnapshotSurvivesConfigReplacement(t *testing.T) {
	for _, boundary := range []string{"stdin", "dispatch"} {
		for _, replacement := range []string{"max_delegation_depth: 2\n", "record: invalid\n"} {
			t.Run(boundary+"/"+strings.TrimSpace(replacement), func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				repo := stageRepoRoot(t)
				id := "m-2026-09-08-740"
				stageContract(t, os.Getenv("HOME"), id)
				stageParentDelegationSkeleton(t, repo, "m-2026-09-08-741", "d-ancestor", "")
				t.Setenv("MISSION_ID", id)
				t.Setenv("PARENT_DELEGATION_ID", "d-ancestor")
				t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
				t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
				cfg := filepath.Join(repo, ".punt-labs", "ethos.yaml")
				require.NoError(t, os.WriteFile(cfg, []byte("record: blame\nmax_delegation_depth: 1\n"), 0o600))
				entered, resume := make(chan struct{}), make(chan struct{})
				var reader io.Reader = strings.NewReader(`{"tool_name":"Agent","tool_input":{},"session_id":"snapshot"}`)
				if boundary == "stdin" {
					reader = &blockedHookInput{Reader: reader, entered: entered, resume: resume}
				} else {
					old := dispatchTierBConfirmedOpen
					dispatchTierBConfirmedOpen = func() { close(entered); <-resume }
					t.Cleanup(func() { dispatchTierBConfirmedOpen = old })
				}
				var out bytes.Buffer
				done := make(chan error, 1)
				go func() { done <- HandlePreToolUse(reader, &out) }()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("hook did not reach synchronization boundary")
				}
				err := os.WriteFile(cfg, []byte(replacement), 0o600)
				close(resume)
				require.NoError(t, err)
				require.NoError(t, <-done)
				var result PreToolUseResult
				require.NoError(t, json.Unmarshal(out.Bytes(), &result))
				require.Equal(t, "deny", result.HookSpecificOutput.Decision())
				require.Contains(t, result.HookSpecificOutput.PermissionDecisionReason, "max_delegation_depth 1")
				require.NotContains(t, result.HookSpecificOutput.PermissionDecisionReason, "record")
				paths, err := filepath.Glob(filepath.Join(repo, ".punt-labs", "ethos", "missions", id, "delegations", "*", "record.yaml"))
				require.NoError(t, err)
				require.Len(t, paths, 1)
				d, err := mission.LoadDelegation(paths[0])
				require.NoError(t, err)
				require.Equal(t, mission.DelegationVerdictAborted, d.Verdict)
			})
		}
	}
}

func TestPreToolUseEntryFailureSurvivesConfigRepair(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := stageRepoRoot(t)
	cfg := filepath.Join(repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfg), 0o700))
	require.NoError(t, os.WriteFile(cfg, []byte("record: invalid\n"), 0o600))
	entered, resume := make(chan struct{}), make(chan struct{})
	reader := &blockedHookInput{
		Reader:  strings.NewReader(`{"tool_name":"Agent","tool_input":{},"session_id":"snapshot-error"}`),
		entered: entered,
		resume:  resume,
	}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- HandlePreToolUse(reader, &out) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("hook did not reach stdin boundary")
	}
	err := os.WriteFile(cfg, []byte("record: blame\n"), 0o600)
	close(resume)
	require.NoError(t, err)
	require.NoError(t, <-done)
	var result PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.Equal(t, "deny", result.HookSpecificOutput.Decision())
	require.Contains(t, result.HookSpecificOutput.PermissionDecisionReason, "record")
	require.NotContains(t, result.HookSpecificOutput.AdditionalEnv, "DELEGATION_ID")
}

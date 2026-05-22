package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/resolve"
)

// dispatchAgent handles the PreToolUse branch for `tool_name == "Agent"`.
// DES-054 v5 §"PreToolUse-on-Agent" dispatch rule:
//
//  1. MISSION_ID env set: Tier B by explicit dispatch. Resolve the
//     contract, allocate a delegation_id from the delegations namespace,
//     emit DELEGATION_ID + MISSION_ID + PARENT_SESSION_ID in
//     additional_env, allow. A malformed MISSION_ID (Load fails)
//     surfaces as a block decision with a named reason — never a
//     silent fall-through to Tier A.
//
//  2. MISSION_ID env unset: Tier A. Round-3 advice path preserved
//     unchanged (stderr line, suppression signals honoured). Allocate
//     a delegation_id and emit DELEGATION_ID + PARENT_SESSION_ID in
//     additional_env; MISSION_ID is NOT echoed (there isn't one).
//
// Inheritance dispatch (parent contract walk + spawn_pattern match)
// is out of scope for this round per the contract; that path lands in
// a later mission.
//
// sessionID comes from the hook input's `session_id` field — Claude
// Code populates it on every tool call. An empty sessionID still gets
// echoed as PARENT_SESSION_ID="" so consumers can tell the difference
// between "unset" (Tier A pre-DES-054) and "set to empty" (test
// fixtures); the env block is still emitted.
func dispatchAgent(w io.Writer, sessionID string) error {
	missionID := os.Getenv("MISSION_ID")
	if missionID != "" {
		return dispatchTierB(w, sessionID, missionID)
	}
	return dispatchTierA(w, sessionID)
}

// dispatchTierA emits the round-3 advice line and an env block carrying
// DELEGATION_ID + PARENT_SESSION_ID. The allocation runs even when the
// advice is suppressed — the delegation_id is what binds audit entries
// to this spawn regardless of whether the operator saw the advisory.
func dispatchTierA(w io.Writer, sessionID string) error {
	maybeEmitTierAAdvice(os.Stderr)

	delegationID, release, err := mission.NewID(mission.NamespaceDelegations, time.Now())
	if err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: allocating delegation id: %v", err))
	}
	// Tier A succeeds: the ID is bound to this spawn and the counter
	// stays incremented.
	release(true)

	env := map[string]string{
		"DELEGATION_ID":     delegationID,
		"PARENT_SESSION_ID": sessionID,
	}
	return json.NewEncoder(w).Encode(PreToolUseResult{
		Decision:      "allow",
		Continue:      true,
		AdditionalEnv: env,
	})
}

// dispatchTierB resolves the MISSION_ID into a contract, allocates a
// delegation_id, and emits the env block with DELEGATION_ID echoed
// MISSION_ID and PARENT_SESSION_ID. A Load failure (missing contract,
// validation failure, filename mismatch) surfaces as a block decision
// — no silent fall-through to Tier A.
//
// The contract is loaded primarily as an existence/validity check.
// The full skeleton write (record.yaml + prompt.md) is deferred to a
// later mission; this round only allocates the ID and emits the env.
// The flock helper (AcquireDelegationLock) is wired so the skeleton
// writer can drop in without re-plumbing the hook entry point.
func dispatchTierB(w io.Writer, sessionID, missionID string) error {
	store, err := tierBMissionStore()
	if err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: resolving mission store: %v", err))
	}
	if _, err := store.Load(missionID); err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: resolving MISSION_ID %q: %v", missionID, err))
	}

	delegationID, release, err := mission.NewID(mission.NamespaceDelegations, time.Now())
	if err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: allocating delegation id: %v", err))
	}
	release(true)

	env := map[string]string{
		"DELEGATION_ID":     delegationID,
		"MISSION_ID":        missionID,
		"PARENT_SESSION_ID": sessionID,
	}
	return json.NewEncoder(w).Encode(PreToolUseResult{
		Decision:      "allow",
		Continue:      true,
		AdditionalEnv: env,
	})
}

// tierBMissionStore builds the mission store the dispatch path reads.
// Mirrors cmd/ethos/mission.go's missionStore() but contained in the
// hook package so the PreToolUse entry point stays a single-argument
// (io.Reader, io.Writer) interface — adding deps would force a
// cmd/ethos/hook.go change outside the mission's write_set.
//
// Errors from os.UserHomeDir surface as a block decision rather than
// a silent allow; the hook fails closed when its persistence layer is
// not reachable.
func tierBMissionStore() (*mission.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("user home dir: %w", err)
	}
	root := filepath.Join(home, ".punt-labs", "ethos")
	return mission.NewStore(root).WithRepoRoot(resolve.FindRepoRoot()), nil
}

// writeAgentBlock emits a block decision with a named reason. Used on
// every dispatch-path error so a hook failure is operator-visible
// (the spawn is refused) rather than silently degrading to Tier A.
func writeAgentBlock(w io.Writer, msg string) error {
	return json.NewEncoder(w).Encode(PreToolUseResult{
		Decision: "block",
		Reason:   msg,
	})
}

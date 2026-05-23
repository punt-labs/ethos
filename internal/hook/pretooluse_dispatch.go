package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
//  2. MISSION_ID env unset, PARENT_DELEGATION_ID set: try Tier B by
//     inheritance. Walk the parent_delegation chain; if any ancestor
//     contract carries a Delegations[] entry whose SpawnPattern
//     matches CLAUDE_AGENT_TYPE with InheritsContract=true, the
//     child inherits that ancestor's missionID. Every error along
//     the walk falls through to Tier A — inheritance is non-blocking
//     by design (DES-054 v5 §"PreToolUse-on-Agent" inheritance rule).
//
//  3. MISSION_ID env unset, no parent delegation (or no match in the
//     walk): Tier A. Round-3 advice path preserved unchanged (stderr
//     line, suppression signals honoured). Allocate a delegation_id
//     and emit DELEGATION_ID + PARENT_SESSION_ID in additional_env;
//     MISSION_ID is NOT echoed (there isn't one).
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
	return dispatchTierBOrTierA(w, sessionID)
}

// dispatchTierA emits the round-3 advice line and an env block carrying
// DELEGATION_ID + PARENT_SESSION_ID. The allocation runs even when the
// advice is suppressed — the delegation_id is what binds audit entries
// to this spawn regardless of whether the operator saw the advisory.
func dispatchTierA(w io.Writer, sessionID string) error {
	maybeEmitTierAAdvice(os.Stderr)

	// Tier A is informational and MUST NOT block the spawn. If
	// delegation_id allocation fails, log the failure for audit
	// reconstruction and allow the spawn through with PARENT_SESSION_ID
	// only — losing a DELEGATION_ID degrades audit binding but is
	// preferable to refusing the Agent call (Bugbot HIGH on PR #327;
	// CHANGELOG and pretooluse.go comment both say Tier A returns
	// allow). The counter is rolled back on every non-allow path via
	// the deferred release(success); success flips to true after the
	// JSON response has been encoded.
	success := false
	delegationID, release, err := mission.NewID(mission.NamespaceDelegations, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos pre-tool-use: tier-A allocating delegation id: %v; allowing spawn without DELEGATION_ID\n",
			err)
		env := map[string]string{"PARENT_SESSION_ID": sessionID}
		return json.NewEncoder(w).Encode(PreToolUseResult{
			Decision:      "allow",
			Continue:      true,
			AdditionalEnv: env,
		})
	}
	defer func() { release(success) }()

	env := map[string]string{
		"DELEGATION_ID":        delegationID,
		"PARENT_DELEGATION_ID": delegationID,
		"PARENT_SESSION_ID":    sessionID,
	}
	if err := json.NewEncoder(w).Encode(PreToolUseResult{
		Decision:      "allow",
		Continue:      true,
		AdditionalEnv: env,
	}); err != nil {
		// Response write failed — counter rolls back via the deferred
		// release(false). Surface so the operator can correlate the
		// missing audit entry.
		fmt.Fprintf(os.Stderr,
			"ethos pre-tool-use: tier-A response write: %v\n", err)
		return err
	}
	success = true
	return nil
}

// dispatchTierB resolves the MISSION_ID into a contract, allocates a
// delegation_id, writes the on-disk record skeleton, and emits the
// env block with DELEGATION_ID, MISSION_ID, PARENT_SESSION_ID, and
// MISSION_ARTIFACTS_DIR (the per-delegation directory the worker
// writes results into). A Load failure surfaces as a block decision
// — no silent fall-through to Tier A.
//
// Lock acquisition order (DES-054 v5 concurrency model):
//
//  1. AcquireMissionLock — shared LOCK_SH on the per-mission lock so
//     concurrent Tier B spawns under one mission do not serialize.
//  2. AcquireDelegationLock — exclusive LOCK_EX on the per-delegation
//     lock so the skeleton write is the sole writer for this ID.
//  3. WriteDelegationSkeleton — atomic temp+rename of record.yaml.
//
// Releases run LIFO via defer.
//
// repoRoot resolution uses resolve.FindRepoRoot — when there is no
// enclosing repo (test fixture, ad-hoc invocation), the helper falls
// back to the working directory and the .ethos tree lands there.
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

	delegationID, releaseID, err := mission.NewID(mission.NamespaceDelegations, time.Now())
	if err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: allocating delegation id: %v", err))
	}
	// Deferred rollback: every dispatch failure between NewID and the
	// successful skeleton write must return the counter to its pre-call
	// value so the allocated ID is not burned. success flips to true
	// only after WriteDelegationSkeleton returns nil — every earlier
	// failure path leaves success=false and the deferred release(false)
	// decrements the counter.
	success := false
	defer func() { releaseID(success) }()

	repoRoot := tierBRepoRoot()
	releaseMission, err := mission.AcquireMissionLock(repoRoot, missionID)
	if err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: acquiring mission lock for %q: %v", missionID, err))
	}
	defer releaseMission()

	globalRoot, err := tierBGlobalRoot()
	if err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: resolving global root for delegation lock: %v", err))
	}
	releaseDelegation, err := mission.AcquireDelegationLock(globalRoot, delegationID)
	if err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: acquiring delegation lock for %q: %v", delegationID, err))
	}
	defer releaseDelegation()

	parentDelegation := os.Getenv("PARENT_DELEGATION_ID")
	if _, err := mission.WriteDelegationSkeleton(repoRoot, missionID, delegationID, mission.DelegationSkeleton{
		Tier:             mission.TierB,
		ParentDelegation: parentDelegation,
		ParentSession:    sessionID,
		AgentType:        os.Getenv("CLAUDE_AGENT_TYPE"),
	}); err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: writing delegation skeleton for %q: %v", delegationID, err))
	}
	// Skeleton is now on disk. The delegation_id slot is occupied
	// regardless of whether the downstream depth check or response
	// encode succeeds — rolling the counter back here would let the
	// next NewID return the same delegation_id and collide with the
	// just-written record. Commit the counter (success=true) at this
	// point; failures past here log to stderr but do not rollback
	// (Bugbot HIGH on PR #327 d12ade2: rolling back after the
	// skeleton is on disk enables ID reuse → directory collision).
	success = true

	// Depth gate (DES-054 v5): walk parent_delegation chain and refuse
	// if adding this spawn would exceed the configured ceiling. The
	// skeleton is on disk at this point — the refusal closes it with
	// verdict=aborted so an audit query can distinguish a depth refusal
	// (terminated before the worker started) from a spawn that ran and
	// failed downstream. The walker fails closed on a missing or
	// unparseable ancestor; we refuse rather than silently admit.
	if reason, ok := enforceDelegationDepth(repoRoot, missionID, delegationID, parentDelegation); !ok {
		return writeAgentBlock(w, reason)
	}

	env := map[string]string{
		"DELEGATION_ID":         delegationID,
		"PARENT_DELEGATION_ID":  delegationID,
		"MISSION_ID":            missionID,
		"PARENT_SESSION_ID":     sessionID,
		"MISSION_ARTIFACTS_DIR": mission.DelegationDir(repoRoot, missionID, delegationID),
	}
	if err := json.NewEncoder(w).Encode(PreToolUseResult{
		Decision:      "allow",
		Continue:      true,
		AdditionalEnv: env,
	}); err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos pre-tool-use: tier-B response write: %v\n", err)
		return err
	}
	return nil
}

// enforceDelegationDepth walks the parent_delegation chain for the
// just-written skeleton and reports whether the proposed depth is
// admissible. Returns (reason, false) when the spawn must be refused;
// the reason names the configured limit and the attempted depth so
// an operator sees both at the refusal site. Returns ("", true) when
// the depth is within budget and the spawn may proceed.
//
// Every refusal path closes the just-written skeleton with
// verdict=aborted before returning so the on-disk record reflects
// the operator-visible state — open + abandoned would be a misleading
// post-mortem signal. The three refusal branches are: config
// resolution error (negative or unreadable max_delegation_depth),
// chain-walk error (corrupt or missing ancestor), and depth-exceeds-
// limit. All three call closeDelegationAborted; omitting the close
// on any branch leaks the skeleton at verdict=open.
//
// Loader failures (a corrupt or missing ancestor) surface as a refusal
// rather than a silent admit: a runaway recursive spawn pattern is
// exactly what the depth gate exists to defeat, and silently treating
// a missing ancestor as zero depth would let one through.
func enforceDelegationDepth(repoRoot, missionID, delegationID, parentDelegation string) (string, bool) {
	limit, err := resolve.ResolveMaxDelegationDepth(repoRoot, mission.MaxDelegationDepthDefault)
	if err != nil {
		closeDelegationAborted(repoRoot, missionID, delegationID)
		return fmt.Sprintf(
			"ethos pre-tool-use: resolving max_delegation_depth: %v", err,
		), false
	}
	d := &mission.Delegation{
		ID:               delegationID,
		ParentDelegation: parentDelegation,
	}
	loader := delegationLoader(repoRoot)
	parentDepth, err := mission.DelegationDepth(d, loader, limit)
	if err != nil {
		closeDelegationAborted(repoRoot, missionID, delegationID)
		return fmt.Sprintf(
			"ethos pre-tool-use: walking parent_delegation chain for %q: %v",
			delegationID, err,
		), false
	}
	proposed := parentDepth + 1
	if proposed > limit {
		closeDelegationAborted(repoRoot, missionID, delegationID)
		return fmt.Sprintf(
			"ethos pre-tool-use: max_delegation_depth %d exceeded by depth %d for %q",
			limit, proposed, delegationID,
		), false
	}
	return "", true
}

// delegationLoader returns a loader the depth walker uses to follow
// the parent_delegation chain. The loader scans every mission tree
// under <repo>/.ethos/missions/* for a matching record because Tier B
// inheritance can promote a child under an ancestor's missionID while
// the immediate parent_delegation lives under a different mission. A
// single-mission loader keyed on the inherited missionID fails on the
// parent link in that shape and aborts an otherwise valid spawn
// (Bugbot MED on PR #328: depth gate single-mission loader).
//
// Errors propagate to the depth walker, which treats them as a refusal
// — silently treating a missing ancestor as zero depth would let a
// runaway recursive spawn pattern pass.
func delegationLoader(repoRoot string) func(id string) (*mission.Delegation, error) {
	return func(id string) (*mission.Delegation, error) {
		d, _, err := findDelegationByID(repoRoot, id)
		if err != nil {
			return nil, err
		}
		return d, nil
	}
}

// closeDelegationAborted is the refusal-path helper that stamps the
// just-written skeleton with verdict=aborted. Errors are written to
// stderr because the refusal itself is already on its way to the
// operator via the hook response — a follow-on close failure should
// not mask the original refusal reason.
func closeDelegationAborted(repoRoot, missionID, delegationID string) {
	closedAt := time.Now().UTC().Format(time.RFC3339)
	if err := mission.CloseDelegationSkeleton(
		repoRoot, missionID, delegationID,
		mission.DelegationVerdictAborted, closedAt,
	); err != nil {
		// fs.ErrNotExist on the close path means the skeleton was
		// never written — an order-of-operations bug in the dispatch
		// (depth refusal fired before WriteDelegationSkeleton). The
		// generic close-failure line would hide that distinction;
		// name it explicitly so the operator can find the offending
		// call order in the source.
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr,
				"ethos: pre-tool-use: order-of-operations bug — depth refusal fired but skeleton was never written (delegation=%s mission=%s)\n",
				delegationID, missionID,
			)
			return
		}
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: closing aborted skeleton: %v\n", err,
		)
	}
}

// tierBRepoRoot resolves the enclosing repo root for the Tier B
// skeleton write. Mirrors the resolve used by tierBMissionStore so
// the lock files, record.yaml, and the MISSION_ARTIFACTS_DIR env all
// agree on the same .ethos tree.
//
// Resolution order:
//  1. ETHOS_REPO_ROOT env override
//  2. resolve.FindRepoRoot (walk for .git)
//  3. os.Getwd fallback (logs to stderr; downstream sites defend
//     against an empty return)
//
// The env override means the precondition evaluator's loadSessionReads
// and the dispatch + inheritance paths all resolve to the same tree
// (Bugbot MED on PR #328: previously dispatch + inheritance used
// FindRepoRoot only while audit + preconditions honored the env var,
// allowing the dispatch path to write to a different tree than the
// hook later read).
func tierBRepoRoot() string {
	if root := resolve.EnvRepoRoot(); root != "" {
		return root
	}
	cwd, err := os.Getwd()
	if err != nil {
		// Getwd failure here is rare (deleted cwd, permission loss).
		// Downstream call sites are defensive against the empty
		// return, but a silent fall-through leaves no trace — surface
		// the underlying error so the operator can correlate a
		// downstream "repoRoot is required" with its cause.
		fmt.Fprintf(os.Stderr, "ethos: pre-tool-use: getwd failed: %v\n", err)
		return ""
	}
	return cwd
}

// tierBGlobalRoot resolves the global ethos root used for per-
// delegation lock files. DES-054 v5 §"Storage Layout" requires the
// per-delegation flock to live at <globalRoot>/delegations/<id>.lock
// so two checkouts of the same repo lock the same inode. Errors from
// os.UserHomeDir surface to the caller — the hook fails closed when
// its persistence layer is not reachable.
func tierBGlobalRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return filepath.Join(home, ".punt-labs", "ethos"), nil
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
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	// NewStoreWithRoots activates the DES-054 two-tree dispatch:
	// reads check the repo tree first (<repoRoot>/.ethos/missions/),
	// then fall back to the global tree. WithRepoRoot alone is
	// trace-only and would miss contracts that live in the repo tree
	// (Copilot HIGH-equivalent on PR #327: Tier B dispatch would
	// block "malformed MISSION_ID" on any in-repo contract).
	// Use tierBRepoRoot() (env-aware) so the mission Store walks the
	// same tree as the dispatch + inheritance + audit + preconditions
	// paths (Bugbot MED on PR #328: split repoRoot resolution).
	return mission.NewStoreWithRoots(tierBRepoRoot(), globalRoot), nil
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

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
//
// Sidecar (DES-054 extension, ethos-620t): when MISSION_ID is unset
// the dispatch consults <globalRoot>/sessions/<id>/active-mission. A
// leader-in-Claude-Code session cannot inject MISSION_ID into its own
// env from inside an active session, so the sidecar is the bridge
// from `ethos mission claim` to the next Agent() spawn. The read is
// best-effort: any error logs to stderr and falls through to the
// inheritance / Tier A path, matching the pattern in
// loadParentDelegation.
func dispatchAgent(w io.Writer, sessionID string, toolInput map[string]any) error {
	missionID := os.Getenv("MISSION_ID")
	if missionID != "" {
		return dispatchTierB(w, sessionID, missionID, toolInput)
	}
	if missionID := readActiveMissionForDispatch(sessionID); missionID != "" {
		return dispatchTierB(w, sessionID, missionID, toolInput)
	}
	return dispatchTierBOrTierA(w, sessionID, toolInput)
}

// readActiveMissionForDispatch consults the active-mission sidecar
// for sessionID. Returns "" on any non-found shape: empty sessionID,
// missing global root, missing sidecar, read error. Errors that are
// not "file not present" log to stderr so the operator can trace why
// a claimed mission did not bind — the dispatch then proceeds along
// the no-sidecar path (inheritance or Tier A) so the spawn still
// runs (Bugbot precedent: dispatch helpers must be non-blocking).
//
// A sidecar naming a mission that is no longer open is stale and is
// refused with a warning (ethos-7vo3). The sidecar is cleared when
// the mission closes in the SAME session; a mission closed from
// anywhere else leaves it behind, and filing a fresh delegation under
// a mission whose results are already in is a false audit trail. The
// spawn still runs — as Tier A, where it belongs.
func readActiveMissionForDispatch(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	globalRoot, err := tierBGlobalRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: active-mission: resolving global root: %v; falling through\n",
			err)
		return ""
	}
	missionID, err := mission.ReadActiveMission(globalRoot, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: active-mission: reading sidecar for %q: %v; falling through\n",
			sessionID, err)
		return ""
	}
	if missionID == "" {
		return ""
	}
	if reason := staleBindingReason(missionID); reason != "" {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: active-mission: session %q is bound to %s but %s; "+
				"run `ethos mission claim <id>` (or dispatch the mission you mean) — spawning without a mission\n",
			sessionID, missionID, reason)
		return ""
	}
	return missionID
}

// staleBindingReason reports why the sidecar's mission cannot take a
// new delegation, or "" when it can. Only one shape is stale: a
// contract that loads and is no longer open.
//
// A store or contract that will not resolve is deliberately NOT
// treated as stale. That case belongs to dispatchTierB, which refuses
// the spawn with the missionID named in the reason — an unresolvable
// binding must never silently admit (DES-054 round-3 rule, pinned by
// TestDispatchAgent_ActiveMissionSidecarMalformedRefuses). Returning
// "" here hands it to that path unchanged.
func staleBindingReason(missionID string) string {
	store, err := tierBMissionStore()
	if err != nil {
		return ""
	}
	c, err := store.Load(missionID)
	if err != nil {
		return ""
	}
	return nonOpenReason(c.Status)
}

// nonOpenReason reports why a mission cannot take a new delegation —
// "" when status is open. Shared by case 2's staleBindingReason (the
// active-mission sidecar) and case 1's dispatchTierB (the MISSION_ID
// env var, both on first Load and on the pre-write TOCTOU re-check)
// so every non-open source produces identical wording.
func nonOpenReason(status string) string {
	if status == mission.StatusOpen {
		return ""
	}
	return fmt.Sprintf("that mission is %s", status)
}

// warnNonOpenMissionID writes the case-1 counterpart of
// readActiveMissionForDispatch's stale-sidecar warning: same shape
// (names the session, the mission, and the remedy), worded for an
// explicit MISSION_ID rather than a claimed sidecar.
func warnNonOpenMissionID(sessionID, missionID, reason string) {
	fmt.Fprintf(os.Stderr,
		"ethos: pre-tool-use: MISSION_ID: session %q named %s but %s; "+
			"run `ethos mission claim <id>` (or dispatch the mission you mean) — spawning without a mission\n",
		sessionID, missionID, reason)
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
		return json.NewEncoder(w).Encode(preToolUseAllowWithEnv(env))
	}
	defer func() { release(success) }()

	env := map[string]string{
		"DELEGATION_ID":        delegationID,
		"PARENT_DELEGATION_ID": delegationID,
		"PARENT_SESSION_ID":    sessionID,
	}
	if err := json.NewEncoder(w).Encode(preToolUseAllowWithEnv(env)); err != nil {
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
func dispatchTierB(w io.Writer, sessionID, missionID string, toolInput map[string]any) error {
	store, err := tierBMissionStore()
	if err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: resolving mission store: %v", err))
	}
	c, err := store.Load(missionID)
	if err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: resolving MISSION_ID %q: %v", missionID, err))
	}
	// Case-1 status re-check (docs/design-delegation-lifecycle.md
	// facet 2): a MISSION_ID env value is inherited by ordinary OS
	// process-environment inheritance across every subsequent tool
	// call a resumed subagent process makes — including calls made
	// long after the mission it names has closed. Case 2's
	// staleBindingReason already refuses a stale sidecar; case 1 must
	// refuse identically rather than trust the mere presence of the
	// env var. Non-open falls through to Tier A (or inheritance) —
	// never a blocked spawn, matching every other attribution
	// fallback in this file.
	if reason := nonOpenReason(c.Status); reason != "" {
		warnNonOpenMissionID(sessionID, missionID, reason)
		return dispatchTierBOrTierA(w, sessionID, toolInput)
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

	repoRoot := tierBStoreRoot()
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

	// TOCTOU re-check (docs/design-delegation-lifecycle.md facet 2):
	// re-Load the contract while both AcquireMissionLock (shared) and
	// AcquireDelegationLock (exclusive, just acquired above) are held.
	// Store.Close takes AcquireMissionLockExclusive around its
	// delegation sweep, so a concurrent Close either committed before
	// this shared holder was admitted (caught here) or is blocked
	// waiting for this shared holder to release (and will sweep the
	// skeleton this call is about to write). Either way the mission
	// can never end up closed with an open delegation record this
	// dispatch wrote after the fact. A reload error is not treated as
	// stale — that mirrors staleBindingReason's own rule and defers to
	// whatever the eventual write or a later read surfaces.
	if recheck, err := store.Load(missionID); err == nil {
		if reason := nonOpenReason(recheck.Status); reason != "" {
			warnNonOpenMissionID(sessionID, missionID, reason)
			return dispatchTierBOrTierA(w, sessionID, toolInput)
		}
	}

	parentDelegation := os.Getenv("PARENT_DELEGATION_ID")
	agentType, _ := toolInput["subagent_type"].(string)
	if agentType == "" {
		agentType = os.Getenv("CLAUDE_AGENT_TYPE")
	}
	promptBody, _ := toolInput["prompt"].(string)
	if _, err := mission.WriteDelegationSkeleton(repoRoot, missionID, delegationID, mission.DelegationSkeleton{
		Tier:             mission.TierB,
		ParentDelegation: parentDelegation,
		ParentSession:    sessionID,
		AgentType:        agentType,
		Prompt:           []byte(promptBody),
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

	// Write the delegation-binding sidecar so the PostToolUse audit
	// writer can tag subagent tool calls with delegation_id +
	// mission_id. additional_env from PreToolUse does NOT persist into
	// hook script processes, so this sidecar is the bridge. Non-fatal:
	// a write failure is a traceability degradation, not a spawn
	// refusal.
	if globalRoot, gErr := tierBGlobalRoot(); gErr == nil {
		if wErr := mission.WriteDelegationBinding(globalRoot, sessionID, mission.DelegationBinding{
			DelegationID:  delegationID,
			MissionID:     missionID,
			ParentSession: sessionID,
		}); wErr != nil {
			fmt.Fprintf(os.Stderr,
				"ethos: pre-tool-use: writing delegation binding: %v\n", wErr)
		}
	}

	env := map[string]string{
		"DELEGATION_ID":         delegationID,
		"PARENT_DELEGATION_ID":  delegationID,
		"MISSION_ID":            missionID,
		"PARENT_SESSION_ID":     sessionID,
		"MISSION_ARTIFACTS_DIR": mission.DelegationDir(repoRoot, missionID, delegationID),
	}
	if err := json.NewEncoder(w).Encode(preToolUseAllowWithEnv(env)); err != nil {
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
// under <repo>/.punt-labs/ethos/missions/* for a matching record because Tier B
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

// tierBStoreRoot resolves the repo whose .punt-labs/ethos mission store the
// Tier B dispatch reads and writes — the contract Load, the per-mission
// lock, the delegation skeleton (record.yaml), the MISSION_ARTIFACTS_DIR
// env, the depth-walk scan, and the aborted-close. It resolves through the
// git common dir (resolve.StoreRepoRoot) so a leader dispatching from a
// linked worktree writes and reads the SAME store the CLI's `mission create`
// wrote to (the main work tree), not the worktree's empty tree. Without this
// the CLI and the dispatch hook disagree and delegation is refused with
// "resolving MISSION_ID not found" — the core ethos-yofr symptom (CR#1).
//
// It returns exactly what StoreRepoRoot returns, INCLUDING "" — it must NOT
// bare-Getwd-fall-back the way tierBRepoRoot does. StoreRepoRoot returns ""
// in two cases: no git repo at all, and (crucially) a set-but-invalid
// ETHOS_REPO_ROOT, where F1's repoRootOverride deliberately returns "" so
// the caller does not resolve some other tree. Substituting the raw cwd here
// would, inside a worktree under a bad override, point the store at the
// worktree's own tree — reintroducing ethos-yofr behind a bad override
// (code-review round 2). On "", NewStoreWithRoots falls back to the global
// tree (a warned, known state) for reads, and the write path
// (AcquireMissionLock, WriteDelegationSkeleton) fails loud on the empty
// repoRoot — never a silent worktree-local write.
func tierBStoreRoot() string {
	return resolve.StoreRepoRoot()
}

// tierBRepoRoot resolves the current work tree root for the per-checkout
// portions of the hook — the audit/precondition read path (via
// preconditions.go's envRepoRoot fallback), whose entries live in the
// committing checkout, not the shared store. Store portions use
// tierBStoreRoot instead (CR#1).
//
// Resolution order:
//  1. ETHOS_REPO_ROOT env override
//  2. resolve.FindRepoRoot (walk for .git)
//  3. os.Getwd fallback (logs to stderr; downstream sites defend
//     against an empty return)
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
	// reads check the repo tree first (<repoRoot>/.punt-labs/ethos/missions/),
	// then fall back to the global tree. WithRepoRoot alone is
	// trace-only and would miss contracts that live in the repo tree
	// (Copilot HIGH-equivalent on PR #327: Tier B dispatch would
	// block "malformed MISSION_ID" on any in-repo contract).
	// Use tierBStoreRoot() so the mission Store walks the same tree as the
	// dispatch skeleton write and the CLI's `mission create` — the main work
	// tree in a linked worktree (CR#1). Without this the dispatch hook reads
	// a different store than the CLI wrote and refuses the spawn.
	//
	// WithCheckoutRoot keeps the two-root invariant uniform across every
	// mission Store: the record resolves to the store root, the DES-058 audit
	// zone to the checkout (tierBRepoRoot). The dispatch path only Loads the
	// contract today (no event append/read), so this is defensive — but it
	// means a future audit access here cannot silently route to the store tree
	// (PR #370 sweep).
	//
	// The checkout root FOLLOWS the store root to "" when the store refuses:
	// tierBStoreRoot is StoreRepoRoot (fail-closed on a bad ETHOS_REPO_ROOT),
	// but tierBRepoRoot (EnvRepoRoot, requireStore=false) re-accepts the
	// refused path. Pairing them unconditionally would let the audit zone
	// resurrect a path the store already refused (PR #370 override-resurrection
	// class, same guard as missionCheckoutRoot + runUI).
	storeRoot := tierBStoreRoot()
	checkoutRoot := ""
	if storeRoot != "" {
		checkoutRoot = tierBRepoRoot()
	}
	return mission.NewStoreWithRoots(storeRoot, globalRoot).
		WithCheckoutRoot(checkoutRoot), nil
}

// writeAgentBlock emits a block decision with a named reason. Used on
// every dispatch-path error so a hook failure is operator-visible
// (the spawn is refused) rather than silently degrading to Tier A.
func writeAgentBlock(w io.Writer, msg string) error {
	return json.NewEncoder(w).Encode(preToolUseDeny(msg))
}

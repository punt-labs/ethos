package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/resolve"
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
// Sidecars (DES-054 extension, ethos-620t; DES-076 round 3): when
// MISSION_ID is unset the dispatch consults two independent stores
// under <globalRoot>/sessions/<id>/ — the single-slot active-mission
// claim, and the per-mission dispatch-pending directory. A
// leader-in-Claude-Code session cannot inject MISSION_ID into its own
// env from inside an active session, so these are the bridge from
// `ethos mission claim`/`dispatch` to a later Agent() spawn. Every read
// is best-effort: an error logs to stderr and falls through to the
// inheritance / Tier A path, matching the pattern in loadParentDelegation.
//
// DES-076 round 1 scoped a dispatch binding to the ONE spawn whose
// agent type matches the contract's declared Worker, single-use. Round
// 3 (review findings C1-C3, m-2026-09-08-004 round 2) moved that
// binding off the single active-mission slot entirely, into its own
// per-mission pending-dispatch store (internal/mission/active.go) — a
// single overwritable slot could hold only one pending dispatch at a
// time, so a second `dispatch --worker bwk` before the first spawn
// silently discarded the first mission's binding, guaranteeing a
// misattribution the moment two missions shared a Worker handle (the
// NORMAL case in this repo, where one specialist handle serves every
// mission in its domain). See active.go's doc comment on the
// dispatch-pending primitives for the full decision.
//
// A claim (`ethos mission claim`) is unaffected by any of this: it
// stays on its own single sticky slot, ungated by agent type, until an
// explicit claim or release. Pending dispatches are checked FIRST,
// because a dispatch is a more specific, more recent instruction than
// a standing claim — this mirrors the pre-round-3 behavior where a
// fresh dispatch always took precedence over an existing claim
// (previously by overwriting the shared slot; now via separate,
// non-destructive storage that lets both coexist).
func dispatchAgent(w io.Writer, sessionID string, toolInput map[string]any) error {
	// Review probe finding F3 (m-2026-09-08-004 round 2): two concurrent
	// Agent() tool calls in the same session — a normal shape, this
	// org's own conventions call for batching independent tool calls in
	// one turn — could both match the SAME oldest pending-dispatch
	// entry before either consumed it. The lock is held across the
	// ENTIRE match-through-admit-or-fall-back sequence, not just the
	// read: releasing it before dispatchTierB runs would still let a
	// second waiter's read interleave with the first caller's
	// still-pending consume decision. See AcquireDispatchPendingLock's
	// own doc comment for why this introduces no new deadlock risk.
	//
	// Review finding P1 (qodo #2, full-branch review of PR #509,
	// m-2026-09-08-004 round 3): the explicit-MISSION_ID branch used to
	// return before this lock was even resolved, so it never consumed a
	// pending-dispatch entry matching missionID — but explicit
	// MISSION_ID is a SUPPORTED override (matchDispatchPending's own
	// ambiguity warning tells the operator to set it to pick a
	// DIFFERENT pending mission than FIFO would), so following our own
	// advice left the entry live to capture the next matching-worker
	// spawn too. The lock is now acquired unconditionally, before
	// either branch, so both the explicit and the matched path can
	// consume under it.
	globalRoot, err := tierBGlobalRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: dispatch-pending: resolving global root: %v; "+
				"falling through without pending-dispatch matching\n", err)
		return dispatchTierBOrTierA(w, sessionID, toolInput)
	}
	release, err := mission.AcquireDispatchPendingLock(globalRoot, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: dispatch-pending: acquiring lock for %q: %v; "+
				"falling through without pending-dispatch matching\n", sessionID, err)
		return dispatchTierBOrTierA(w, sessionID, toolInput)
	}
	defer release()

	if missionID := os.Getenv("MISSION_ID"); missionID != "" {
		return dispatchTierB(w, sessionID, missionID, toolInput, mission.BoundViaMissionIDEnv,
			func() { consumeDispatchBinding(sessionID, missionID) })
	}
	agentType := spawnAgentType(toolInput)

	if missionID, boundVia := readActiveMissionForDispatch(sessionID, agentType); missionID != "" {
		var onDispatched func()
		if boundVia == mission.BoundViaSidecarDispatch {
			onDispatched = func() { consumeDispatchBinding(sessionID, missionID) }
		}
		return dispatchTierB(w, sessionID, missionID, toolInput, boundVia, onDispatched)
	}
	return dispatchTierBOrTierA(w, sessionID, toolInput)
}

// spawnAgentType reports the agent type this Agent() call is spawning:
// the tool_input's subagent_type when present, else CLAUDE_AGENT_TYPE.
// Shared by the pending-dispatch Worker-match gate
// (readActiveMissionForDispatch/matchDispatchPending) and dispatchTierB's
// own delegation-skeleton write, so both see the same answer for the
// same spawn (review finding C5, m-2026-09-08-004 round 2 — verified by
// grep that no second inline copy of this logic remains anywhere in
// this package).
func spawnAgentType(toolInput map[string]any) string {
	agentType, _ := toolInput["subagent_type"].(string)
	if agentType == "" {
		agentType = os.Getenv("CLAUDE_AGENT_TYPE")
	}
	return agentType
}

// readActiveMissionForDispatch reports whether agentType's spawn may
// bind to a pending dispatch or an active claim for sessionID, and
// which.
//
// Returns ("", "") on any non-found or non-usable shape: empty
// sessionID, missing global root, no pending dispatch matching
// agentType, no claim, or a claimed mission that is no longer open
// (ethos-7vo3 — a fresh warning to stderr names why). Errors that are
// not "file not present" log to stderr so the operator can trace why a
// bound mission did not take the spawn — the dispatch then proceeds
// along the no-match path (inheritance or Tier A) so the spawn still
// runs (Bugbot precedent: dispatch helpers must be non-blocking).
//
// boundVia names how the returned missionID may be attributed
// (mission.BoundViaSidecarClaim or mission.BoundViaSidecarDispatch),
// for the caller to both decide consumption and to stamp the
// provenance field on the delegation record it writes.
func readActiveMissionForDispatch(sessionID, agentType string) (missionID, boundVia string) {
	if sessionID == "" {
		return "", ""
	}
	globalRoot, err := tierBGlobalRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: active-mission: resolving global root: %v; falling through\n",
			err)
		return "", ""
	}

	// 1. Pending dispatches, oldest matching entry first. No contract
	// Load is needed to match — the Worker was recorded directly in the
	// pending file at dispatch time (DES-076 round 3, C3) — so a
	// resolution failure past this point (the matched mission's
	// contract cannot Load, or is no longer open) is handled entirely
	// by dispatchTierB's own existing Load-and-block / non-open-fallback
	// gates, exactly like an explicit MISSION_ID naming an unloadable or
	// closed contract. That is a deliberate doctrine change from round
	// 1: round 1's "never block the ambient bridge" applied to the
	// PRE-match uncertainty of a Load-dependent matcher; once a match is
	// certain without a Load, a subsequently-unloadable contract is a
	// genuine, actionable problem for THIS specific spawn, not ambient
	// noise behind every spawn in the session.
	if pendingMissionID := matchDispatchPending(globalRoot, sessionID, agentType); pendingMissionID != "" {
		return pendingMissionID, mission.BoundViaSidecarDispatch
	}

	// 2. Claim: sticky, unconditional, unaffected by Worker matching.
	// Nothing but `ethos mission claim` writes a fresh, clean binding to
	// this sidecar as of DES-076 round 3 (dispatch moved to its own
	// store above) — but "nothing writes a bad one on purpose" is not
	// the same as "a bad one cannot exist": ReadActiveMissionBinding
	// (active.go) can still return BindOriginUnknown for a truncated or
	// mismatched origin file (a partial write, a mixed-binary window, a
	// hand-inspected legacy sidecar) — review finding C2, m-2026-09-08-004
	// round 2. That case is refused here explicitly, not treated as a
	// claim just because dispatch no longer writes here on purpose:
	// DES-076 made claim the PERMISSIVE origin (ungated by Worker, and
	// per commit_trailers.go's gate the only origin that stamps commit
	// trailers), so answering "claim" for ambiguous evidence would both
	// mis-capture a spawn and turn on trailers for a binding the
	// operator never explicitly claimed.
	binding, err := mission.ReadActiveMissionBinding(globalRoot, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: active-mission: reading sidecar for %q: %v; falling through\n",
			sessionID, err)
		return "", ""
	}
	if binding.MissionID == "" {
		return "", ""
	}
	if binding.Origin != mission.BindOriginClaim {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: active-mission: session %q has an ambiguous binding to %s "+
				"(origin %q, not a clean claim) — spawning without a mission; run `ethos mission "+
				"claim <id>` (or `ethos mission release`) to resolve it\n",
			sessionID, binding.MissionID, binding.Origin)
		return "", ""
	}
	if reason := staleBindingReason(binding.MissionID); reason != "" {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: active-mission: session %q is bound to %s but %s; "+
				"run `ethos mission claim <id>` (or `ethos mission release`) — spawning without a mission\n",
			sessionID, binding.MissionID, reason)
		return "", ""
	}
	return binding.MissionID, mission.BoundViaSidecarClaim
}

// matchDispatchPending scans sessionID's pending dispatches
// (internal/mission/active.go's ReadDispatchPending, oldest first) for
// the first entry whose recorded Worker equals agentType, and returns
// its mission ID. Returns "" on any non-match: no pending dispatches,
// none matching, or a read failure (logged to stderr, non-blocking —
// matching the discipline every sidecar reader in this package
// follows). Caller must hold AcquireDispatchPendingLock for the whole
// match-through-admit sequence (review probe F3, m-2026-09-08-004
// round 2) — this function does no locking of its own.
//
// A matching entry whose mission is PROVABLY non-open (Load succeeds
// and the status is not "open") is skipped AND cleared, not returned:
// review probe F2 (m-2026-09-08-004 round 2) demonstrated that leaving
// a stale entry in place permanently head-of-line-blocks every NEWER
// pending dispatch for the same Worker, since FIFO always re-selects
// the oldest entry first. A closed/failed/escalated/abandoned mission
// can never legitimately take a new delegation, so clearing its stale
// entry here is not a heuristic guess — it is the same
// nonOpenReason check dispatchTierB itself would apply, just run
// before committing to a doomed match instead of after.
//
// A matching entry whose mission FAILS TO LOAD is a DIFFERENT case,
// handled differently (review finding K1, full-branch review of
// m-2026-09-08-004 round 3): a Load failure proves nothing — the
// contract is git-tracked, so `mission dispatch` followed by `git
// checkout` to a branch without it is a reachable, non-exotic way to
// make one disappear and later reappear — so C3's doctrine still
// forbids deleting it on unproven evidence. But round 3's original
// behavior (fold it into `candidates` and return it as the match
// whenever no OTHER open entry outranks it) meant an unloadable entry
// at the head of the queue denied every subsequent same-worker spawn
// FOREVER, since it is always the oldest and FIFO always re-selects the
// oldest: a newer, perfectly valid pending dispatch for the same worker
// was unreachable behind it. The entry is now SKIPPED (excluded from
// `candidates`, loop continues to the next entry) but never cleared —
// it can still resolve on its own (a later branch switch, a retried
// write) and can always be cleared explicitly via `ethos mission
// release`, which needs no Load at all (ClearDispatchPending is a pure
// filesystem removal). A skip-only entry that is the LAST word — no
// other entry matches — still yields no match here, so the spawn falls
// through to Tier A/B; dispatchTierB is never reached for it and never
// gets a chance to name the failure, so this function names it
// directly instead.
//
// "It can still resolve on its own" is not purely a benefit (review
// finding J6, full-branch review of m-2026-09-08-004 round 3): the same
// property that lets a transient failure heal itself also lets a
// TRULY stale entry re-enter the misattribution class DES-076 exists to
// prevent, via oscillation rather than a single bad match. Sequence:
// dispatch `m-A` on branch X, `git checkout main` (the contract is gone
// — the entry is unresolvable, this spawn falls through unbound), `git
// checkout X` again (the contract is back — the entry is resolvable
// again) — the NEXT `bwk` spawn now matches `m-A`, even though it has
// nothing to do with `m-A` and the operator has moved on. Skip-not-clear
// trades "permanent denial" (K1's bug) for "eventual, silent,
// re-triggerable misattribution" rather than eliminating the hazard
// outright; `ethos mission release` is still the only positive remedy,
// and it must be run BEFORE switching back to a branch that resurrects
// a stale entry, not after.
//
// When two or more LIVE (non-stale, resolvable) entries match
// agentType, the oldest wins by FIFO, but that is a silent,
// unresolvable ambiguity for the operator unless it is named: two
// missions can legitimately share one Worker handle (this repo's own
// team assigns one specialist to every mission in its domain), and the
// spawn now landing on THIS one instead of THAT one is exactly the
// misattribution class DES-076 exists to prevent.
// warnDispatchPendingAmbiguity emits that signal — only for a
// two-or-more-match tie, never for a lone match, and never merely
// because OTHER pending entries exist for a different Worker (a
// session dispatching both `bwk` and `rmh` work is not ambiguous for a
// `bwk` spawn).
func matchDispatchPending(globalRoot, sessionID, agentType string) string {
	// J1 (full-branch review, m-2026-09-08-004 round 3): classification
	// now runs through mission.ClassifyPendingDispatches, the SAME
	// function the CLI/MCP dispatch-time queue-position advisory calls
	// (via hook.DispatchBoundMessage below) — a store construction
	// failure here is reported the same way a read failure always was
	// in this function, non-blocking.
	store, err := tierBMissionStore()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: dispatch-pending: resolving mission store: %v; falling through\n", err)
		return ""
	}
	classified, warnings, err := mission.ClassifyPendingDispatches(store, globalRoot, sessionID, agentType)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: dispatch-pending: reading for %q: %v; falling through\n",
			sessionID, err)
		return ""
	}
	for _, warning := range warnings {
		fmt.Fprintf(os.Stderr, "ethos: pre-tool-use: dispatch-pending: %s\n", warning)
	}
	var candidates []string
	for _, entry := range classified {
		switch entry.Status {
		case mission.PendingEntryStale:
			fmt.Fprintf(os.Stderr,
				"ethos: pre-tool-use: dispatch-pending: session %q's pending dispatch to %s is "+
					"stale (%s); clearing it so it cannot block a newer pending dispatch\n",
				sessionID, entry.MissionID, entry.Reason)
			if clearErr := mission.ConsumeDispatchPending(globalRoot, sessionID, entry.MissionID); clearErr != nil {
				fmt.Fprintf(os.Stderr,
					"ethos: pre-tool-use: dispatch-pending: clearing stale entry for %q: %v\n",
					entry.MissionID, clearErr)
			}
			continue
		case mission.PendingEntryUnresolvable:
			fmt.Fprintf(os.Stderr,
				"ethos: pre-tool-use: dispatch-pending: session %q's pending dispatch to %s could "+
					"not be resolved (%s); skipping it (not clearing it — the failure is not proof "+
					"the mission is gone for good) so it cannot block a newer pending dispatch for "+
					"worker %q; run `ethos mission release` if it is stuck for good\n",
				sessionID, entry.MissionID, entry.Reason, agentType)
			continue
		default: // mission.PendingEntryOpen
			candidates = append(candidates, entry.MissionID)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) > 1 {
		warnDispatchPendingAmbiguity(agentType, candidates)
	}
	return candidates[0]
}

// warnDispatchPendingAmbiguity names a genuine multi-match ambiguity in
// matchDispatchPending's candidate set: two or more live pending
// dispatches recorded the same Worker, so FIFO's oldest-wins tiebreak is
// resolving a real conflict rather than picking among options that all
// mean the same thing. Names the count, the worker, every competing
// mission ID, which one FIFO chose, and that MISSION_ID overrides the
// match entirely — the one lever that actually lets the operator pick a
// different one of the competing candidates for this specific spawn.
func warnDispatchPendingAmbiguity(agentType string, candidates []string) {
	fmt.Fprintf(os.Stderr,
		"ethos: pre-tool-use: dispatch-pending: %d pending dispatches match worker %q (%s); "+
			"binding this spawn to the oldest (%s). If this spawn is for a different mission, "+
			"set MISSION_ID explicitly.\n",
		len(candidates), agentType, strings.Join(candidates, ", "), candidates[0])
}

// DispatchBoundMessage builds the advisory line `mission dispatch`/
// `mission create` prints (CLI, via cmd/ethos/mission.go's
// bindDispatchedMission) or returns (MCP, via
// internal/mcp/mission_tools.go's bindDispatchedMission) immediately
// after writing a new pending-dispatch entry, naming missionID's actual
// queue position among other pending dispatches for worker — not an
// assumed "next spawn" position (review finding K8, corrected by J1:
// full-branch review, m-2026-09-08-004 round 3).
//
// Exported so both the CLI and MCP surfaces call this ONE
// implementation instead of maintaining two independently-drifting
// copies (K8 already found the wording stale in both places at once;
// J1 found the underlying classification logic diverged from
// matchDispatchPending's own, in both copies, the same way). It shares
// mission.ClassifyPendingDispatches with matchDispatchPending, so the
// reported position and the entry the hook would actually match at
// spawn time cannot disagree: an unresolvable or stale entry ahead of
// missionID is never counted as "ahead of it," because
// matchDispatchPending would skip it too.
//
// remedy is the caller's own escape-hatch wording (the CLI and MCP
// phrasings differ slightly — "run `ethos mission ...`" vs. "call
// mission ..." — which is cosmetic, not logic, so it stays a parameter
// rather than being duplicated here). A store or read failure falls
// back to the unconditional "will attribute worker's next matching
// spawn" wording rather than blocking or omitting the advisory — this
// line is best-effort visibility, never a gate.
func DispatchBoundMessage(store *mission.Store, globalRoot, sessionID, missionID, worker, remedy string) string {
	unconditional := fmt.Sprintf(
		"session %s will attribute worker %q's next matching Agent() spawn to %s; %s",
		sessionID, worker, missionID, remedy)
	classified, _, err := mission.ClassifyPendingDispatches(store, globalRoot, sessionID, worker)
	if err != nil {
		return unconditional
	}
	var live []string
	for _, c := range classified {
		if c.Status == mission.PendingEntryOpen {
			live = append(live, c.MissionID)
		}
	}
	for i, id := range live {
		if id != missionID {
			continue
		}
		if i == 0 {
			return unconditional
		}
		return fmt.Sprintf(
			"session %s queued a pending dispatch of worker %q to %s, but %d pending dispatch(es) "+
				"for %q are ahead of it and will be matched first (%s); %s",
			sessionID, worker, missionID, i, worker, strings.Join(live[:i], ", "), remedy)
	}
	// missionID itself did not classify as open (should not happen
	// right after a successful WriteDispatchPending, but fall back
	// rather than claim a queue position for an entry we cannot find
	// among the live ones).
	return unconditional
}

// consumeDispatchBinding removes missionID's pending-dispatch entry
// after it has been consumed by its matching worker spawn (DES-076):
// each pending dispatch is single-use, so it must not linger to
// capture a later, different spawn of the same Worker.
//
// Unlike the pre-round-3 active-mission slot, this is a single-file
// removal (internal/mission/active.go's ConsumeDispatchPending) — no
// paired-file consistency question a partial failure could leave
// mismatched (review finding C2's class does not apply here at all).
//
// Advisory, non-blocking, but LOUD about the consequence: a transient
// failure means the very next matching-worker spawn is also captured
// before a retry clears it; a PERSISTENT failure (EACCES, a read-only
// sessions dir, a full disk) leaves the entry capturing every future
// spawn of this Worker for as long as the underlying condition holds
// (review finding F4/C9, unbounded, not "one extra spawn"). The
// mission's own `close`/`abandon` also clears its own entry precisely
// (ClearMissionBindings), and `ethos mission release` clears every
// pending dispatch in the session unconditionally — but review finding
// C9's addendum (m-2026-09-08-004 round 2) is the reason neither is
// named here as a GUARANTEED fix: both remove the same file through the
// same os.Remove call this function's own failure came from, so a truly
// persistent condition (not a transient contention blip) defeats all
// three identically. The genuine remedy in that case is fixing the
// underlying filesystem condition directly, not retrying a different
// ethos command that shares the same failure mode.
func consumeDispatchBinding(sessionID, missionID string) {
	globalRoot, err := tierBGlobalRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: dispatch-pending: resolving global root to consume %q: %v; "+
				"the pending entry for %q was NOT cleared and will capture the next matching-worker "+
				"spawn too; run `ethos mission release` to clear it\n",
			missionID, missionID, err)
		return
	}
	if err := mission.ConsumeDispatchPending(globalRoot, sessionID, missionID); err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: dispatch-pending: clearing %q: %v; the pending entry was NOT "+
				"cleared and will capture the next matching-worker spawn too; run `ethos mission "+
				"release` to clear it\n",
			missionID, err)
	}
}

// staleBindingReason reports why the sidecar's mission cannot take a
// new delegation, or "" when it can. Only one shape is stale: a
// contract that loads and is no longer open.
//
// Since DES-076 round 3 this is called ONLY for a CLAIM-origin binding
// (readActiveMissionForDispatch's claim branch); dispatch-pending
// matching no longer calls it at all — see matchDispatchPending's own
// doc comment for why a Load is not needed to match a pending dispatch.
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

// missionResolutionFailedMessage builds dispatchTierB's block message for
// the initial store.Load(missionID) failure, worded per boundVia (review
// finding H1, full-branch review of m-2026-09-08-004 round 3): a spawn
// that matched a pending dispatch is blocked by a clearable sidecar file,
// not by an environment variable, and the operator needs to be told that
// difference and the worker/session it names or the block reads as an
// unrecoverable internal error with no path forward.
func missionResolutionFailedMessage(sessionID, missionID, agentType, boundVia string, err error) string {
	switch boundVia {
	case mission.BoundViaSidecarDispatch:
		return fmt.Sprintf(
			"ethos pre-tool-use: session %q's pending dispatch bound worker %q to mission %s, "+
				"but that mission failed to load: %v; run `ethos mission release` to clear the "+
				"pending dispatch (or `ethos mission show %s` to inspect it)",
			sessionID, agentType, missionID, err, missionID)
	case mission.BoundViaSidecarClaim:
		return fmt.Sprintf(
			"ethos pre-tool-use: session %q is claimed to mission %s, but that mission failed to "+
				"load: %v; run `ethos mission release` to clear the claim",
			sessionID, missionID, err)
	default:
		return fmt.Sprintf("ethos pre-tool-use: resolving MISSION_ID %q: %v", missionID, err)
	}
}

// warnNonOpenMission writes dispatchTierB's non-open-status advisory,
// worded per boundVia so the remedy named actually clears the source that
// produced missionID (review finding H1, full-branch review of
// m-2026-09-08-004 round 3).
//
// mission.BoundViaMissionIDEnv and mission.BoundViaInherited are NOT
// backed by a clearable sidecar — the former is an OS environment
// variable inherited by every later tool call a resumed subagent process
// makes, the latter is the parent_delegation inheritance walk — so
// `ethos mission release` would not change either source and would be a
// false remedy for them (review finding C13, m-2026-09-08-004 round 2).
// mission.BoundViaSidecarClaim and mission.BoundViaSidecarDispatch ARE
// sidecar files `ethos mission release` clears, so both name it, and the
// dispatch case additionally names the worker and the more targeted
// `ethos mission close`/`abandon` remedy that clears only this one entry
// (see consumeDispatchBinding's doc comment on that same scoping).
func warnNonOpenMission(sessionID, missionID, agentType, boundVia, reason string) {
	switch boundVia {
	case mission.BoundViaSidecarDispatch:
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: dispatch-pending: session %q's pending dispatch bound worker %q "+
				"to mission %s but %s; run `ethos mission release` (clears every pending dispatch in "+
				"this session) or `ethos mission close %s`/`abandon %s` (clears just this one) — "+
				"spawning without a mission\n",
			sessionID, agentType, missionID, reason, missionID, missionID)
	case mission.BoundViaSidecarClaim:
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: active-mission: session %q is claimed to mission %s but %s; "+
				"run `ethos mission release` — spawning without a mission\n",
			sessionID, missionID, reason)
	default:
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: MISSION_ID: session %q named %s but %s; "+
				"run `ethos mission claim <id>` (or dispatch the mission you mean) — spawning without a mission\n",
			sessionID, missionID, reason)
	}
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

// dispatchTierBConfirmedOpen is a test-only synchronization hook,
// invoked immediately after dispatchTierB's status check confirms the
// mission is open, right before the call that blocks acquiring the
// shared mission lock. Its zero value is a no-op with negligible
// production cost; tests that need to race a concurrent Close against
// this exact moment override it to signal a channel, replacing a
// blind time.Sleep guess with a real synchronization point (round-2
// re-review finding #6 on the delegation-lifecycle TOCTOU test).
var dispatchTierBConfirmedOpen = func() {}

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
//
// boundVia is stamped onto the delegation skeleton's BoundVia field
// (DES-076 round 2) — one of the mission.BoundVia* constants naming
// which of the three admission paths (explicit MISSION_ID env, parent
// inheritance, or active-mission sidecar consumption) produced this
// spawn. It is the fact `mission abandon --disclaim` later checks
// mechanically, so every caller must pass the value that actually
// describes how IT resolved missionID — never a guess or a shared
// default.
//
// onDispatched, when non-nil, runs exactly once — after the spawn is
// fully admitted (the JSON response has been encoded), never on a
// refusal or an internal fall-through to Tier A/B. DES-076 uses this to
// consume a one-shot active-mission dispatch binding only once its
// matching spawn has actually gone through; a blocked or failed spawn
// leaves the binding in place so a retry of the same call can still
// find it. Every caller but the active-mission sidecar's matching-spawn
// path passes nil.
func dispatchTierB(w io.Writer, sessionID, missionID string, toolInput map[string]any, boundVia string, onDispatched func()) error {
	agentType := spawnAgentType(toolInput)
	store, err := tierBMissionStore()
	if err != nil {
		return writeAgentBlock(w,
			fmt.Sprintf("ethos pre-tool-use: resolving mission store: %v", err))
	}
	c, err := store.Load(missionID)
	if err != nil {
		return writeAgentBlock(w, missionResolutionFailedMessage(sessionID, missionID, agentType, boundVia, err))
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
		warnNonOpenMission(sessionID, missionID, agentType, boundVia, reason)
		return dispatchTierBOrTierA(w, sessionID, toolInput) // status re-check fallback: never consumes onDispatched
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
	dispatchTierBConfirmedOpen()
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
	// dispatch wrote after the fact.
	//
	// A reload error falls through to Tier A/B fallback rather than to
	// WriteDelegationSkeleton — asymmetric treatment of the recheck vs.
	// the first Load above (which blocks the spawn outright on error)
	// would let an error the first check would have refused silently
	// authorize the write here. Neither Load failing is evidence the
	// mission is open; only a successful Load reporting status: open is.
	recheck, err := store.Load(missionID)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: MISSION_ID: session %q, mission %s: TOCTOU re-check Load failed: %v; "+
				"falling through to Tier A/B fallback rather than writing a delegation on unverified status\n",
			sessionID, missionID, err)
		return dispatchTierBOrTierA(w, sessionID, toolInput)
	}
	if reason := nonOpenReason(recheck.Status); reason != "" {
		warnNonOpenMission(sessionID, missionID, agentType, boundVia, reason)
		return dispatchTierBOrTierA(w, sessionID, toolInput)
	}

	parentDelegation := os.Getenv("PARENT_DELEGATION_ID")
	promptBody, _ := toolInput["prompt"].(string)
	if _, err := mission.WriteDelegationSkeleton(repoRoot, missionID, delegationID, mission.DelegationSkeleton{
		Tier:             mission.TierB,
		ParentDelegation: parentDelegation,
		ParentSession:    sessionID,
		AgentType:        agentType,
		Prompt:           []byte(promptBody),
		BoundVia:         boundVia,
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
	// The spawn is now fully admitted as Tier B — the one point DES-076
	// treats as "this dispatch actually happened," and the only point
	// from which onDispatched runs.
	if onDispatched != nil {
		onDispatched()
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

package mission

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Active-mission sidecar.
//
// The leader's Claude Code process cannot export MISSION_ID into its
// own env from inside an active session, so a Tier B dispatch from an
// in-session Agent() call has no way to discover the mission via the
// environment alone. The sidecar is the smallest-blast-radius bridge:
// one file at a known per-session path, one best-effort read in the
// dispatch hook, no changes to Claude Code's tool surface.
//
// Path: <globalRoot>/sessions/<session-id>/active-mission
// File mode: 0o600. Parent dir mode: 0o700.
// Content: the mission ID as plain text (trailing newline tolerated).
//
// Helpers refuse to operate when sessionID is empty so an unknown
// session cannot accidentally write a sidecar at <globalRoot>/sessions//
// active-mission.

// ActiveMissionPath returns the absolute path to the active-mission
// sidecar for sessionID under globalRoot. Returns an empty string when
// either argument is empty — callers treat that as "no path to act on".
func ActiveMissionPath(globalRoot, sessionID string) string {
	if globalRoot == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(globalRoot, "sessions", filepath.Base(sessionID), "active-mission")
}

// ActiveMissionOriginPath returns the path to the bind-origin sidecar
// that sits beside active-mission. Returns an empty string when either
// argument is empty, matching ActiveMissionPath.
func ActiveMissionOriginPath(globalRoot, sessionID string) string {
	if globalRoot == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(globalRoot, "sessions", filepath.Base(sessionID), "active-mission-origin")
}

// Bind origins. The origin answers "how did this session come to be
// bound to this mission?", and the two answers carry different
// authority:
//
//   - BindOriginClaim — `ethos mission claim`. The operator said "I am
//     working on this mission", so their commits carry its trailers.
//   - BindOriginDispatch — `ethos mission create` / `mission dispatch`.
//     The leader named a mission FOR SOMEONE ELSE. DES-076: unlike a
//     claim, this binding is single-use and scoped to the ONE Agent()
//     spawn whose agent type matches the contract's declared Worker —
//     internal/hook/pretooluse_dispatch.go's readActiveMissionForDispatch
//     gates on that match and consumeDispatchBinding clears the sidecar
//     the moment it is consumed, so it can never also attribute
//     whatever the leader spawns next. It also must not stamp the
//     leader's own commits even for that one matching spawn: the
//     leader goes on to do unrelated work in the same session, and
//     tagging it would re-open ethos-jawp's false-trailer class through
//     a new door.
//
// THE ORIGIN LIVES IN ITS OWN FILE, and active-mission stays exactly
// one line. A second line in active-mission broke every older reader:
// they TrimSpace the whole file, so an ethos from before this change
// read the mission as "m-...\nclaim" and denied every Agent spawn. The
// break is not hypothetical — agents build to .tmp/ethos and run the
// CLI from there while Claude Code hooks invoke the installed binary,
// so one `mission claim` through a new build poisoned the session for
// the installed one (rsc on PR #415).
//
// A claim is the ABSENCE of the origin file, which is also what every
// sidecar written before this change looks like. Only a dispatch
// writes one, so an upgrade needs no migration and a downgrade loses
// only the suppression, falling back to the pre-fix default.
//
// DES-076 round 3 (review finding C2, m-2026-09-08-004 round 2): as of
// the dispatch-pending redesign, nothing in production writes
// BindOriginDispatch to this pair anymore (dispatch bindings live in
// their own per-mission store — see the dispatch-pending doc comment
// below) — but the reading and writing machinery for a non-claim
// origin is retained for the same reason it always was: a mixed-binary
// window (rsc on PR #415) or a hand-inspected legacy sidecar could
// still produce one. BindOriginUnknown exists for exactly that
// residual case: an origin file that EXISTS but does not cleanly
// resolve (truncated, or naming a different mission than
// active-mission) is no longer defaulted to BindOriginClaim.
// Positive, contradictory evidence that the binding's origin is NOT a
// plain claim must never be discarded in the permissive direction —
// see ReadActiveMissionBinding's own doc comment for why an ABSENT
// origin file is a different, genuinely safe case from a PRESENT one
// that fails to parse.
const (
	BindOriginClaim    = "claim"
	BindOriginDispatch = "dispatch"
	// BindOriginUnknown is never written by this package. It is a
	// read-only sentinel ReadActiveMissionBinding returns when the
	// origin file exists but its content does not resolve into a known
	// shape — see that function's doc comment. Every caller that gates
	// a capability on Origin == BindOriginClaim (commit trailer
	// emission, the dispatch-vs-claim match in
	// internal/hook/pretooluse_dispatch.go) already refuses this value
	// by construction, with no separate check needed: it is neither
	// BindOriginClaim nor BindOriginDispatch.
	BindOriginUnknown = "unknown"
)

// ActiveMissionBinding is the session's mission binding: which mission,
// and how it was made.
type ActiveMissionBinding struct {
	MissionID string
	Origin    string // BindOriginClaim, BindOriginDispatch, or BindOriginUnknown
}

// ReadActiveMission reads the active-mission sidecar for sessionID.
// Returns ("", nil) when the file is absent — a missing sidecar is the
// common "no active mission" state, not an error. Returns the raw
// trimmed file contents on success: validation of the missionID shape
// is the caller's responsibility, not this helper's.
func ReadActiveMission(globalRoot, sessionID string) (string, error) {
	path := ActiveMissionPath(globalRoot, sessionID)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading active-mission sidecar %q: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// ReadActiveMissionBinding reports the session's mission and the origin
// of the binding. A missing active-mission sidecar is the zero value
// with a nil error.
//
// The origin file is honored only when it names the SAME mission the
// active-mission sidecar does. That makes the pair self-checking: an
// origin file left over from an earlier binding names a different
// mission and is ignored, so the two files cannot drift into a wrong
// answer — the failure mode a second file would otherwise introduce.
//
// Two DIFFERENT non-matches are deliberately given two DIFFERENT
// answers (DES-076 round 3, review finding C2, m-2026-09-08-004 round
// 2 — this distinction did not exist before that round, and its
// absence is what let a claim's own permissive defaulting apply to a
// case it was never meant to cover):
//
//   - The origin file is ABSENT. This is the legitimate, unambiguous
//     legacy shape: every sidecar written before the origin file
//     existed looks exactly like this, and `ethos mission claim` still
//     produces it today (WriteActiveMissionOrigin's claim branch writes
//     active-mission then REMOVES the origin file). Absence is a state
//     this design assigns a real meaning to — BindOriginClaim — not a
//     guess.
//   - The origin file EXISTS but does not cleanly resolve: it is
//     truncated/short, or it names a DIFFERENT mission than
//     active-mission. This is POSITIVE evidence that something is
//     wrong — a partial write, a stale leftover, or (before DES-076
//     round 3 moved dispatch off this pair entirely) a dispatch binding
//     whose second write failed. Defaulting THIS case to BindOriginClaim
//     was the round-1/round-2 behavior, and it was dangerous: DES-076
//     made claim the PERMISSIVE origin (sticky, ungated by Worker, and
//     — per internal/hook/commit_trailers.go's gate — the only origin
//     that stamps commit trailers), so silently answering "claim" for
//     an ambiguous read would both mis-capture a spawn and turn on
//     trailers for a mission the operator never explicitly claimed.
//     This case now returns BindOriginUnknown instead — a value every
//     Origin-gated caller already refuses by construction (see the
//     constant's own doc comment), so there is no separate check for a
//     caller to forget.
//
// An origin file that exists but will not read at all (a genuine I/O
// error, not merely unparseable content) is NEITHER of those: it
// surfaces as an error, exactly as before this round. An I/O failure on
// a file that is there means the binding is unknown for a reason the
// caller needs to see, not silently answer.
func ReadActiveMissionBinding(globalRoot, sessionID string) (ActiveMissionBinding, error) {
	missionID, err := ReadActiveMission(globalRoot, sessionID)
	if err != nil {
		return ActiveMissionBinding{}, err
	}
	if missionID == "" {
		return ActiveMissionBinding{}, nil
	}
	b := ActiveMissionBinding{MissionID: missionID, Origin: BindOriginClaim}

	path := ActiveMissionOriginPath(globalRoot, sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return b, nil
		}
		return ActiveMissionBinding{}, fmt.Errorf("reading active-mission-origin sidecar %q: %w", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		b.Origin = BindOriginUnknown
		return b, nil
	}
	origin := strings.TrimSpace(lines[0])
	if origin == "" {
		b.Origin = BindOriginUnknown
		return b, nil
	}
	if strings.TrimSpace(lines[1]) != missionID {
		b.Origin = BindOriginUnknown
		return b, nil
	}
	b.Origin = origin
	return b, nil
}

// WriteActiveMission binds sessionID to missionID as a claim — the
// operator saying "I am working on this". It is the shape `ethos
// mission claim` writes, and the shape every pre-origin sidecar had.
// A dispatch binding goes through WriteActiveMissionOrigin.
func WriteActiveMission(globalRoot, sessionID, missionID string) error {
	return WriteActiveMissionOrigin(globalRoot, sessionID, missionID, BindOriginClaim)
}

// WriteActiveMissionOrigin binds sessionID to missionID with the given
// origin. active-mission holds the mission ID and nothing else, so
// every reader that ever existed can read it; a dispatch additionally
// writes the origin sidecar beside it.
//
// Refuses missionID == "" — clearing is ClearActiveMission's job and
// silently writing an empty file would let a caller "claim nothing" by
// accident. An empty origin is treated as BindOriginClaim.
//
// Write order is chosen so an interrupted pair never reads wrong:
//
//   - dispatch writes the origin FIRST. It names the new mission, so
//     until active-mission catches up it does not match and is ignored.
//   - claim writes active-mission first, THEN removes the origin file.
//     A failed removal leaves a file naming the previous mission, which
//     no longer matches and is ignored.
//
// Either way the reader falls back to claim, which is the pre-fix
// default: a lost suppression costs a stray trailer, never a denied
// spawn.
func WriteActiveMissionOrigin(globalRoot, sessionID, missionID, origin string) error {
	if origin == "" {
		origin = BindOriginClaim
	}
	path := ActiveMissionPath(globalRoot, sessionID)
	if path == "" {
		return fmt.Errorf("writing active-mission: globalRoot and sessionID are required")
	}
	if missionID == "" {
		return fmt.Errorf("writing active-mission: missionID is required (use ClearActiveMission to remove)")
	}

	if origin != BindOriginClaim {
		if err := writeSidecarFile(
			ActiveMissionOriginPath(globalRoot, sessionID),
			origin+"\n"+missionID+"\n",
		); err != nil {
			return err
		}
		return writeSidecarFile(path, missionID+"\n")
	}

	if err := writeSidecarFile(path, missionID+"\n"); err != nil {
		return err
	}
	return removeSidecarFile(ActiveMissionOriginPath(globalRoot, sessionID))
}

// writeSidecarFile writes content to path atomically: the per-session
// directory is created at 0o700, the content lands in a temp file in
// the same directory at 0o600, and a rename puts it in place. A partial
// write never leaves a half-formed sidecar, and the temp file is
// removed on every error path.
func writeSidecarFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating session dir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp sidecar in %q: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing temp sidecar %q: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp sidecar %q: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing temp sidecar %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("renaming sidecar %q to %q: %w", tmpPath, path, err)
	}
	return nil
}

// removeSidecarFile deletes path, treating "already gone" as success.
// An empty path is a no-op so callers need no guard.
func removeSidecarFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing sidecar %q: %w", path, err)
	}
	return nil
}

// DelegationBinding is the per-dispatch binding info that bridges the
// PreToolUse (where the delegation_id is allocated) to the PostToolUse
// audit writer (where the delegation_id should tag each tool call).
// additional_env from PreToolUse does NOT persist into hook script
// processes, so the binding sidecar is the bridge.
type DelegationBinding struct {
	DelegationID  string
	MissionID     string
	ParentSession string
}

// DelegationBindingPath returns the path to the delegation-binding
// sidecar for sessionID.
func DelegationBindingPath(globalRoot, sessionID string) string {
	if globalRoot == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(globalRoot, "sessions", filepath.Base(sessionID), "delegation-binding")
}

// WriteDelegationBinding writes the binding info that the PostToolUse
// audit writer reads. Called from the PreToolUse Tier B dispatch after
// the delegation skeleton is written and the delegation_id is known.
func WriteDelegationBinding(globalRoot, sessionID string, b DelegationBinding) error {
	path := DelegationBindingPath(globalRoot, sessionID)
	if path == "" {
		return fmt.Errorf("writing delegation-binding: globalRoot and sessionID are required")
	}
	return writeSidecarFile(path, b.DelegationID+"\n"+b.MissionID+"\n"+b.ParentSession+"\n")
}

// ReadDelegationBinding reads the delegation-binding sidecar.
// Returns a zero-value DelegationBinding and nil when the file is
// absent — missing is the common "no active dispatch" state.
func ReadDelegationBinding(globalRoot, sessionID string) (DelegationBinding, error) {
	path := DelegationBindingPath(globalRoot, sessionID)
	if path == "" {
		return DelegationBinding{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DelegationBinding{}, nil
		}
		return DelegationBinding{}, fmt.Errorf("reading delegation-binding %q: %w", path, err)
	}
	// Trim each line, not just the file: a sidecar written with CRLF
	// endings (or hand-edited with a trailing space) would otherwise
	// carry the stray byte into the mission ID and fail every
	// comparison against it — the delegation would silently drop off
	// the commit trailer.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var b DelegationBinding
	if len(lines) > 0 {
		b.DelegationID = strings.TrimSpace(lines[0])
	}
	if len(lines) > 1 {
		b.MissionID = strings.TrimSpace(lines[1])
	}
	if len(lines) > 2 {
		b.ParentSession = strings.TrimSpace(lines[2])
	}
	return b, nil
}

// ClearActiveMission removes the active-mission sidecar for sessionID.
// Missing is not an error — clearing an already-clear slot is a no-op
// so `ethos mission release` is safe to call unconditionally.
//
// Both files go, but NOT unconditionally in parallel: the origin
// removal is attempted only after the active-mission removal succeeds.
// ReadActiveMissionBinding reads "active-mission present, origin
// absent" as BindOriginClaim — the pre-origin default, sticky and
// ungated by agent type. If a partial failure removed the origin file
// but left active-mission behind (a permissions race, a concurrent
// writer, anything that makes one os.Remove succeed and the other
// fail), that shape silently upgrades whatever mission active-mission
// still names into a sticky claim — resurrecting the exact unscoped-
// capture bug DES-076 fixed, through a different door (review finding
// F6, m-2026-09-08-003). Removing the active-mission file first and
// stopping on its failure means the pair either both go or neither
// does; a failed active-mission removal leaves the origin file in
// place, matching the mission it still names, so the reader's
// same-mission check (ReadActiveMissionBinding) keeps the two
// consistent rather than converging to the wrong answer.
func ClearActiveMission(globalRoot, sessionID string) error {
	path := ActiveMissionPath(globalRoot, sessionID)
	if path == "" {
		return nil
	}
	if err := removeSidecarFile(path); err != nil {
		return err
	}
	return removeSidecarFile(ActiveMissionOriginPath(globalRoot, sessionID))
}

// Pending dispatch bindings (DES-076 round 3, review finding C1 on
// m-2026-09-08-004 round 2).
//
// The single active-mission slot could hold only ONE dispatch binding
// at a time. `ethos mission dispatch --worker bwk` for mission A wrote
// it; a second `dispatch --worker bwk` for mission B before A's worker
// ever spawned overwrote it, silently discarding A's binding — and
// this repo pins ONE handle per specialty domain (bwk for every Go
// internals mission), so back-to-back same-worker dispatch is the
// NORMAL workflow, not a corner case. The declared-Worker discriminator
// had zero discriminating power in exactly the situation this repo
// generates most: two pending dispatches sharing a worker guaranteed a
// misattribution the moment either spawned.
//
// The fix keys the binding by MISSION instead of holding one
// overwritable slot: each pending dispatch gets its own file, named by
// mission ID, under <globalRoot>/sessions/<id>/dispatch-pending/. N
// pending dispatches to the same Worker now coexist without collision.
// A spawn matching a given Worker consumes the OLDEST pending entry for
// that Worker (file mtime order) — the natural interpretation of
// CLAUDE.md's own two-step dispatch-then-spawn protocol: a leader
// dispatches, then immediately spawns, in that order, and did so for
// A before B in this repo's own back-to-back-dispatch pattern.
//
// This also closes two related bugs the single-slot design could not
// avoid:
//   - C2: the single active-mission/active-mission-origin pair encoded
//     both claim and dispatch bindings in one two-file structure, and a
//     partial failure clearing one file but not the other converged to
//     an ambiguous state ReadActiveMissionBinding had to guess at.
//     Dispatch bindings no longer touch that pair at all — claim stays
//     exactly as it was (a single sticky slot, its own two-file
//     structure, unaffected), and each dispatch-pending entry is ONE
//     file with no paired-file consistency question to get wrong.
//   - C3: matching a claimant against the single slot required Loading
//     the mission's contract to discover its Worker, so a Load failure
//     during matching was genuinely ambiguous (was this a mismatch, or
//     an unresolvable match?) and had to fall through silently. The
//     Worker handle is now recorded directly in the pending-dispatch
//     file at dispatch time, so matching needs no Load at all — a
//     contract that fails to load for an already-matched pending
//     dispatch is handled by dispatchTierB's own existing Load-and-block
//     path, identically to an explicit MISSION_ID naming an unloadable
//     contract.
func dispatchPendingRoot(globalRoot, sessionID string) string {
	if globalRoot == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(globalRoot, "sessions", filepath.Base(sessionID), "dispatch-pending")
}

// AcquireDispatchPendingLock opens (and creates if needed) an exclusive
// per-session flock guarding sessionID's ENTIRE pending-dispatch match
// decision — from reading the candidate list through to
// `dispatchTierB`'s full admission (or fallback). Review probe finding
// F3 (m-2026-09-08-004 round 2, planted alongside C1-C13): two
// concurrent `Agent()` tool calls in the same session (a normal shape —
// this org's own conventions call for batching independent tool calls
// in one turn) could both call matchDispatchPending before either
// consumed its match, resolving to the SAME oldest pending entry twice
// — the exact double-match the review probe demonstrated directly.
//
// The lock is held by the CALLER across the whole
// read-match-then-admit-or-fall-back sequence
// (internal/hook/pretooluse_dispatch.go's dispatchAgent), not just the
// read — a lock released before `dispatchTierB` runs would still let a
// second waiter's read interleave with the first caller's still-pending
// consume decision. This is a NEW lock class with no existing caller
// that acquires a mission or delegation lock first and this one
// second, so it introduces no reversal of the acquisition order
// AcquireMissionLockExclusive's own doc comment already prescribes —
// this lock is always the OUTERMOST one, acquired before any mission or
// delegation lock, never nested inside one.
//
// Deliberately does NOT wrap the deferred `onDispatched` consumption
// itself in a SEPARATE acquisition — the caller holds this lock for the
// whole call, so the eventual `ConsumeDispatchPending` (or the decision
// not to call it, on a fallback) happens under the same critical
// section the match did.
//
// Despite the name, this same per-session lock now also guards two
// callers that are not `dispatchAgent`'s own match decision at all
// (leader review of PR #509, m-2026-09-08-004 tail round): a fresh
// `ethos mission claim` write (cmd/ethos/mission.go's runMissionClaim,
// via WithDispatchPendingLock) and a full session teardown
// (internal/session/store.go's deleteFiles, which now holds this lock
// across its whole clear-through-roster-removal span, not only the
// dispatch-pending clear substep). Reusing this ONE lock rather than
// minting a second per-session lock class was a deliberate choice: it
// keeps the "always outermost, never nested inside a mission or
// delegation lock" invariant scoped to a single acquisition point
// instead of two that would each need the same reasoning re-applied,
// and every one of these three consumer sets does a single,
// self-contained filesystem mutation with no nested mission or
// delegation lock acquisition of its own, so the invariant holds
// trivially for all three. The name stays scoped to its original,
// still-primary purpose; see WithDispatchPendingLock's own doc comment
// for the full participant list and why each one needs it.
func AcquireDispatchPendingLock(globalRoot, sessionID string) (func(), error) {
	dir := dispatchPendingRoot(globalRoot, sessionID)
	if dir == "" {
		return nil, fmt.Errorf("globalRoot and sessionID are required for dispatch-pending lock")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating dispatch-pending directory %s: %w", dir, err)
	}
	lockPath := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening dispatch-pending lock %s: %w", lockPath, err)
	}
	if err := flock(f, lockExclusive); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquiring exclusive dispatch-pending lock %s: %w", lockPath, err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_ = funlock(f)
		_ = f.Close()
	}
	return release, nil
}

// WithDispatchPendingLock acquires AcquireDispatchPendingLock for
// sessionID, runs fn, and releases the lock before returning — the
// locked-wrapper form of a single dispatch-pending mutation.
//
// WriteDispatchPending, ClearDispatchPending, and ConsumeDispatchPending
// are themselves unlocked I/O primitives. That is deliberate, not an
// oversight: internal/hook/pretooluse_dispatch.go's dispatchAgent
// acquires AcquireDispatchPendingLock ONCE and holds it across its
// entire read-match-then-admit-or-fall-back sequence, calling these
// primitives directly several times within that single critical section
// (matchDispatchPending's stale-entry clear, consumeDispatchBinding's
// post-admission consume, the explicit-MISSION_ID branch's consume). A
// self-locking primitive would make every one of those inner calls
// re-acquire a lock the outer call already holds — and unlike a Go
// mutex, this is a real flock: a second os.OpenFile+flock from the SAME
// PROCESS on the SAME file blocks on itself, because flock locks an
// open file description, not a process, so there is no re-entrant
// exemption. Self-locking primitives would deadlock dispatchAgent on
// its own first inner call.
//
// Every caller OUTSIDE that one critical section — `ethos mission
// dispatch`/`create` staging a new pending entry (cmd/ethos/mission.go
// and internal/mcp/mission_tools.go's bindDispatchedMission),
// `ethos mission release` clearing every pending entry
// (runMissionRelease), a mission's own close/abandon consuming its one
// entry (ClearMissionBindings), and session cleanup clearing every
// pending entry for a dying or purged session
// (internal/session/store.go) — runs as a SEPARATE `ethos` process (or,
// for the session-store case, a caller with no relationship to
// dispatchAgent's in-process lock at all) with no way to already hold
// dispatchAgent's lock, so it has nothing to deadlock against. Those
// callers MUST go through this wrapper rather than calling the raw
// primitives directly: without it, a `mission dispatch` write, a
// `mission release`/`close`/`abandon` clear, or a session purge's clear
// can interleave with dispatchAgent's own held-lock window — a selected
// entry can vanish out from under an in-flight admission before the
// delegation skeleton is written, or a write can land after a
// concurrent cleanup scan has already decided the directory is empty,
// leaving a released or purged session bound again.
//
// Two more callers reuse this SAME per-session lock even though neither
// touches the pending-dispatch store directly (leader review of PR
// #509, m-2026-09-08-004 tail round): `ethos mission claim`
// (cmd/ethos/mission.go's runMissionClaim, wrapping
// mission.WriteActiveMission) and `session.Store`'s own teardown
// primitive (internal/session/store.go's deleteFiles, which acquires
// this lock ONCE and holds it across clearing every sidecar AND
// removing the roster). Neither is a "dispatch-pending mutation" by
// name, but both need mutual exclusion against the exact same set of
// participants this lock already serializes: a resumed session reusing
// sessionID that writes a fresh claim (or a fresh pending dispatch)
// while `Store.Delete`/`Purge`/`PurgeTombstoned` are mid-teardown for
// that same session ID would otherwise have its new binding written,
// then silently orphaned the instant the roster disappears — deleteFiles
// is the ONE primitive every deletion path funnels through, and
// List()/Purge() discover sessions to revisit only by their roster
// file, so a sidecar with no roster has no GC path at all. Reusing this
// lock rather than minting a fourth lock class keeps the whole
// mission-sidecar surface (claim, dispatch-pending, delegation-binding,
// and the roster-driven teardown that clears all three) serialized
// through one acquisition point.
//
// Lock-order note: this is the same lock AcquireDispatchPendingLock's
// own doc comment requires to stay OUTERMOST relative to any mission or
// delegation lock. fn here does a single, self-contained filesystem
// mutation (write/consume/clear) with no nested mission or delegation
// lock acquisition, so that invariant is trivially preserved by every
// caller of this wrapper. internal/session/store.go's callers run this
// wrapper from inside their OWN, unrelated roster flock
// (Store.withLock) — a third lock class this function neither acquires
// nor is acquired from within, so nesting it there introduces no
// reversal of any existing pairing: no code path acquires the
// dispatch-pending lock and then tries to acquire a session roster
// lock, only the other direction, and only from that one caller.
func WithDispatchPendingLock(globalRoot, sessionID string, fn func() error) error {
	release, err := AcquireDispatchPendingLock(globalRoot, sessionID)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// DispatchPendingPath returns the path to the pending-dispatch file for
// one mission. Returns "" when any argument is empty.
func DispatchPendingPath(globalRoot, sessionID, missionID string) string {
	dir := dispatchPendingRoot(globalRoot, sessionID)
	if dir == "" || missionID == "" {
		return ""
	}
	return filepath.Join(dir, filepath.Base(missionID))
}

// WriteDispatchPending records that missionID has been dispatched and
// is awaiting its ONE matching Worker spawn. worker is the contract's
// declared Worker handle — recorded here so a later scan never needs
// to re-Load the contract to discover it (see the C3 note above).
func WriteDispatchPending(globalRoot, sessionID, missionID, worker string) error {
	path := DispatchPendingPath(globalRoot, sessionID, missionID)
	if path == "" {
		return fmt.Errorf("writing dispatch-pending: globalRoot, sessionID, and missionID are required")
	}
	if strings.TrimSpace(worker) == "" {
		return fmt.Errorf("writing dispatch-pending: worker is required")
	}
	return writeSidecarFile(path, worker+"\n")
}

// DispatchPendingEntry is one pending dispatch binding, as reported by
// ReadDispatchPending.
type DispatchPendingEntry struct {
	MissionID string
	Worker    string
	CreatedAt time.Time // the pending file's mtime; used for FIFO ordering.
}

// ReadDispatchPending lists every pending dispatch binding for
// sessionID, oldest first. A corrupt or unreadable individual entry is
// reported as a warning string rather than failing the whole scan — one
// bad file must not blind the reader to every other still-good pending
// dispatch, the same non-blocking discipline every sidecar reader in
// this file follows.
func ReadDispatchPending(globalRoot, sessionID string) ([]DispatchPendingEntry, []string, error) {
	dir := dispatchPendingRoot(globalRoot, sessionID)
	if dir == "" {
		return nil, nil, nil
	}
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading dispatch-pending dir %q: %w", dir, err)
	}
	var out []DispatchPendingEntry
	var warnings []string
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		// The per-session dispatch-pending lock file (".lock",
		// AcquireDispatchPendingLock) lives in this same directory and
		// is not a pending entry -- a mission ID is never dotfile-named,
		// so this exclusion cannot collide with a real entry.
		if strings.HasPrefix(de.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, de.Name())
		info, statErr := de.Info()
		if statErr != nil {
			warnings = append(warnings, fmt.Sprintf("stat %q: %v", path, statErr))
			continue
		}
		// L5 (full-branch review, m-2026-09-08-004 round 3): every other
		// reader in this package refuses a symlinked entry
		// (LoadDelegation's own rejectSymlink call) rather than silently
		// following it; this one was reading straight through
		// os.ReadFile, which does follow symlinks, unlike every sibling
		// reader's discipline.
		if symErr := rejectSymlink(path); symErr != nil {
			warnings = append(warnings, symErr.Error())
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			warnings = append(warnings, fmt.Sprintf("reading %q: %v", path, readErr))
			continue
		}
		worker := strings.TrimSpace(string(data))
		if worker == "" {
			warnings = append(warnings, fmt.Sprintf("%q: empty worker", path))
			continue
		}
		out = append(out, DispatchPendingEntry{
			MissionID: de.Name(),
			Worker:    worker,
			CreatedAt: info.ModTime(),
		})
	}
	// L5 (full-branch review, m-2026-09-08-004 round 3): mtime alone is
	// not a stable FIFO discriminator — two entries written within the
	// same filesystem mtime tick (coarse on some filesystems/platforms;
	// concurrent writers holding the same AcquireDispatchPendingLock in
	// quick succession) sort with sort.Slice's documented non-stable,
	// unspecified relative order, so a rerun of the identical input can
	// silently pick a different "oldest" entry. Mission IDs are
	// date-sequential with a fixed-width numeric suffix (mission.NewID),
	// so they are lexically sortable in creation order; break a
	// CreatedAt tie on MissionID for a fully deterministic total order,
	// and use SliceStable so any residual non-comparator-visible ordering
	// (there is none left, but the cost of the extra guard is nil) can
	// never introduce nondeterminism either.
	sort.SliceStable(out, func(i, j int) bool { return dispatchPendingLess(out[i], out[j]) })
	return out, warnings, nil
}

// dispatchPendingLess is ReadDispatchPending's FIFO ordering: oldest
// CreatedAt first, tiebroken on MissionID when two entries share a
// mtime — see ReadDispatchPending's sort call for why the tiebreak is
// necessary (review finding L5, m-2026-09-08-004 round 3). Extracted so
// the ordering logic itself is unit-testable without depending on
// os.ReadDir's incidental sorted-by-filename return order, which today
// happens to already match mission-ID order and would otherwise mask a
// broken tiebreak in any filesystem-backed test.
func dispatchPendingLess(a, b DispatchPendingEntry) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.MissionID < b.MissionID
}

// PendingEntryStatus classifies a pending-dispatch entry's mission for
// ClassifyPendingDispatches.
type PendingEntryStatus int

const (
	// PendingEntryOpen: the mission's contract loaded and its status is
	// "open" — a live, matchable candidate.
	PendingEntryOpen PendingEntryStatus = iota
	// PendingEntryStale: the contract loaded but its status is
	// something other than "open" — provably dead.
	PendingEntryStale
	// PendingEntryUnresolvable: loading the contract itself failed.
	// Proves nothing — the failure may be transient (a branch switch
	// that temporarily removed a git-tracked contract file, a lock
	// contention blip) — so an unresolvable entry is neither a live
	// candidate nor provably dead.
	PendingEntryUnresolvable
)

// ClassifyPendingEntry reports whether missionID's contract is open,
// provably non-open ("stale"), or unresolvable (Load failed), plus a
// human-readable reason for the non-open cases.
func ClassifyPendingEntry(store *Store, missionID string) (PendingEntryStatus, string) {
	if store == nil {
		return PendingEntryUnresolvable, "no mission store"
	}
	c, err := store.Load(missionID)
	if err != nil {
		return PendingEntryUnresolvable, err.Error()
	}
	if c.Status != StatusOpen {
		return PendingEntryStale, fmt.Sprintf("that mission is %s", c.Status)
	}
	return PendingEntryOpen, ""
}

// ClassifiedPendingEntry pairs a DispatchPendingEntry with its
// ClassifyPendingEntry result.
type ClassifiedPendingEntry struct {
	DispatchPendingEntry
	Status PendingEntryStatus
	Reason string
}

// ClassifyPendingDispatches is the single source of truth for "which of
// sessionID's pending dispatches for worker would actually be matched
// by a spawn right now" — oldest first, every entry classified.
//
// Review finding J1 (full-branch review, m-2026-09-08-004 round 3): the
// hook's own matcher (internal/hook's matchDispatchPending) and the CLI
// and MCP surfaces' dispatch-time queue-position advisory used to
// compute this independently — the matcher's classify-and-filter logic
// lived only in the hook package, while the advisory messages did a
// bare Worker-equality filter with no classification at all. The two
// could disagree: an unresolvable entry ahead of a new dispatch was
// reported as "ahead of it and will be matched first" when the matcher
// itself would actually SKIP that unresolvable entry and match the new
// one instead — the message told the operator the opposite of what
// would happen. Both call sites now share this one function so the
// reported queue position and the entry the hook would actually match
// cannot diverge; a caller that needs to know why an entry was
// excluded (to clear a stale one, or warn about an unresolvable one)
// reads Status/Reason directly rather than re-deriving them.
func ClassifyPendingDispatches(store *Store, globalRoot, sessionID, worker string) ([]ClassifiedPendingEntry, []string, error) {
	entries, warnings, err := ReadDispatchPending(globalRoot, sessionID)
	if err != nil {
		return nil, nil, err
	}
	var out []ClassifiedPendingEntry
	for _, e := range entries {
		if e.Worker != worker {
			continue
		}
		status, reason := ClassifyPendingEntry(store, e.MissionID)
		out = append(out, ClassifiedPendingEntry{DispatchPendingEntry: e, Status: status, Reason: reason})
	}
	return out, warnings, nil
}

// ConsumeDispatchPending removes ONE pending dispatch entry — called
// once its matching spawn has been fully admitted (DES-076: after the
// JSON response has been encoded, never on a refusal). Missing is not
// an error — already consumed, or never existed.
//
// Unlike ClearActiveMission's two-file pair, this is a single-file
// removal: there is no paired-file consistency question a partial
// failure could leave in an ambiguous state (C2's exact class). A
// failure here is reported to the caller, which is responsible for
// telling the operator the remedy (`ethos mission release`) — see
// consumeDispatchBinding in internal/hook/pretooluse_dispatch.go.
func ConsumeDispatchPending(globalRoot, sessionID, missionID string) error {
	return removeSidecarFile(DispatchPendingPath(globalRoot, sessionID, missionID))
}

// ClearDispatchPending removes every pending dispatch entry for
// sessionID — `ethos mission release`'s dispatch-side counterpart to
// ClearActiveMission. Missing is not an error.
func ClearDispatchPending(globalRoot, sessionID string) error {
	dir := dispatchPendingRoot(globalRoot, sessionID)
	if dir == "" {
		return nil
	}
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("reading dispatch-pending dir %q: %w", dir, err)
	}
	var errs []error
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		// Leave the lock file in place -- it is not a pending entry (see
		// the matching exclusion in ReadDispatchPending), and removing a
		// lock file a concurrent holder still has open is unnecessary
		// churn, not a correctness requirement.
		if strings.HasPrefix(de.Name(), ".") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, de.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ClearMissionBindings removes sessionID's active-mission and
// delegation-binding sidecars when they name missionID. Every surface
// that takes a mission out of the open set calls it: the commit-msg
// trailer fallback gates on the active-mission sidecar, so a close that
// left it behind would keep tagging later missionless commits with the
// closed mission (ethos-jawp).
//
// Each sidecar is cleared only when it names missionID, so a claim or
// dispatch for a different, still-open mission survives. A missing
// sidecar is a silent no-op — that is the ordinary state.
//
// The two sidecars are independent, so a failure on one does not stop
// work on the other; all failures come back joined. Callers treat the
// result as advisory — a mission that closed stays closed — but must
// report it, because each failure leaves a sidecar in place and the
// trailer gate open on a closed mission.
func ClearMissionBindings(globalRoot, sessionID, missionID string) error {
	if globalRoot == "" || sessionID == "" || missionID == "" {
		return nil
	}
	var errs []error

	// ReadActiveMission and ReadDelegationBinding both report a missing
	// sidecar as a zero value with a nil error, so a non-nil error here
	// is always a real read failure — never "no sidecar".
	active, err := ReadActiveMission(globalRoot, sessionID)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("reading active mission: %w", err))
	case active == missionID:
		if err := ClearActiveMission(globalRoot, sessionID); err != nil {
			errs = append(errs, fmt.Errorf("clearing active mission: %w", err))
		}
	}

	b, err := ReadDelegationBinding(globalRoot, sessionID)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("reading delegation binding: %w", err))
	case b.MissionID == missionID:
		if err := ClearDelegationBinding(globalRoot, sessionID); err != nil {
			errs = append(errs, fmt.Errorf("clearing delegation binding: %w", err))
		}
	}

	// Dispatch-pending entries are keyed by mission ID (DES-076 round
	// 3), so a mission taking itself out of the open set can remove
	// its OWN pending entry precisely — unlike the old single ambiguous
	// slot, there is no risk of clearing a DIFFERENT mission's binding
	// by mistake. This is the one remedy `ethos mission close`/`abandon`
	// actually restores for a stuck pending dispatch on THIS mission;
	// a stuck entry for a DIFFERENT mission is unaffected and still
	// needs `ethos mission release` (review finding C9's follow-up,
	// m-2026-09-08-004 round 2).
	//
	// ClearMissionBindings runs from `ethos mission close`/`abandon`, a
	// separate process from dispatchAgent's own held dispatch-pending
	// lock, so it must take that lock itself rather than call
	// ConsumeDispatchPending unlocked (WithDispatchPendingLock's own doc
	// comment).
	if err := WithDispatchPendingLock(globalRoot, sessionID, func() error {
		return ConsumeDispatchPending(globalRoot, sessionID, missionID)
	}); err != nil {
		errs = append(errs, fmt.Errorf("clearing dispatch-pending: %w", err))
	}
	return errors.Join(errs...)
}

// ClearDelegationBinding removes the delegation-binding sidecar for
// sessionID. Missing is not an error — clearing an already-clear slot
// is a no-op.
//
// The binding is written per-dispatch but was never cleared, so it
// accumulated across sessions and let the commit-msg hook's fallback
// tag unrelated commits with a stale delegation (ethos-jawp). Clearing
// it on `mission release` and on terminal transitions bounds the
// sidecar's lifetime to the dispatch it describes.
func ClearDelegationBinding(globalRoot, sessionID string) error {
	path := DelegationBindingPath(globalRoot, sessionID)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("removing delegation-binding sidecar %q: %w", path, err)
	}
	return nil
}

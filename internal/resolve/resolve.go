// Package resolve implements identity resolution chains for humans
// and agents. Humans are resolved from iam declarations, git config,
// or OS user. Agents are resolved from per-repo config.
package resolve

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/process"
	"github.com/punt-labs/ethos/v4/internal/session"

	"gopkg.in/yaml.v3"
)

// Resolution modes for the resolution field (DES-057 Part A).
const (
	// ResolutionLayered is the default: repo → bundle → global, where the
	// global fallback catches anything the repo layer lacks.
	ResolutionLayered = "layered"
	// ResolutionRepoOnly drops the global tail, making the repo layer
	// authoritative so a missing handle or attribute fails loud instead of
	// silently resolving from the user's home.
	ResolutionRepoOnly = "repo-only"
)

// RepoConfig holds the repo-local ethos configuration.
//
// MaxDelegationDepth bounds the parent_delegation chain length the
// PreToolUse-on-Agent hook will admit (DES-054 v5). A spawn whose
// depth would exceed this limit is refused with verdict=aborted on
// the just-written skeleton. Zero means "use the package default"
// (mission.MaxDelegationDepthDefault) so a repo with no override
// gets a safe value rather than an unbounded chain.
//
// Resolution selects the layer chain (DES-057). Unset means layered —
// byte-identical to the behavior before the field existed.
type RepoConfig struct {
	Agent              string `yaml:"agent,omitempty"`                // default agent identity handle
	Team               string `yaml:"team,omitempty"`                 // team that owns this repo
	ActiveBundle       string `yaml:"active_bundle,omitempty"`        // currently active team bundle name
	Resolution         string `yaml:"resolution,omitempty"`           // DES-057 layer policy; "" == layered
	MaxDelegationDepth int    `yaml:"max_delegation_depth,omitempty"` // DES-054 v5 depth ceiling; 0 == default
}

// ErrNoIdentityMatch is wrapped by the error Resolve returns when every step
// ran and none matched: an absence of identity, not a failure to look. A
// caller that falls back on "no identity" checks for it; any other Resolve
// error is a failure (git did not answer, a store would not read) and must
// not be treated as no identity.
var ErrNoIdentityMatch = errors.New("no identity matches")

// Resolve returns the identity handle for the current caller.
//
// Resolution chain (stops at first match):
//  1. iam declaration — walk process tree for PID-keyed session file
//  2. git config user.name — match identity github field
//  3. git config user.email — match identity email field
//  4. $USER — match identity handle field
//
// Returns an error when no step matches.
func Resolve(store identity.IdentityStore, ss *session.Store, checkout string) (string, error) {
	// Step 1: check for iam declaration via process tree.
	if ss != nil {
		sp, err := resolveFromSession(ss)
		if err != nil {
			// A session WAS expected here — running under Claude Code, or
			// an explicit ETHOS_SESSION/pointer file resolved an ID — but
			// something about it did not check out (unidentifiable,
			// unreadable roster, no matching participant). DES-074: this
			// is the wrong-answer risk, not an absence, so it must not
			// fall through to git/OS.
			return "", err
		}
		if sp.found {
			if sp.handle != "" {
				return sp.handle, nil
			}
			// Participant exists but has no persona — do not fall
			// through to git/OS. This is an explicit "no identity."
			return "", fmt.Errorf("session participant found but no persona configured")
		}
	}

	// Step 2: git config user.name → github field.
	gitName, err := GitConfig(checkout, "user.name")
	if err != nil {
		return "", fmt.Errorf("reading the git user: %w", err)
	}
	if gitName != "" {
		id, err := store.FindBy("github", gitName)
		if err != nil {
			return "", fmt.Errorf("searching identities by github: %w", err)
		}
		if id != nil {
			return id.Handle, nil
		}
	}

	// Step 3: git config user.email → email field.
	gitEmail, err := GitConfig(checkout, "user.email")
	if err != nil {
		return "", fmt.Errorf("reading the git user: %w", err)
	}
	if gitEmail != "" {
		id, err := store.FindBy("email", gitEmail)
		if err != nil {
			return "", fmt.Errorf("searching identities by email: %w", err)
		}
		if id != nil {
			return id.Handle, nil
		}
	}

	// Step 4: $USER → handle field.
	osUser := os.Getenv("USER")
	if osUser != "" {
		id, err := store.FindBy("handle", osUser)
		if err != nil {
			return "", fmt.Errorf("searching identities by handle: %w", err)
		}
		if id != nil {
			return id.Handle, nil
		}
	}

	return "", fmt.Errorf("%w git user %q, email %q, or OS user %q%s",
		ErrNoIdentityMatch, gitName, gitEmail, osUser, repoOnlyHint(store))
}

// repoOnlyHint explains a no-match under DES-057 repo-only mode, or
// returns "" in layered mode. Without it the terminal error is a generic
// "no identity matches" that cannot say WHY the global store — which
// holds the caller's identity — went unconsulted.
func repoOnlyHint(store identity.IdentityStore) string {
	ls, ok := store.(*identity.LayeredStore)
	if !ok || !ls.RepoAuthoritative() {
		return ""
	}
	return " — this repo sets resolution: repo-only, so the global identity store was not consulted; run `ethos vendor <handle>` to add your identity to this repo"
}

// sessionPersona is the result of resolveFromSession.
type sessionPersona struct {
	handle string // persona handle, may be empty (explicitly no persona)
	found  bool   // true if a session participant was found
}

// SessionSourceEnv is the source SessionID reports when the ID came from
// ETHOS_SESSION (an explicit, caller-supplied anchor that consumers verify),
// as opposed to the corroborated CLAUDE_PID pointer file (SessionSourcePID).
const SessionSourceEnv = "env"

// SessionSourcePID is the source SessionID reports when the ID came from
// the pointer file keyed on a corroborated CLAUDE_PID. Named for the
// mechanism that actually resolves it — no fallback to the topmost-ancestor
// walk remains in this path (review finding, PR #502) — rather than the
// pre-fix "walk", which described a mechanism this function no longer uses.
const SessionSourcePID = "pid"

// ErrNoSession is returned by SessionID and resolveFromSession when a
// session WAS expected — running under Claude Code (see
// process.UnderClaudeCode), or an explicit ETHOS_SESSION was set — but
// could not be identified: the env value is present but uncorroborated,
// the pointer file is missing, or the named roster is unreadable or has no
// matching participant. It names the remedy so a caller that requires a
// session (iam, mission claim/release) can fail loud with a non-zero exit
// instead of silently defaulting to some other identity (operator ruling
// 2026-09-07, DES-074): a mechanism is reliable or it raises a clear error
// with a hint.
//
// It is deliberately NOT returned for the other, unremarkable failure
// state: not running under Claude Code at all (headless, CI, SDK, a plain
// terminal) — SessionID returns ErrNotUnderClaudeCode for that instead, a
// second, distinct sentinel (round 2: two independent reviewers found
// callers pattern-matching on `err == nil` alone to mean "resolved,"
// which silently accepted an empty id whenever the first sentinel-less
// design returned (id="", err=nil) — the third outcome was reconstructible
// only from a nil error PLUS an empty string, and at least three callers
// missed the second half of that check). Conflating "no session at all"
// with "a session was expected but broken" was itself a defect DES-074
// names explicitly: "no session" and "this git user" are different
// answers and must not be returned interchangeably, but neither may a CI
// run's total absence of a Claude Code session be treated as an alarming,
// loud failure.
// The message carries no "ethos: " prefix — cmd/ethos's top-level error
// printer adds that once; prefixing it here doubled it to "ethos: ethos:
// ..." (Bugbot/team-lead HIGH, round 2).
//
// The base message names only the failure, not a remedy (mission 005
// finding G): "set ETHOS_SESSION=<id>, or run `ethos session start`" was
// verified inert against this specific error's two sub-cases.
// `ethos session start` (cmd/ethos/session.go's runSessionStart) creates a
// roster and prints an `export ETHOS_SESSION=...` line to stdout — it
// never calls session.Store.WriteCurrentSession, so it cannot repair a
// broken pointer file, and its own output reaches nothing unless the
// caller evaluates it (`eval "$(ethos session start)"`), which fixes only
// that one shell's environment, never a process Claude Code spawned
// (which inherits Claude Code's environment, not the terminal's). Naming
// a remedy that does not remedy costs the reader a retry cycle before
// they learn to distrust the message, so each call site below composes
// its own sub-case-specific advice instead — see restartPointerRemedy and
// deadRosterRemedy.
var ErrNoSession = errors.New("cannot identify the calling session")

// restartPointerRemedy is ErrNoSession's remedy for its dominant sub-case:
// the session-current pointer file is missing, blank, or otherwise
// unreadable (SessionID's own failure path). Restarting the Claude Code
// session (or running /clear) makes SessionStart fire again, which is the
// only thing that calls session.Store.WriteCurrentSession — `ethos
// session start` alone does not.
const restartPointerRemedy = "restart the Claude Code session (or run /clear) so SessionStart re-establishes the session pointer; " +
	"the direct, non-interactive equivalent is `ethos session write-current --pid <pid> --session <id>`"

// uncorroboratedPIDRemedy is ErrNoSession's remedy for the sub-case where
// CLAUDE_PID itself is absent or fails corroboration: there is no key to
// look a session pointer up by at all, so restartPointerRemedy's advice
// (re-run SessionStart) does not apply until the underlying cause — an
// older Claude Code harness (pre-2.1.234) or a genuinely broken ancestry —
// is fixed. Falling back to the topmost-ancestor walk here would key the
// lookup on the shared "claude daemon run" PID every concurrent session on
// the host collides on — precisely the collision DES-074 exists to close
// (review finding, PR #502) — so this case is reported as unresolvable
// rather than resolved via that shared key.
const uncorroboratedPIDRemedy = "CLAUDE_PID is absent or could not be corroborated as a live ancestor of this process; " +
	"upgrade to Claude Code 2.1.234+ if this is an older harness, or set ETHOS_SESSION=<id> directly"

// deadRosterRemedy is ErrNoSession's remedy for its other sub-case: an
// explicit ETHOS_SESSION (or a resolved pointer) names a session ID whose
// roster no longer exists on disk. Unlike restartPointerRemedy, there is
// no Claude Code hook to re-fire here — the caller supplied (or the
// pointer named) an ID that is simply gone. `eval "$(ethos session
// start)"` mints a fresh one and sets it in the CALLING shell, but the
// eval is mandatory: the bare command only prints the export, and even
// with eval this fixes only that shell's own environment, never a process
// Claude Code spawned.
const deadRosterRemedy = "run `eval \"$(ethos session start)\"` in your shell to mint a fresh session " +
	"(the eval is required; this does not help a process Claude Code spawned, which inherits Claude Code's own environment, not the shell's)"

// ErrNotUnderClaudeCode is returned by SessionID when no session was ever
// expected: not running under Claude Code at all (headless, CI, SDK, a
// plain terminal). This is the normal, unremarkable counterpart to
// ErrNoSession — a caller for which a declared session is merely one of
// several optional identity sources (resolve.Resolve's step 1 of 4)
// treats this as "try the next source," never as a failure to report.
//
// Making this a distinct, named error rather than a silent (id="",
// err=nil) pair closes a class of bug the round 2 review found three
// instances of: a caller checking only `err == nil` to mean "resolved"
// silently accepted an empty id for this case, because nil was ALSO what
// success looked like. With this sentinel, `err == nil` means resolved,
// full stop; every other outcome — this one included — is a distinct,
// named, non-nil error a caller must consciously unwrap with errors.Is.
var ErrNotUnderClaudeCode = errors.New("not running under Claude Code")

// UnderClaudeCode indirects process.UnderClaudeCode — the DES-074 "was a
// session expected" signal — behind a package variable rather than a
// direct call, so tests across every consumer package can override it
// deterministically. Unlike CLAUDE_PID/CLAUDECODE, a real claude ancestor
// process cannot be un-set with t.Setenv: this whole suite (and its
// consumers' test suites) normally runs INSIDE a live Claude Code session,
// where process.UnderClaudeCode's ancestor-walk check is unavoidably true
// regardless of which env vars a test strips. A test that wants the
// "genuinely no session expected" branch overrides this var instead.
var UnderClaudeCode = process.UnderClaudeCode

// pointerRetryAttempts and pointerRetryDelay bound the retry SessionID
// applies to a missing pointer file when running under Claude Code: a
// consumer can start before SessionStart finishes writing it (DES-074
// point 5, ported from biff session_id.py's _RESOLVE_ATTEMPTS /
// _RESOLVE_DELAY_S — SessionStart fires before an MCP client connects;
// the retry is a safety net for that race, not the common case). The
// retry never fires when not under Claude Code at all — there
// SessionStart never ran and never will, so retrying would only add
// latency to the common no-session case (CI, scripts) for no benefit.
// pointerRetryAttempts is a var, not a const, so a test can drive it to 1
// (or 0) and prove retryReadCurrentSession's err/id invariant holds
// structurally rather than merely at the shipped value of 10 (mission
// 005 finding D).
var (
	pointerRetryAttempts = 10
	pointerRetryDelay    = 50 * time.Millisecond
)

// SessionID resolves the active session ID using the harness-neutral chain:
// ETHOS_SESSION, then the pointer file keyed on a corroborated CLAUDE_PID.
//
// Three outcomes (DES-074), and err == nil if and only if id != "" — a
// caller may trust `err == nil` alone to mean "resolved, id is valid,"
// full stop (round 2: two of the three outcomes used to share err == nil,
// and at least three callers pattern-matched on the error alone, silently
// accepting an empty id for the "no session" case):
//   - id != "", err == nil: resolved. source names SessionSourceEnv or
//     SessionSourcePID.
//   - id == "", err == ErrNotUnderClaudeCode: not running under Claude Code
//     at all (headless, CI, SDK, a plain terminal) — a normal state. No
//     session was ever expected; callers may silently try another
//     identity source.
//   - id == "", errors.Is(err, ErrNoSession): a session WAS expected
//     (running under Claude Code, per process.UnderClaudeCode) but could
//     not be identified. Callers must fail loud with a non-zero exit and
//     must NOT substitute another identity source.
//
// Deliberately does NOT fall back to the topmost-ancestor walk
// (process.FindClaudePID's own fallback) when CLAUDE_PID is absent or fails
// corroboration: that walk returns the shared "claude daemon run" PID every
// concurrent Claude Code session on a host collides on, which would key
// this lookup on an unrelated session's pointer and resolve a plausible but
// WRONG session — precisely the collision this decision exists to close,
// reachable through an older harness, a transient process-table failure, or
// ancestry deeper than the walk's own depth cap (review finding, PR #502).
// process.LegacyClaudePID's walk remains legitimate for participant-roster
// tolerance (resolveFromSession below) and for process.UnderClaudeCode's own
// presence check, neither of which shares this function's wrong-answer risk.
//
// Callers that accept an explicit session (a --session flag or an MCP
// session_id arg) check that first and bypass this (DES-061).
func SessionID(ss *session.Store) (id, source string, err error) {
	// TrimSpace, not a bare non-empty check (mission 005 finding E): an
	// env value of all whitespace is not a session ID any more than an
	// empty string is, but `sid != ""` alone would have accepted it and
	// handed a caller a garbage ID to look up.
	if sid := strings.TrimSpace(os.Getenv("ETHOS_SESSION")); sid != "" {
		return sid, SessionSourceEnv, nil
	}

	if !UnderClaudeCode() {
		return "", "", ErrNotUnderClaudeCode
	}

	pid, ok := process.ClaudePIDFromEnvCorroborated()
	if !ok {
		// A session WAS expected (UnderClaudeCode is true) but there is no
		// key this call can trust to look a pointer file up by. See the
		// function doc: falling back to the walk here would re-key on the
		// shared daemon PID, not report a wrong-answer risk as unresolvable.
		return "", "", fmt.Errorf("%w: %s", ErrNoSession, uncorroboratedPIDRemedy)
	}

	sid, rerr := ss.ReadCurrentSession(pid)
	if rerr != nil && errors.Is(rerr, os.ErrNotExist) {
		// Retry ONLY the startup race the retry exists for: a consumer
		// running before SessionStart finishes writing the pointer file
		// (DES-074 point 5). Every other failure -- permission denied, "is
		// a directory," a blank-file read -- is deterministic, not a race:
		// WriteCurrentSession's atomic temp+rename discipline (mission 005
		// finding E) means a blank pointer can no longer be observed
		// mid-write, so retrying it (or any other non-ErrNotExist cause)
		// just spends ~450ms re-deriving the identical failure before
		// reporting it (Copilot finding, PR #502).
		sid, rerr = retryReadCurrentSession(ss, pid, rerr)
	}
	if rerr != nil {
		// Wrap rerr and append restartPointerRemedy rather than returning
		// the bare ErrNoSession sentinel: the real cause (a permission
		// error or a corrupt file is DIFFERENT from an ordinary missing
		// pointer, and the retry above already confirmed there is no
		// pointer to find) must stay visible (round 2, R6), and the
		// remedy must actually be the one that fixes THIS sub-case — a
		// broken or absent pointer file, repaired only by a fresh
		// SessionStart (mission 005 finding G). errors.Is(err, ErrNoSession)
		// still holds for every caller that checks it, since %w preserves
		// the chain.
		return "", "", fmt.Errorf("%w: %s (%v)", ErrNoSession, restartPointerRemedy, rerr)
	}
	return sid, SessionSourcePID, nil
}

// retryReadCurrentSession re-reads the PID-keyed pointer file a few times
// with a short delay, covering the startup race where a consumer runs
// before SessionStart finishes writing it. The caller gates entry on
// errors.Is(firstErr, os.ErrNotExist) — this function does not re-check
// that itself, since it exists only to cover that one race, not to decide
// whether retrying is warranted (Copilot finding, PR #502: retrying a
// deterministic error like EACCES or a blank-file read just re-derives the
// identical cause ~450ms later). firstErr is the error from the read that
// triggered the retry; it seeds lastErr so that a zero-iteration loop
// (pointerRetryAttempts <= 1) still returns a non-nil error instead of
// silently reporting ("", nil) — SessionID's err/id invariant must hold
// regardless of how many retries the constant configures, not merely at
// its current value of 10 (mission 005 finding D).
func retryReadCurrentSession(ss *session.Store, pid string, firstErr error) (string, error) {
	lastErr := firstErr
	for i := 0; i < pointerRetryAttempts-1; i++ {
		time.Sleep(pointerRetryDelay)
		sid, err := ss.ReadCurrentSession(pid)
		if err == nil {
			return sid, nil
		}
		lastErr = err
	}
	return "", lastErr
}

// resolveFromSession resolves the session via the harness-neutral chain
// (ETHOS_SESSION, then the corroborated-CLAUDE_PID pointer), then returns
// the caller's persona from the roster. The caller's participant is keyed on
// ETHOS_AGENT_ID when set — matching how iam records it on both the CLI
// and MCP surfaces — else on the Claude PID.
//
// Returns (sessionPersona{}, nil) — silently try the next identity
// source — in two cases: no session was ever expected (not running
// under Claude Code at all), or a session resolved and its roster
// loaded, but this caller is not a declared participant in it. The
// latter is the ordinary state for any process that has not run `iam`
// yet, not a wrong-answer risk (round 2, R3 — binding ruling: "a
// participant miss is NOT fatal; only an unresolvable session is").
// Returns a non-nil error — always ErrNoSession or wrapping it — only
// when the SESSION itself does not check out: unidentifiable, or a
// roster that fails to load (deleted, unreadable). These are two of the
// three ways DES-074 measured this mechanism producing "a plausible
// wrong answer with exit status 0" — "an ended session" and, before
// CLAUDE_PID keying, "the wrong repo" (a misspelled persona is the
// third, already surfaced downstream when the caller loads the returned
// handle, not a resolveFromSession concern). Returns found=true with
// empty handle if the participant exists but has no persona configured
// — the caller must not fall through to git/OS for that case either,
// since a declared-but-personaless participant is an explicit "no
// identity," not an absence.
func resolveFromSession(ss *session.Store) (sessionPersona, error) {
	sessionID, source, err := SessionID(ss)
	if err != nil {
		if errors.Is(err, ErrNotUnderClaudeCode) {
			// A legitimate absence, not a wrong-answer risk: translate to
			// this function's own "try the next identity source" contract
			// rather than propagating SessionID's sentinel outward.
			return sessionPersona{}, nil
		}
		return sessionPersona{}, err
	}
	roster, lErr := ss.Load(sessionID)
	if lErr != nil {
		// The session ID itself resolved, but no roster exists for it — a
		// dead reference, not a broken pointer, so the remedy differs
		// (mission 005 finding G): an explicit ETHOS_SESSION named an ID
		// that is simply gone (deadRosterRemedy); a resolved pointer
		// naming a deleted roster is repaired the same way a broken
		// pointer is, by a fresh SessionStart (restartPointerRemedy).
		remedy := restartPointerRemedy
		if source == SessionSourceEnv {
			remedy = deadRosterRemedy
		}
		if errors.Is(lErr, os.ErrNotExist) {
			return sessionPersona{}, fmt.Errorf("session %q not found: %w: %s", sessionID, ErrNoSession, remedy)
		}
		return sessionPersona{}, fmt.Errorf("session %q has an unreadable roster (%v): %w: %s", sessionID, lErr, ErrNoSession, remedy)
	}
	agentID := os.Getenv("ETHOS_AGENT_ID")
	selfKeyed := agentID == ""
	if selfKeyed {
		agentID = process.FindClaudePID()
	}
	p := roster.FindParticipant(agentID)
	if p == nil && selfKeyed {
		// A session's primary participant written before DES-074 is keyed
		// on the walk-derived PID, not the corroborated CLAUDE_PID this
		// caller just resolved — an in-flight session at upgrade time
		// would otherwise show "no participant matching" until it ends
		// (round 2 finding). Tolerate the legacy key as a fallback; an
		// explicit ETHOS_AGENT_ID is never subject to this — that value is
		// exact by the caller's own declaration, not a guess to widen.
		if legacy := process.LegacyClaudePID(); legacy != agentID {
			p = roster.FindParticipant(legacy)
		}
	}
	if p == nil {
		// RULING (round 2, R3, binding): a participant miss is NOT fatal;
		// only an unresolvable SESSION is. The session itself resolved
		// fine (a real roster loaded) — this caller simply is not a
		// declared participant in it, which is the ordinary state for
		// any process that has not run `iam` yet, not a wrong-answer
		// risk. Silently try the next identity source, exactly like "not
		// running under Claude Code at all."
		return sessionPersona{}, nil
	}
	// Participant found. If persona is empty, that's an explicit
	// "no persona configured" — not "try git/OS instead."
	if p.Persona == "" {
		return sessionPersona{found: true}, nil
	}
	return sessionPersona{handle: p.Persona, found: true}, nil
}

// ethosRootOf is <storeRoot>/.punt-labs/ethos when it is a directory, "" with a
// nil error only when it does not exist, and an error for every other failure.
// The whole path is checked, one component at a time with Lstat: a dangling
// symlink at any level (.punt-labs included), a non-directory, a symlink loop
// or a permission error is a store ethos cannot read, never "no store yet". A
// repository with no .punt-labs at all, or no .punt-labs/ethos in it, is the
// ordinary "no store yet" (ENOENT of a component that is simply not there).
func ethosRootOf(storeRoot string) (string, error) {
	dir := storeRoot
	for _, name := range []string{".punt-labs", "ethos"} {
		dir = filepath.Join(dir, name)
		li, err := os.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", nil
		case err != nil:
			return "", fmt.Errorf("examining %s: %w", dir, err)
		}
		si, err := os.Stat(dir) // follows a symlink: a dangling one fails here
		switch {
		case err != nil:
			kind := ""
			if li.Mode()&fs.ModeSymlink != 0 {
				kind = " (a dangling symlink)"
			}
			return "", fmt.Errorf("the ethos store path %s cannot be read%s: %w", dir, kind, err)
		case !si.IsDir():
			return "", fmt.Errorf("the ethos store path %s is not a directory", dir)
		}
	}
	if err := checkDirAccess(dir); err != nil {
		return "", fmt.Errorf("the ethos store %s cannot be read: %w", dir, err)
	}
	return dir, nil
}

// checkDirAccess opens dir and reads one name (the read permission), and looks
// up a name that is not there (the search permission: ENOENT when it can be
// searched, EACCES when it cannot). A store directory ethos cannot list or
// search is a store it cannot use, not a missing one.
func checkDirAccess(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	_, rerr := f.Readdirnames(1)
	cerr := f.Close()
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return rerr
	}
	if cerr != nil {
		return cerr
	}
	if _, err := os.Lstat(filepath.Join(dir, ".ethos-access-probe")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// EnabledMarker reports whether ethos is enabled in the work tree root: the
// marker .punt-labs/ethos/enabled is there. The path is validated as EthosDirOf
// does, so a store ethos cannot read is an error, not "not enabled"; and a
// marker that exists but is not a regular file (a directory, a FIFO, a socket,
// a link to nothing) is an error too: it is neither enabled nor not. The hook
// scripts apply the same rule in shell (plugin/hooks/pre-commit.sh).
func EnabledMarker(root string) (bool, error) {
	ethos, err := ethosRootOf(root)
	if err != nil {
		return false, err
	}
	if ethos == "" {
		return false, nil
	}
	marker := filepath.Join(ethos, "enabled")
	li, err := os.Lstat(marker)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("examining the enabled marker %s: %w", marker, err)
	}
	if li.Mode().IsRegular() {
		return true, nil
	}
	if li.Mode()&fs.ModeSymlink != 0 {
		if si, serr := os.Stat(marker); serr == nil && si.Mode().IsRegular() {
			return true, nil
		}
	}
	return false, fmt.Errorf("the enabled marker %s is not a regular file", marker)
}

// EthosDirOf is .punt-labs/ethos under root, validated component by component
// as Roots validates the store: "" with a nil error when a component is simply
// absent, an error for a dangling link, a non-directory or a directory that
// cannot be read. It is not a locator: it examines a root the caller already
// has (a checkout from Located), so a check that reads a marker there does not
// mistake an unreadable store for a missing one.
func EthosDirOf(root string) (string, error) { return ethosRootOf(root) }

// ConfigOf reads the repo-local ethos configuration under a store root that
// Roots (or a caller holding its result) established: .punt-labs/ethos.yaml
// first, then the legacy .punt-labs/ethos/config.yaml. It returns nil, nil only
// when neither exists; a config that is a dangling symlink, unreadable or
// malformed is an error, never "no config".
func ConfigOf(storeRoot string) (*RepoConfig, error) { return loadRepoConfig(storeRoot) }

// loadRepoConfig reads repo-local ethos configuration. Tries
// .punt-labs/ethos.yaml first, falls back to the legacy path
// .punt-labs/ethos/config.yaml. Returns nil, nil when neither exists.
func loadRepoConfig(repoRoot string) (*RepoConfig, error) {
	newPath := filepath.Join(repoRoot, ".punt-labs", "ethos.yaml")
	used := newPath
	data, err := readConfigFile(newPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		// New path not found — try legacy path.
		oldPath := filepath.Join(repoRoot, ".punt-labs", "ethos", "config.yaml")
		used = oldPath
		data, err = readConfigFile(oldPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return nil, err
		}
	}
	var cfg RepoConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing repo config %s: %w", used, err)
	}
	return &cfg, nil
}

// readConfigFile reads p. A path that is absent is fs.ErrNotExist; a path that is
// a symlink to nothing is NOT absent, it is an error: ReadFile reports both as
// ENOENT.
func readConfigFile(p string) ([]byte, error) {
	data, err := os.ReadFile(p)
	if err == nil {
		return data, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		if li, lerr := os.Lstat(p); lerr == nil && li.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("reading %s: %w", p, errDanglingConfig)
		}
		return nil, err
	}
	return nil, fmt.Errorf("reading %s: %w", p, err)
}

var errDanglingConfig = errors.New("it is a dangling symlink")

// ResolveAgent returns the default agent identity handle for the repo.
// Reads .punt-labs/ethos.yaml first, falls back to legacy
// .punt-labs/ethos/config.yaml.
//
// Returns ("", nil) when not in a git repo (repoRoot == "") or when
// no repo config exists (cfg == nil) — neither is an error condition.
// Returns ("", err) when the config file exists but cannot be read
// or parsed: the caller decides whether to fail-closed, fail-open, or
// surface the error diagnostically.
func ResolveAgent(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", nil
	}
	cfg, err := loadRepoConfig(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve agent: %w", err)
	}
	if cfg == nil {
		return "", nil
	}
	return cfg.Agent, nil
}

// ResolveTeam returns the team name from repo config. Same error
// contract as ResolveAgent: ("", nil) for no-repo and not-configured,
// ("", err) for read or parse failures.
func ResolveTeam(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", nil
	}
	cfg, err := loadRepoConfig(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve team: %w", err)
	}
	if cfg == nil {
		return "", nil
	}
	return cfg.Team, nil
}

// ResolveMaxDelegationDepth returns the depth ceiling for the
// PreToolUse-on-Agent dispatch (DES-054 v5). Returns (defaultValue, nil)
// when no repo is in scope or the config file is absent; returns the
// configured value when non-zero; returns (defaultValue, nil) when the
// config exists but sets max_delegation_depth to zero (the "leave at
// default" sentinel).
//
// A negative configured value is a configuration error and surfaces as
// an error rather than silently flipping to the default — a negative
// depth would refuse every spawn including the root, and an operator
// who typed -16 instead of 16 should see the diagnostic.
func ResolveMaxDelegationDepth(repoRoot string, defaultValue int) (int, error) {
	if repoRoot == "" {
		return defaultValue, nil
	}
	cfg, err := loadRepoConfig(repoRoot)
	if err != nil {
		return defaultValue, fmt.Errorf("resolve max_delegation_depth: %w", err)
	}
	if cfg == nil || cfg.MaxDelegationDepth == 0 {
		return defaultValue, nil
	}
	if cfg.MaxDelegationDepth < 0 {
		return defaultValue, fmt.Errorf(
			"resolve max_delegation_depth: configured value %d is negative",
			cfg.MaxDelegationDepth,
		)
	}
	return cfg.MaxDelegationDepth, nil
}

// ResolveResolution returns the repo's layer policy: ResolutionLayered or
// ResolutionRepoOnly. An absent repo, an absent config, and an empty field
// all mean layered — the behavior every repo had before the field existed.
//
// An unrecognized value is an error, not a silent fall-back to layered: a
// typo like "repo_only" would otherwise leave the global fallback quietly
// in place, which is precisely the state repo-only exists to eliminate.
func ResolveResolution(repoRoot string) (string, error) {
	if repoRoot == "" {
		return ResolutionLayered, nil
	}
	cfg, err := loadRepoConfig(repoRoot)
	if err != nil {
		return ResolutionLayered, fmt.Errorf("resolve resolution: %w", err)
	}
	if cfg == nil || cfg.Resolution == "" {
		return ResolutionLayered, nil
	}
	switch cfg.Resolution {
	case ResolutionLayered, ResolutionRepoOnly:
		return cfg.Resolution, nil
	default:
		return ResolutionLayered, fmt.Errorf(
			"resolve resolution: unknown resolution %q: must be %q or %q",
			cfg.Resolution, ResolutionLayered, ResolutionRepoOnly)
	}
}

// ResolveActiveBundle returns the configured active_bundle name for a
// repo, or empty string if not configured (or not in a repo). Same
// error contract as ResolveAgent: ("", nil) for no-repo and
// not-configured, ("", err) for read or parse failures.
func ResolveActiveBundle(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", nil
	}
	cfg, err := loadRepoConfig(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve active bundle: %w", err)
	}
	if cfg == nil {
		return "", nil
	}
	return cfg.ActiveBundle, nil
}

// RepoRootOverride reports the ETHOS_REPO_ROOT override (whitespace-trimmed)
// and whether it is set, WITHOUT validating it or emitting any warning. It
// lets a caller distinguish "no override, genuinely not in a repo" (set ==
// false) from "an override was set but the resolver refused it" (set == true
// while locateRepoRoot/locateStoreRoot refused it). setup uses it to fail loud on
// a bad override instead of silently degrading to no-repo mode (#370 F-A).
func RepoRootOverride() (value string, set bool) {
	v := strings.TrimSpace(os.Getenv("ETHOS_REPO_ROOT"))
	return v, v != ""
}

// checkRepoRootOverride validates an ETHOS_REPO_ROOT override: it must name an
// existing directory, and when requireStore is set it must also hold a
// .punt-labs/ethos store. A set-but-invalid override is a loud error naming the
// bad root and what is missing — an override that lies about the location
// silently writes to the wrong tree, the exact ethos-yofr symptom (SFH F1). On
// an invalid override it returns ("", true, err): the caller does NOT fall
// through to auto-resolution and quietly pick a different tree.
func checkRepoRootOverride(requireStore bool) (root string, set bool, err error) {
	v, ok := RepoRootOverride()
	if !ok {
		return "", false, nil
	}
	if info, err := os.Stat(v); err != nil || !info.IsDir() {
		return "", true, fmt.Errorf("ETHOS_REPO_ROOT=%q is not an existing directory; refusing to use it", v)
	}
	if requireStore {
		// Only a store that is not there is the "fresh repository" enable
		// fills in; one that is there and cannot be read is its own error.
		ethos, err := ethosRootOf(v)
		if err != nil {
			return "", true, fmt.Errorf("ETHOS_REPO_ROOT=%q: refusing to use it: %w", v, err)
		}
		if ethos == "" {
			return "", true, fmt.Errorf("ETHOS_REPO_ROOT=%q has no %s store; refusing to use it: %w",
				v, filepath.Join(".punt-labs", "ethos"), ErrOverrideNoStore)
		}
	}
	return v, true, nil
}

// checkoutRootIfAny is the current work tree root — the directory holding the
// .git marker for the cwd's checkout — or "" with a nil error outside any
// repository. Any other failure (an invalid ETHOS_REPO_ROOT, a .git git
// refuses, a git that does not answer) is the error. In a linked worktree it
// is the WORKTREE root, not the main one: per-checkout operations
// (enable/disable markers, generated .claude/agents, audit chunks that must
// travel in this worktree's commit) belong to the checkout, not the shared
// store; store readers and writers use storeRootIfAny. ETHOS_REPO_ROOT
// overrides resolution (see locateRepoRoot).
func checkoutRootIfAny() (string, error) {
	root, err := locateRepoRoot()
	if errors.Is(err, ErrNotInRepo) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return root, nil
}

// warned holds the messages warnOnce has printed.
var warned sync.Map

// warnOnce prints a message to stderr at most once per locateTTL: the
// resolvers run on every command and hook, and the same skipped .git would
// otherwise repeat; a long-running server shows it again once the TTL passes,
// when it is still true.
func warnOnce(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	now := locateNow()
	if v, ok := warned.Load(msg); ok && now.Sub(v.(time.Time)) < locateTTL {
		return
	}
	warned.Store(msg, now)
	fmt.Fprintln(os.Stderr, msg)
}

// locateRepoRoot locates the current work tree root, with the reason it found nothing: an invalid
// ETHOS_REPO_ROOT, an unreadable working directory, a .git git refuses, or a
// working directory outside any repository. It returns the error rather than
// printing it; a .git it walked past is still noted on stderr (warnOnce).
func locateRepoRoot() (string, error) {
	if root, set, err := checkRepoRootOverride(false); set {
		return root, err
	}
	loc, err := locateCwd()
	return loc.root, err
}

// ErrOverrideNoStore is wrapped by the error locateStoreRoot returns when
// ETHOS_REPO_ROOT names an existing directory that holds no store yet: a fresh
// repository, which `ethos enable` is about to give one.
var ErrOverrideNoStore = errors.New("no ethos store at the overridden root")

// ErrNotInRepo is wrapped by the error locateRepoRoot returns when the working
// directory is outside any repository, the one failure that is not a problem.
var ErrNotInRepo = errors.New("not inside a git repository")

// storeRootIfAny is the root of the repository whose .punt-labs/ethos store
// the caller should read and write, or "" with a nil error outside any
// repository. Any other failure — a refused ETHOS_REPO_ROOT, a .git git
// refuses, a worktree whose store cannot be named — is the error, and a
// writer must refuse rather than fall back to the global store.
//
// It differs from checkoutRootIfAny only inside a linked worktree. Git keeps
// one shared object store per repo; a linked worktree's .git is a file pointing
// at the main work tree's .git, and — in the org-standard submodule layout —
// the worktree's own .punt-labs/ethos is an empty, unpopulated checkout.
// Resolving through the common dir means an agent working in
// <repo>/.claude/worktrees/x reads and writes the store at
// <repo>/.punt-labs/ethos rather than a different, empty tree that silently
// degrades to the global store (ethos-yofr).
func storeRootIfAny() (string, error) {
	root, err := locateStoreRoot()
	if errors.Is(err, ErrNotInRepo) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return root, nil
}

// locateStoreRoot locates the store root, with the reason it found nothing: a refused
// ETHOS_REPO_ROOT, a .git git refuses, or a working directory outside any
// repository (wrapping ErrNotInRepo, the one failure that is not a problem). A
// writer must refuse on any other error rather than fall back to the global
// store. It returns the error rather than printing it; a .git it walked past
// is still noted on stderr (warnOnce).
func locateStoreRoot() (string, error) {
	if root, set, err := checkRepoRootOverride(true); set {
		return root, err
	}
	loc, err := locateCwd()
	if err == nil && loc.storeErr != nil {
		return "", loc.storeErr
	}
	return loc.store, err
}

// location is where resolution found the repository for a directory.
type location struct {
	root     string // the work tree root
	store    string // the work tree whose .punt-labs/ethos is the store
	storeErr error  // why the store cannot be named, when root can
}

// located is one cached resolution: the location, the .git entries walked
// past on the way, and the error.
type located struct {
	loc     location
	skipped []string // "path: reason", for the skip notice
	err     error
}

// locations caches answers, keyed on the git binary (or its absence) and the
// starting directory: the resolvers run many times per command, and each
// answer from git costs a process. Only a location and ErrNotInRepo are
// cached, and only for locateTTL, so a long-running server sees a repaired or
// new repository; a refusal is never cached.
var locations sync.Map

// locateTTL bounds how long a cached answer is reused.
const locateTTL = 30 * time.Second

// locateNow is the clock the cache reads; a test may replace it.
var locateNow = time.Now

// cachedLocation is a located answer and when it was found.
type cachedLocation struct {
	r  located
	at time.Time
}

// locateCwd resolves the working directory and notes on stderr every .git it
// walked past.
func locateCwd() (location, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return location{}, fmt.Errorf("getting the working directory: %w", err)
	}
	r := locate(cwd)
	for _, s := range r.skipped {
		warnOnce("ethos: warning: skipping %s", s)
	}
	return r.loc, r.err
}

// locate resolves the repository containing start. When a working git is on
// PATH, git is the authority (gitLocate); without one the hand rules decide
// (handLocate).
func locate(start string) located {
	git, err := usableGit()
	if err != nil {
		return located{err: err} // a refusal: never cached
	}
	key := git + "\x00" + start
	if v, ok := locations.Load(key); ok {
		if c := v.(cachedLocation); locateNow().Sub(c.at) < locateTTL {
			return c.r
		}
	}
	var r located
	if git != "" {
		r = gitLocate(git, start)
	} else {
		r = handLocate(start)
	}
	if (r.err == nil && r.loc.storeErr == nil) || errors.Is(r.err, ErrNotInRepo) {
		locations.Store(key, cachedLocation{r: r, at: locateNow()})
	} else {
		locations.Delete(key)
	}
	return r
}

// gitProbe is what `git --version` said about one git binary, and when.
type gitProbe struct {
	version string    // "git version 2.43.0"
	err     error     // why it did not run
	at      time.Time // when the probe ran
}

// probes holds one gitProbe per git path. A success is kept for the life of
// the process; a failure is retried after locateTTL, so a long-running server
// uses a git that has started to run.
var probes sync.Map

// usableGit returns the git on PATH when it runs, else "". A git that is on
// PATH but does not run (the macOS xcrun shim without the command line tools)
// is noted once; the hand rules decide in its place. A git that does not
// answer within gitenv.Timeout is an error: a refusal, not a reason to guess
// by the hand rules.
func usableGit() (string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", nil
	}
	p := probeGit(git)
	switch {
	case errors.Is(p.err, gitenv.ErrTimeout):
		return "", fmt.Errorf("finding the repository: %w", p.err)
	case p.err != nil:
		warnOnce("ethos: warning: %s does not run (%v); finding the repository by the hand rules", git, p.err)
		return "", nil
	}
	return git, nil
}

// probeGit runs `git --version` for git and remembers the answer: a success
// for good, a failure for locateTTL, a timeout not at all.
func probeGit(git string) gitProbe {
	if v, ok := probes.Load(git); ok {
		if p := v.(gitProbe); p.err == nil || locateNow().Sub(p.at) < locateTTL {
			return p
		}
	}
	out, err := gitenv.Output("", git, "--version")
	p := gitProbe{version: strings.TrimSpace(string(out)), at: locateNow()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		p.err = err
	}
	if !errors.Is(p.err, gitenv.ErrTimeout) {
		probes.Store(git, p)
	}
	return p
}

// gitLocate asks git for the work tree root, the git dir and the common dir of
// start. git's "not a git repository" is ErrNotInRepo; any other git failure
// (dubious ownership, an invalid gitfile, an unreadable commondir, "must be
// run in a work tree") is a resolution error carrying git's own message.
//
// The options are the ones git has had since 2.13. git runs in the C locale,
// so its messages are the English ones matched here, and without GIT_DIR,
// GIT_WORK_TREE or GIT_COMMON_DIR, so it discovers the repository from start
// rather than answering for the one an exported variable names (gitenv). An
// answer that is not three paths, from a git older than those options, falls
// back to the hand rules.
func gitLocate(git, start string) located {
	out, err := gitenv.Output(start, git, "rev-parse", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	if errors.Is(err, gitenv.ErrTimeout) {
		return located{err: fmt.Errorf("finding the repository for %s: %w", start, err)}
	}
	if err != nil {
		if dot := danglingOnWalk(gitWalk(start)); dot != nil {
			return located{err: dot}
		}
		return located{err: gitLocateError(start, err)}
	}
	top, gitDir, common, ok := parseRevParse(start, string(out))
	if !ok {
		warnOnce("ethos: warning: %s (%s) answered `git rev-parse --show-toplevel --absolute-git-dir --git-common-dir` with %q, not three paths; finding the repository by the hand rules",
			git, probeGit(git).version, out)
		return handLocate(start)
	}
	root, under := ancestorAt(start, top)
	var skipped []string
	if under {
		// Every .git between start and the root git chose was walked past.
		for dir := start; dir != root; dir = filepath.Dir(dir) {
			if derr := gitenv.DanglingDotGit(dir); derr != nil {
				return located{err: derr}
			}
			dot := filepath.Join(dir, ".git")
			if _, err := os.Lstat(dot); !errors.Is(err, fs.ErrNotExist) {
				skipped = append(skipped, dot+": git does not accept it as a repository")
			}
		}
	}
	store, serr := storeRoot(root, gitDir, common, git)
	return located{loc: location{root: root, store: store, storeErr: serr}, skipped: skipped}
}

// danglingOnWalk is the first dangling .git symlink among dirs, or nil.
func danglingOnWalk(dirs []string) error {
	for _, d := range dirs {
		if err := gitenv.DanglingDotGit(d); err != nil {
			return err
		}
	}
	return nil
}

// parseRevParse reads the three lines gitLocate asks for: the top level and
// the git dir, both absolute, and the common dir, which git prints relative
// to the directory it ran in when it is not absolute. That directory is the
// physical one (git's getcwd resolves symlinks), not start's logical form, so
// a relative common dir is joined to start with its symlinks resolved.
func parseRevParse(start, out string) (top, gitDir, common string, ok bool) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		return "", "", "", false
	}
	top, gitDir, common = lines[0], lines[1], lines[2]
	if !filepath.IsAbs(top) || !filepath.IsAbs(gitDir) || common == "" || strings.HasPrefix(common, "-") {
		return "", "", "", false
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(realDir(start), common)
	}
	return top, gitDir, common, true
}

// gitLocateError turns a failed `git rev-parse` into a resolution error. It
// names the nearest .git only when that helps: not when git's message already
// names its work tree, the .git, or a path inside the .git (messageNamesGit),
// and not when the .git is at or above a GIT_CEILING_DIRECTORIES entry, which
// git never examined.
func gitLocateError(start string, err error) error {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return fmt.Errorf("running git rev-parse in %s: %w", start, err)
	}
	msg := strings.TrimSpace(string(ee.Stderr))
	if gitenv.IsNotARepo(msg) {
		// git is the authority: a .git it walked past, or a mount or
		// ceiling boundary it stopped at, does not make this a refusal.
		return fmt.Errorf("%s is %w", start, ErrNotInRepo)
	}
	if dot := firstDotGit(start); dot != "" && !messageNamesGit(msg, dot) {
		return fmt.Errorf("git cannot resolve the repository for %s at %s: %s", start, dot, msg)
	}
	return fmt.Errorf("git cannot resolve the repository for %s: %s", start, msg)
}

// messageNamesGit reports whether git's message msg names the work tree that
// holds dot, or dot or a path inside it. Paths are matched as whole
// components: the work tree /r/a is not named by /r/ab or /x/r/a, and every
// absolute path is not "in" the root work tree.
func messageNamesGit(msg, dot string) bool {
	return namesPath(msg, filepath.Dir(dot), false) || namesPath(msg, dot, true)
}

// namesPath reports whether msg holds p as a whole path: preceded by the start
// of msg or a delimiter, and followed by the end of msg, a delimiter, a
// sentence-ending period, or (when under is set) a separator.
func namesPath(msg, p string, under bool) bool {
	for i := 0; ; {
		j := strings.Index(msg[i:], p)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(p)
		before := start == 0 || isPathDelim(msg[start-1])
		after := end == len(msg) || isPathDelim(msg[end]) ||
			msg[end] == '.' && (end+1 == len(msg) || isPathDelim(msg[end+1])) ||
			under && msg[end] == filepath.Separator
		if before && after {
			return true
		}
		i = start + 1
	}
}

// isPathDelim reports whether c ends a path in one of git's messages.
func isPathDelim(c byte) bool {
	return strings.IndexByte(" \t\r\n'\"`,;:()[]<>=", c) >= 0
}

// realDir returns p with symlinks resolved, or p cleaned when it cannot be.
func realDir(p string) string { return gitenv.RealDir(p) }

// firstDotGit returns the nearest .git entry git examines from start, or "".
func firstDotGit(start string) string {
	for _, dir := range gitWalk(start) {
		dot := filepath.Join(dir, ".git")
		if _, err := os.Lstat(dot); !errors.Is(err, fs.ErrNotExist) {
			return dot
		}
	}
	return ""
}

// gitWalk is the directories git examines from start (gitenv.Walk).
func gitWalk(start string) []string { return gitenv.Walk(start) }

// parseGitBool parses a git boolean (gitenv.ParseBool).
func parseGitBool(s string) (b, ok bool) { return gitenv.ParseBool(s) }

// ancestorAt returns the ancestor of start (or start itself) that is the
// directory target, keeping the caller's path form (git reports symlinks
// resolved), and whether there is one. Otherwise it returns target.
func ancestorAt(start, target string) (string, bool) {
	ti, err := os.Stat(target)
	if err != nil {
		return target, false
	}
	for dir := start; ; {
		if di, err := os.Stat(dir); err == nil && os.SameFile(di, ti) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return target, false
		}
		dir = parent
	}
}

// storeRoot returns the work tree that holds the store. A main tree, a
// submodule and a separate git dir, whose git dir is their common dir, keep it
// in their own work tree. A linked worktree is decided by its common dir: a
// bare one has no work tree, so the store is the worktree's own; a non-bare
// one that is the .git of its parent work tree puts the store in that main
// checkout. Any other common dir (a separate git dir, a submodule's
// .git/modules/<name>) names no work tree ethos can trust: the store is
// refused rather than guessed. git, when not "", reads core.bare; otherwise
// the hand rules do.
func storeRoot(root, gitDir, common, git string) (string, error) {
	if sameDir(gitDir, common) {
		return root, nil
	}
	common = filepath.Clean(common)
	bare, err := configBare(git, common)
	if err != nil {
		return "", fmt.Errorf("%s is a linked worktree of the repository at %s, whose core.bare cannot be read (%w); "+
			"ethos cannot tell where its store is: set ETHOS_REPO_ROOT", root, common, err)
	}
	if bare {
		return root, nil
	}
	main := filepath.Dir(common)
	if !sameDir(filepath.Join(main, ".git"), common) {
		return "", fmt.Errorf("%s is a linked worktree of the repository at %s, which is neither bare nor the .git of a work tree "+
			"(a separate git dir or a submodule's git dir); ethos cannot tell where its store is: "+
			"run from a work tree that holds the store, or set ETHOS_REPO_ROOT", root, common)
	}
	return main, nil
}

// configBare reports whether the repository at common sets core.bare. git,
// when not "", is asked; otherwise handConfigBare reads the file.
func configBare(git, common string) (bool, error) {
	if git == "" {
		return handConfigBare(common)
	}
	cfg := filepath.Join(common, "config")
	// In a neutral directory: `git config --file` reads no repository, and a
	// broken .git in the working directory must not fail it.
	dir, env, cleanup, nerr := gitenv.Neutral()
	if nerr != nil {
		return false, nerr
	}
	defer cleanup()
	out, err := gitenv.Run(gitenv.Spec{Git: git, Dir: dir, Env: env, Args: []string{"config", "--file", cfg, "--bool", "core.bare"}})
	var ee *exec.ExitError
	switch {
	case err == nil:
		return strings.TrimSpace(string(out)) == "true", nil
	case errors.As(err, &ee) && ee.ExitCode() == 1 && gitenv.WithoutTrace(string(ee.Stderr)) == "":
		// Unset, or no config file. Trace lines (a trace2 target in the user's
		// config) are not part of the answer; any other stderr is — an
		// unreadable config exits 1 with "warning: unable to access".
		return false, nil
	case errors.As(err, &ee) && len(ee.Stderr) > 0:
		return false, fmt.Errorf("git config --file %s core.bare: %s", cfg, strings.TrimSpace(string(ee.Stderr)))
	default:
		return false, fmt.Errorf("git config --file %s core.bare: %w", cfg, err)
	}
}

// handConfigBare reads core.bare from common/config as git reads a boolean:
// the last assignment in a [core] section (no subsection) wins; a key with no
// value is true; any other value is read by parseGitBool. An absent file or
// key is false. include directives are not followed.
func handConfigBare(common string) (bool, error) {
	cfg := filepath.Join(common, "config")
	data, err := readRegular(cfg)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", cfg, err)
	}
	bare, inCore := false, false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end < 0 {
				return false, fmt.Errorf("%s: malformed section header %q", cfg, line)
			}
			inCore = strings.EqualFold(strings.TrimSpace(line[1:end]), "core")
			line = strings.TrimSpace(line[end+1:])
		}
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		if !inCore || line == "" {
			continue
		}
		key, val, hasVal := strings.Cut(line, "=")
		if !strings.EqualFold(strings.TrimSpace(key), "bare") {
			continue
		}
		if !hasVal {
			bare = true
			continue
		}
		b, ok := parseGitBool(strings.Trim(strings.TrimSpace(val), `"`))
		if !ok {
			return false, fmt.Errorf("%s: core.bare = %q is not a boolean", cfg, strings.TrimSpace(val))
		}
		bare = b
	}
	return bare, nil
}

// sameDir reports whether a and b name the same directory: by file identity
// when both exist (git prints symlinks resolved), else by cleaned path.
func sameDir(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	if aerr == nil && berr == nil {
		return os.SameFile(ai, bi)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// handLocate walks the directories git would examine from start (gitWalk:
// the physical path, stopping at GIT_CEILING_DIRECTORIES and at a filesystem
// boundary) to the first .git the hand rules accept (see examineGit). A
// directory that is itself a git directory (a bare repository, or the inside
// of a .git) is refused, as git refuses it: not a work tree. The root keeps
// the caller's path form where it is an ancestor of start. Used only without
// a git that runs.
//
// The hand rules still differ from git in these ways:
//   - an unreadable .git directory is refused, where git skips it and
//     resolves the enclosing repository;
//   - core.worktree in a .git directory is not honoured: the work tree is
//     always the directory holding .git;
//   - safe.directory ownership checks, safe.bareRepository, and the
//     repository format version and extensions are not checked;
//   - include directives are not followed when reading core.bare;
//   - a GIT_DISCOVERY_ACROSS_FILESYSTEM that is not a boolean is read as
//     unset, where git dies; a parent whose device cannot be read ends the
//     walk, where git dies.
func handLocate(start string) located {
	var skipped []string
	for _, dir := range gitWalk(start) {
		dot := filepath.Join(dir, ".git")
		g, skip, err := examineGit(dot)
		if err != nil {
			return located{skipped: skipped, err: err}
		}
		if g.dir != "" {
			root, _ := ancestorAt(start, dir)
			store, serr := storeRoot(root, g.dir, g.common, "")
			return located{loc: location{root: root, store: store, storeErr: serr}, skipped: skipped}
		}
		if skip != "" {
			skipped = append(skipped, dot+": "+skip)
		}
		if _, reason, err := checkGitDir(dir); err == nil && reason == "" {
			// git finds dir itself to be a git directory: a bare repository
			// or the inside of a .git, neither a work tree.
			return located{skipped: skipped, err: fmt.Errorf(
				"%s is inside the git directory %s, not a work tree; ethos must be run in a work tree", start, dir)}
		}
	}
	return located{skipped: skipped, err: fmt.Errorf("%s is %w", start, ErrNotInRepo)}
}

// gitDirs is a git directory and its common directory.
type gitDirs struct{ dir, common string }

// examineGit classifies a .git path by the hand rules: found (g.dir set) when
// it is a git directory or a gitfile naming one; a skip reason when it is
// walked past (empty when the path does not exist); an error when it is
// refused or cannot be read. It never opens anything but a regular file or a
// directory.
//
// The rules follow git where it is strict: a gitfile is the exact bytes
// "gitdir: " at offset 0 then a path (only trailing CR/LF stripped), and its
// target must itself be a git directory (checkGitDir). A .git directory that
// cannot be read is an error, where git skips it and resolves the enclosing
// repository — writing there would put the store in a tree the user may not
// mean. handLocate lists the other differences from git.
func examineGit(dot string) (g gitDirs, skip string, err error) {
	if _, err := os.Lstat(dot); errors.Is(err, fs.ErrNotExist) {
		return gitDirs{}, "", nil
	} else if err != nil {
		return gitDirs{}, "", fmt.Errorf("examining %s: %w", dot, err)
	}
	info, err := os.Stat(dot)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ELOOP):
		// git walks past it to an enclosing repository; that is not the one
		// the user is in, so it is refused, not skipped.
		return gitDirs{}, "", gitenv.DanglingDotGit(filepath.Dir(dot))
	case err != nil:
		return gitDirs{}, "", fmt.Errorf("examining %s: %w", dot, err)
	case info.IsDir():
		common, reason, err := checkGitDir(dot)
		if err != nil {
			return gitDirs{}, "", fmt.Errorf("examining %s: %w", dot, err)
		}
		if reason != "" {
			return gitDirs{}, "not a git directory (" + reason + ")", nil
		}
		return gitDirs{dir: dot, common: common}, "", nil
	case !info.Mode().IsRegular():
		return gitDirs{}, "not a directory or a gitfile", nil
	}
	data, err := os.ReadFile(dot)
	if err != nil {
		return gitDirs{}, "", fmt.Errorf("examining %s: %w", dot, err)
	}
	target, ok := strings.CutPrefix(strings.TrimRight(string(data), "\r\n"), "gitdir: ")
	if !ok || target == "" {
		return gitDirs{}, "", fmt.Errorf("%s is not a valid gitfile (want \"gitdir: <path>\" at the start): %w", dot, errInvalidGit)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(dot), target)
	}
	common, reason, err := checkGitDir(target)
	if err != nil {
		return gitDirs{}, "", fmt.Errorf("%s names %s: %w", dot, target, err)
	}
	if reason != "" {
		return gitDirs{}, "", fmt.Errorf("%s names %s, which is not a git directory (%s): %w", dot, target, reason, errInvalidGit)
	}
	return gitDirs{dir: target, common: common}, "", nil
}

// errInvalidGit marks a .git the hand rules refuse for its content, not for a
// failure to read it.
var errInvalidGit = errors.New("invalid git file")

// checkGitDir applies git's test for a git directory to gd: HEAD a regular
// file holding a ref or an object id, and objects/ and refs/ directories in
// the common dir (gd, or the one its commondir file names). It returns the
// common dir, or the reason gd is not a git directory. A commondir that cannot
// be read or names nothing is an error, as is any entry that cannot be
// examined.
func checkGitDir(gd string) (common, reason string, err error) {
	head := filepath.Join(gd, "HEAD")
	info, err := os.Stat(head)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", "no HEAD", nil
	case err != nil:
		return "", "", fmt.Errorf("examining %s: %w", head, err)
	case !info.Mode().IsRegular():
		return "", "HEAD is not a regular file", nil
	}
	data, err := os.ReadFile(head)
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", head, err)
	}
	if !validHead(string(data)) {
		return "", "HEAD holds neither a ref nor an object id", nil
	}
	common = gd
	cd := filepath.Join(gd, "commondir")
	data, err = readRegular(cd)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// no commondir: gd is its own common dir
	case err != nil:
		return "", "", fmt.Errorf("reading %s: %w", cd, err)
	default:
		c := strings.TrimSpace(string(data))
		if c == "" {
			return "", "", fmt.Errorf("%s is empty: %w", cd, errInvalidGit)
		}
		if !filepath.IsAbs(c) {
			c = filepath.Join(gd, c)
		}
		info, err := os.Stat(c)
		switch {
		case errors.Is(err, fs.ErrNotExist) || err == nil && !info.IsDir():
			return "", "", fmt.Errorf("%s names %s, which is not a directory: %w", cd, c, errInvalidGit)
		case err != nil:
			return "", "", fmt.Errorf("examining %s: %w", c, err)
		}
		common = c
	}
	for _, name := range []string{"objects", "refs"} {
		p := filepath.Join(common, name)
		info, err := os.Stat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", "no " + name + " directory", nil
		case err != nil:
			return "", "", fmt.Errorf("examining %s: %w", p, err)
		case !info.IsDir():
			return "", name + " is not a directory", nil
		}
	}
	return common, "", nil
}

// readRegular reads p only when it is a regular file, so a FIFO or a device
// never blocks the caller. An absent p is fs.ErrNotExist; anything other than
// a regular file is errInvalidGit.
func readRegular(p string) ([]byte, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file: %w", p, errInvalidGit)
	}
	return os.ReadFile(p)
}

// HandGitDirs is not a locator: it reads the .git of a root the caller already
// has (from Located), by the hand rules, for a caller without a git that runs.
// It returns the git directory and the common directory of the .git
// in root, by the hand rules (examineGit) and without running git: it never
// opens anything but a regular file or a directory. An error says why there
// is none: root/.git is absent, walked past, refused, or unreadable.
func HandGitDirs(root string) (dir, common string, err error) {
	dot := filepath.Join(root, ".git")
	g, skip, err := examineGit(dot)
	switch {
	case err != nil:
		return "", "", err
	case g.dir != "":
		return g.dir, g.common, nil
	case skip != "":
		return "", "", fmt.Errorf("%s is not a repository: %s", dot, skip)
	default:
		return "", "", fmt.Errorf("%s: %w", dot, fs.ErrNotExist)
	}
}

// validHead reports whether HEAD's content is what git accepts: "ref:"
// followed by a refs/ path, or a hex object id (SHA-1 or SHA-256).
func validHead(s string) bool {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "ref:"); ok {
		return strings.HasPrefix(strings.TrimLeft(rest, " \t"), "refs/")
	}
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// CheckoutOf is the work tree root git (or, without a git that runs, the hand
// rules) finds for dir, through the same locator Roots uses: "" with a nil error
// outside any repository, otherwise the error says why it cannot be found. It
// answers for dir, not for the process: ETHOS_REPO_ROOT is not consulted. A
// caller that has the process's checkout takes it from Located.Checkout; this is
// for one that is given a directory to verify (githook's hooks lookup).
func CheckoutOf(dir string) (string, error) {
	r := locate(dir)
	if errors.Is(r.err, ErrNotInRepo) {
		return "", nil
	}
	if r.err != nil {
		return "", r.err
	}
	return r.loc.root, nil
}

// IsWorkingTree reports whether dir is the root of a working tree: it holds a
// .git that git (or, without a git that runs, the hand rules) accepts as a
// repository. A .git that is walked past or refused is not a working tree; an
// error is returned only when the .git cannot be read. It is a classifier for
// a directory it is handed (doctor's walk finds nested checkouts with it), not
// a locator: the process's repository comes from Roots.
func IsWorkingTree(dir string) (bool, error) {
	dot := filepath.Join(dir, ".git")
	if _, err := os.Lstat(dot); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("examining %s: %w", dot, err)
	}
	git, err := usableGit()
	if err != nil {
		return false, err
	}
	if git == "" {
		g, _, err := examineGit(dot)
		if errors.Is(err, errInvalidGit) {
			return false, nil
		}
		return g.dir != "", err
	}
	r := locate(dir)
	if errors.Is(r.err, gitenv.ErrTimeout) {
		return false, r.err
	}
	if r.err != nil {
		return false, checkReadable(dot) // git refuses it: not a working tree
	}
	ri, err := os.Stat(r.loc.root)
	if err != nil {
		return false, fmt.Errorf("examining %s: %w", r.loc.root, err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		return false, fmt.Errorf("examining %s: %w", dir, err)
	}
	return os.SameFile(ri, di), nil
}

// checkReadable opens p when it is a regular file or a directory (never
// anything that could block, such as a FIFO) and reports why it cannot be.
func checkReadable(p string) error {
	info, err := os.Stat(p)
	if err != nil || !(info.IsDir() || info.Mode().IsRegular()) {
		return nil // dangling, a loop, or special: walked past, not unreadable
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("examining %s: %w", p, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", p, err)
	}
	return nil
}

// RepoName returns the repository name (e.g. "punt-labs/ethos") of checkout,
// the work tree the resolver located, from its "origin" remote URL. It returns
// ("", nil) when checkout is "" (outside a repository) or the repository has
// no origin. Every other failure — a timeout, git not installed, dubious
// ownership — is an error carrying git's message. git runs in checkout, never
// in the working directory: the two differ under ETHOS_REPO_ROOT.
func RepoName(checkout string) (string, error) {
	if checkout == "" {
		return "", nil
	}
	out, absent, err := gitenv.Read(gitenv.Spec{
		Git: "git", Dir: checkout, Args: []string{"remote", "get-url", "origin"}, Env: gitenv.Parsed(),
	}, gitenv.AbsentNoRemote|gitenv.AbsentNotRepo)
	if err != nil {
		return "", fmt.Errorf("reading the origin remote in %s: %w", checkout, err)
	}
	if absent {
		return "", nil
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", nil
	}
	name := parseRepoName(url)
	if name == "" {
		fmt.Fprintf(os.Stderr, "ethos: could not parse repo name from remote URL %q\n", url)
	}
	return name, nil
}

// parseRepoName extracts "owner/repo" from a remote URL.
// Supports HTTPS (https://github.com/owner/repo.git) and
// SSH (git@github.com:owner/repo.git) formats.
func parseRepoName(url string) string {
	url = strings.TrimSuffix(url, ".git")

	var name string

	// SSH format: git@github.com:owner/repo
	// Exclude URLs with "://" (HTTPS, etc.).
	if i := strings.Index(url, ":"); i >= 0 && !strings.Contains(url, "://") {
		name = url[i+1:]
	} else {
		// HTTPS format: https://github.com/owner/repo
		parts := strings.Split(url, "/")
		if len(parts) >= 2 {
			name = parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	}

	// Reject malformed URLs where the result has no owner/repo separator.
	if !strings.Contains(name, "/") {
		return ""
	}
	return name
}

// GitConfig reads a single git config value as git would from checkout, the
// work tree the resolver located (its own config, then global, then system).
// With no checkout ("" — outside a repository, or one that could not be
// located) git runs in a neutral directory outside any repository, so only the
// user's config is read, never the working directory's repository: under
// ETHOS_REPO_ROOT that is some other repository.
// Returns ("", nil) when the key is unset, and when git is not installed:
// identity resolution does not require git, so a missing git has no config to
// read. Every other failure — a timeout, a corrupt config, dubious ownership —
// is an error carrying git's message, never an unset key.
func GitConfig(checkout, key string) (string, error) {
	spec := gitenv.Spec{Git: "git", Dir: checkout, Args: []string{"config", key}, Env: gitenv.Parsed()}
	where := checkout
	if checkout == "" {
		// A plain `git config` in a directory outside any repository: the whole
		// user config ([include], GIT_CONFIG_COUNT, -c) and no repository's.
		dir, env, cleanup, err := gitenv.Neutral()
		if err != nil {
			return "", err
		}
		defer cleanup()
		spec.Dir, spec.Env = dir, env
		where = "the user's git config"
	}
	out, absent, err := gitenv.Read(spec, gitenv.AbsentUnset)
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading git config %s in %s: %w", key, where, err)
	case absent:
		// "Unset" is only believed when every config file could be read: a git
		// that skips an unreadable file exits 1 as for an unset key.
		if err := gitenv.UnreadableUserConfig(); err != nil {
			return "", fmt.Errorf("reading git config %s in %s: %w", key, where, err)
		}
		return "", nil
	}
	v := strings.TrimSpace(string(out))
	// Strip surrounding quotes — some git configs store values with
	// embedded quotes (e.g., user.name = "\"jmf-pobox\"").
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	return v, nil
}

// Package resolve implements identity resolution chains for humans
// and agents. Humans are resolved from iam declarations, git config,
// or OS user. Agents are resolved from per-repo config.
package resolve

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

// Resolve returns the identity handle for the current caller.
//
// Resolution chain (stops at first match):
//  1. iam declaration — walk process tree for PID-keyed session file
//  2. git config user.name — match identity github field
//  3. git config user.email — match identity email field
//  4. $USER — match identity handle field
//
// Returns an error when no step matches.
func Resolve(store identity.IdentityStore, ss *session.Store) (string, error) {
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
	gitName := GitConfig("user.name")
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
	gitEmail := GitConfig("user.email")
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

	return "", fmt.Errorf("no identity matches git user %q, email %q, or OS user %q%s",
		gitName, gitEmail, osUser, repoOnlyHint(store))
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
// as opposed to the Claude process-tree walk.
const SessionSourceEnv = "env"

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
// terminal). That is a normal condition — no session was ever expected —
// and SessionID reports it as (id="", source="", err=nil) instead.
// Conflating the two was itself a defect DES-074 names explicitly: "no
// session" and "this git user" are different answers and must not be
// returned interchangeably, but neither may a CI run's total absence of a
// Claude Code session be treated as an alarming, loud failure.
// The message carries no "ethos: " prefix — cmd/ethos's top-level error
// printer adds that once; prefixing it here doubled it to "ethos: ethos:
// ..." (Bugbot/team-lead HIGH, round 2).
var ErrNoSession = errors.New(
	"cannot identify the calling session — set ETHOS_SESSION=<id>, or run `ethos session start`")

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
const (
	pointerRetryAttempts = 10
	pointerRetryDelay    = 50 * time.Millisecond
)

// SessionID resolves the active session ID using the harness-neutral chain:
// ETHOS_SESSION, then the Claude process-tree current-pointer.
//
// Three outcomes (DES-074):
//   - id != "", err == nil: resolved. source names SessionSourceEnv or "walk".
//   - id == "", err == nil: not running under Claude Code at all (headless,
//     CI, SDK, a plain terminal) — a normal state. No session was ever
//     expected; callers may silently try another identity source.
//   - id == "", err == ErrNoSession: a session WAS expected (running under
//     Claude Code, per process.UnderClaudeCode) but could not be
//     identified. Callers must fail loud with a non-zero exit and must NOT
//     substitute another identity source.
//
// Callers that accept an explicit session (a --session flag or an MCP
// session_id arg) check that first and bypass this (DES-061).
func SessionID(ss *session.Store) (id, source string, err error) {
	if sid := os.Getenv("ETHOS_SESSION"); sid != "" {
		return sid, SessionSourceEnv, nil
	}

	pid := process.FindClaudePID()
	underClaude := UnderClaudeCode()

	sid, rerr := ss.ReadCurrentSession(pid)
	if rerr != nil && underClaude {
		sid, rerr = retryReadCurrentSession(ss, pid)
	}
	if rerr != nil {
		if !underClaude {
			return "", "", nil
		}
		// Wrap rerr rather than returning the bare ErrNoSession sentinel:
		// "set ETHOS_SESSION" is the right remedy for the common case (no
		// pointer file — the retry above just confirmed it, there is no
		// session), but a permission error or a corrupt file is a
		// DIFFERENT, determinable cause that remedy would not fix (round
		// 2, R6). errors.Is(err, ErrNoSession) still holds for every
		// caller that checks it, since %w preserves the chain.
		return "", "", fmt.Errorf("%w (%v)", ErrNoSession, rerr)
	}
	return sid, "walk", nil
}

// retryReadCurrentSession re-reads the PID-keyed pointer file a few times
// with a short delay, covering the startup race where a consumer runs
// before SessionStart finishes writing it.
func retryReadCurrentSession(ss *session.Store, pid string) (string, error) {
	var lastErr error
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
// (ETHOS_SESSION, then the Claude PID walk), then returns the caller's
// persona from the roster. The caller's participant is keyed on
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
	sessionID, _, err := SessionID(ss)
	if err != nil {
		return sessionPersona{}, err
	}
	if sessionID == "" {
		return sessionPersona{}, nil
	}
	roster, lErr := ss.Load(sessionID)
	if lErr != nil {
		if errors.Is(lErr, os.ErrNotExist) {
			return sessionPersona{}, fmt.Errorf("session %q not found: %w", sessionID, ErrNoSession)
		}
		return sessionPersona{}, fmt.Errorf("session %q has an unreadable roster (%v): %w", sessionID, lErr, ErrNoSession)
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

// FindRepoEthosRoot returns the path to .punt-labs/ethos/ for the repo the
// store should read and write, or empty string if not in a repo or the
// directory doesn't exist. It resolves through StoreRepoRoot, so a linked
// worktree finds the main work tree's store rather than its own empty
// checkout (ethos-yofr).
func FindRepoEthosRoot() string {
	repoRoot := StoreRepoRoot()
	if repoRoot == "" {
		return ""
	}
	ethosRoot := filepath.Join(repoRoot, ".punt-labs", "ethos")
	if info, err := os.Stat(ethosRoot); err == nil && info.IsDir() {
		return ethosRoot
	}
	return ""
}

// LoadRepoConfig reads repo-local ethos configuration. Tries
// .punt-labs/ethos.yaml first, falls back to the legacy path
// .punt-labs/ethos/config.yaml. Returns nil, nil when neither exists.
func LoadRepoConfig(repoRoot string) (*RepoConfig, error) {
	newPath := filepath.Join(repoRoot, ".punt-labs", "ethos.yaml")
	data, err := os.ReadFile(newPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading %s: %w", newPath, err)
		}
		// New path not found — try legacy path.
		oldPath := filepath.Join(repoRoot, ".punt-labs", "ethos", "config.yaml")
		data, err = os.ReadFile(oldPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("reading %s: %w", oldPath, err)
		}
	}
	var cfg RepoConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing repo config: %w", err)
	}
	return &cfg, nil
}

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
	cfg, err := LoadRepoConfig(repoRoot)
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
	cfg, err := LoadRepoConfig(repoRoot)
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
	cfg, err := LoadRepoConfig(repoRoot)
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
	cfg, err := LoadRepoConfig(repoRoot)
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
	cfg, err := LoadRepoConfig(repoRoot)
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
// while FindRepoRoot/StoreRepoRoot returned ""). setup uses it to fail loud on
// a bad override instead of silently degrading to no-repo mode (#370 F-A).
func RepoRootOverride() (value string, set bool) {
	v := strings.TrimSpace(os.Getenv("ETHOS_REPO_ROOT"))
	return v, v != ""
}

// repoRootOverride returns the ETHOS_REPO_ROOT override and whether it was
// set. A set override is validated: it must name an existing directory, and
// when requireStore is set it must also hold a .punt-labs/ethos store. A
// set-but-invalid override is a loud error naming the bad root and what is
// missing — an override that lies about the location silently writes to the
// wrong tree, the exact ethos-yofr symptom (SFH F1). On an invalid override
// it returns ("", true): "an override was set" so the caller does NOT fall
// through to auto-resolution and quietly pick a different tree, and the bad
// path is never returned — resolution reports "no repo," which surfaces the
// global-fallback warning downstream.
func repoRootOverride(requireStore bool) (root string, set bool) {
	v, ok := RepoRootOverride()
	if !ok {
		return "", false
	}
	if info, err := os.Stat(v); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "ethos: ETHOS_REPO_ROOT=%q is not an existing directory; refusing to use it\n", v)
		return "", true
	}
	if requireStore {
		store := filepath.Join(v, ".punt-labs", "ethos")
		if info, err := os.Stat(store); err != nil || !info.IsDir() {
			fmt.Fprintf(os.Stderr, "ethos: ETHOS_REPO_ROOT=%q has no %s store; refusing to use it\n",
				v, filepath.Join(".punt-labs", "ethos"))
			return "", true
		}
	}
	return v, true
}

// FindRepoRoot returns the current work tree root — the directory holding
// the .git marker for the cwd's checkout — or empty string when the cwd is
// not inside a repo. In a linked worktree it returns the WORKTREE root, not
// the main one: per-checkout operations (enable/disable markers, generated
// .claude/agents, audit chunks that must travel in this worktree's commit)
// belong to the checkout, not the shared store. Store readers and writers
// use StoreRepoRoot instead.
//
// ETHOS_REPO_ROOT (whitespace-trimmed) overrides the walk so an operator
// can force the root when auto-resolution is wrong; it must name an existing
// directory (see repoRootOverride).
func FindRepoRoot() string {
	if root, set := repoRootOverride(false); set {
		return root
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// StoreRepoRoot returns the root of the repository whose .punt-labs/ethos
// store the caller should read and write, or empty string when the cwd is
// not inside a repo.
//
// It differs from FindRepoRoot only inside a linked worktree. Git keeps one
// shared object store per repo; a linked worktree's .git is a file pointing
// at the main work tree's .git, and — in the org-standard submodule layout —
// the worktree's own .punt-labs/ethos is an empty, unpopulated checkout.
// Resolving through the common dir means an agent working in
// <repo>/.claude/worktrees/x reads and writes the store at
// <repo>/.punt-labs/ethos rather than a different, empty tree that silently
// degrades to the global store (ethos-yofr).
//
// Resolution order:
//  1. ETHOS_REPO_ROOT env override (whitespace-trimmed) — validated to hold
//     a .punt-labs/ethos store, else refused loudly (see repoRootOverride).
//  2. Walk upward for a .git marker. A .git directory is the main work
//     tree — the store lives here. A .git file is a linked worktree (or a
//     submodule); resolve the owning repo through the common dir.
func StoreRepoRoot() string {
	if root, set := repoRootOverride(true); set {
		return root
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		marker := filepath.Join(dir, ".git")
		info, err := os.Stat(marker)
		if err == nil {
			if info.IsDir() {
				return dir // main work tree — the store lives here
			}
			if root := worktreeStoreRoot(dir); root != "" {
				return root
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// worktreeStoreRoot resolves the repository root that owns the store for a
// directory whose .git is a file. A linked worktree's store lives in the
// MAIN work tree, reached through the shared (common) git dir; a submodule's
// store lives in its own work tree. It asks git first — `git rev-parse`
// honors the real repository layout — then falls back to reading the .git
// gitdir and its commondir by hand when git is absent, so a worktree still
// resolves in a git-less environment. This mirrors the common-dir idiom in
// internal/doctor and internal/githook.
func worktreeStoreRoot(worktree string) string {
	if root := gitStoreRoot(worktree); root != "" {
		return root
	}
	return manualStoreRoot(worktree)
}

// gitStoreRoot returns git's own resolution of the owning repo root, or
// empty when git cannot answer. A linked worktree has a per-worktree git
// dir (<main>/.git/worktrees/<name>) distinct from the shared common dir
// (<main>/.git); the main root is the common dir's parent. When the two
// match (a submodule or a plain checkout), the store lives in this work
// tree, so the worktree dir is returned as-is to preserve the caller's
// symlink-form path.
func gitStoreRoot(worktree string) string {
	gitDir := gitRevParseAbs(worktree, "--git-dir")
	common := gitRevParseAbs(worktree, "--git-common-dir")
	if gitDir == "" || common == "" {
		return ""
	}
	common = filepath.Clean(common)
	if filepath.Clean(gitDir) == common {
		return worktree // submodule or plain checkout
	}
	if filepath.Base(common) != ".git" {
		// A non-standard common dir — e.g. a submodule's .git/modules/<name>,
		// whose parent is inside .git, not a work tree (SFH F4). Keep the
		// worktree rather than resolving a bogus root inside .git.
		return worktree
	}
	return filepath.Dir(common)
}

// gitRevParseAbs runs `git -C dir rev-parse --path-format=absolute <arg>`
// and returns the trimmed output, or empty on any error. The absolute
// path format (git 2.31+) makes the result independent of cwd; an older
// git that rejects the flag falls through to the manual resolver.
func gitRevParseAbs(dir, arg string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", arg).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// manualStoreRoot resolves the owning repo root without git by reading the
// .git gitdir pointer and its commondir file. For a linked worktree the
// commondir resolves to <main>/.git, whose parent is the main work tree.
// A submodule (commondir absent) keeps its store in this work tree, so the
// worktree dir is returned.
//
// It distinguishes a clean submodule signal (commondir simply absent) from a
// genuine read error — a stale worktree whose main repo moved or was
// deleted, a corrupt gitdir, or a permission failure. The clean case is
// silent; a genuine error warns to stderr naming the worktree fallback,
// rather than silently returning an empty store (SFH F2).
func manualStoreRoot(worktree string) string {
	dotgit := filepath.Join(worktree, ".git")
	data, err := os.ReadFile(dotgit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: warning: cannot read %s (%v); using the worktree store at %s\n", dotgit, err, worktree)
		return worktree
	}
	gd := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
	if gd == "" {
		fmt.Fprintf(os.Stderr, "ethos: warning: %s has no gitdir pointer; using the worktree store at %s\n", dotgit, worktree)
		return worktree
	}
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(worktree, gd)
	}
	if _, err := os.Stat(gd); err != nil {
		// The gitdir target is gone — a stale worktree (its main repo moved
		// or was deleted), not a clean submodule. Do not silently treat it
		// as one.
		fmt.Fprintf(os.Stderr, "ethos: warning: worktree git dir %s is unreadable (%v); the main repo may have moved — using the worktree store at %s\n", gd, err, worktree)
		return worktree
	}
	data, err = os.ReadFile(filepath.Join(gd, "commondir"))
	if err != nil {
		if !os.IsNotExist(err) {
			// A present-but-unreadable commondir is a real error, not the
			// clean submodule signal (which is commondir simply absent).
			fmt.Fprintf(os.Stderr, "ethos: warning: cannot read %s (%v); using the worktree store at %s\n", filepath.Join(gd, "commondir"), err, worktree)
		}
		return worktree // absent commondir: a submodule keeps its own store
	}
	common := strings.TrimSpace(string(data))
	if common == "" {
		return worktree
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gd, common)
	}
	common = filepath.Clean(common)
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return worktree
}

// EnvRepoRoot is the canonical name several DES-054 hook and CLI call
// sites use to address the repo tree for per-checkout state (audit chunks,
// missions.jsonl trace). It resolves the current work tree, honoring
// ETHOS_REPO_ROOT first — kept distinct from StoreRepoRoot because audit
// and trace files must travel in the committing checkout, not the shared
// store. Centralizing the env+walk pair keeps audit-write and
// precondition-read resolving identically (Bugbot HIGH/MED across PR #328:
// a previous split let them disagree on which repo is "this one").
func EnvRepoRoot() string {
	return FindRepoRoot()
}

// RepoName returns the repository name (e.g. "punt-labs/ethos") for the
// current working directory. Parses the "origin" remote URL.
// Returns empty string if not in a git repo or no origin remote is set.
func RepoName() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			fmt.Fprintf(os.Stderr, "ethos: git remote get-url: %v\n", err)
		}
		return ""
	}
	url := strings.TrimSpace(string(out))
	name := parseRepoName(url)
	if name == "" {
		fmt.Fprintf(os.Stderr, "ethos: could not parse repo name from remote URL %q\n", url)
	}
	return name
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

// GitConfig reads a single git config value. Returns empty string if
// git is not installed or the key is not set.
func GitConfig(key string) string {
	out, err := exec.Command("git", "config", key).Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	// Strip surrounding quotes — some git configs store values with
	// embedded quotes (e.g., user.name = "\"jmf-pobox\"").
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	return v
}

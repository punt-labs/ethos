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
	"sync"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"

	"gopkg.in/yaml.v3"
)

var (
	warnedMu   sync.Mutex
	warnedSeen = map[string]bool{}
)

// WarnOnce writes msg to stderr at most once per process, keyed on the
// message text. Store-root resolution runs several times per command
// (identity, bundle, mission, archetype), so a bad override, a genuine
// resolution error, or a global-store fallback would otherwise repeat the
// same line 2-4x. Loud means once, not spammy. Distinct roots produce
// distinct messages and so each still warns. Shared with cmd/ethos so the
// override refusal, the worktree-resolution warnings, and the mission
// global-fallback warning all dedupe against one set.
func WarnOnce(msg string) {
	warnedMu.Lock()
	defer warnedMu.Unlock()
	if warnedSeen[msg] {
		return
	}
	warnedSeen[msg] = true
	fmt.Fprintln(os.Stderr, msg)
}

// RepoConfig holds the repo-local ethos configuration.
//
// MaxDelegationDepth bounds the parent_delegation chain length the
// PreToolUse-on-Agent hook will admit (DES-054 v5). A spawn whose
// depth would exceed this limit is refused with verdict=aborted on
// the just-written skeleton. Zero means "use the package default"
// (mission.MaxDelegationDepthDefault) so a repo with no override
// gets a safe value rather than an unbounded chain.
type RepoConfig struct {
	Agent              string `yaml:"agent,omitempty"`                // default agent identity handle
	Team               string `yaml:"team,omitempty"`                 // team that owns this repo
	ActiveBundle       string `yaml:"active_bundle,omitempty"`        // currently active team bundle name
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
		sp := resolveFromSession(ss)
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

	return "", fmt.Errorf("no identity matches git user %q, email %q, or OS user %q", gitName, gitEmail, osUser)
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

// SessionID resolves the active session ID using the harness-neutral chain:
// ETHOS_SESSION, then the Claude process-tree current-pointer. It returns
// ("", "") when neither yields one, otherwise the ID and its source
// (SessionSourceEnv or "walk"). The source lets a caller apply the
// verification an explicit env anchor warrants without re-reading the
// environment. Callers that accept an explicit session (a --session flag or
// an MCP session_id arg) check that first and bypass this (DES-061).
func SessionID(ss *session.Store) (id, source string) {
	if sid := os.Getenv("ETHOS_SESSION"); sid != "" {
		return sid, SessionSourceEnv
	}
	sid, err := ss.ReadCurrentSession(process.FindClaudePID())
	if err != nil {
		return "", ""
	}
	return sid, "walk"
}

// resolveFromSession resolves the session via the harness-neutral chain
// (ETHOS_SESSION, then the Claude PID walk), then returns the caller's
// persona from the roster. The caller's participant is keyed on
// ETHOS_AGENT_ID when set — matching how iam records it on both the CLI
// and MCP surfaces — else on the Claude PID. Returns found=false if no
// session or no matching participant. Returns found=true with empty handle
// if the participant exists but has no persona configured — callers must
// not fall through to git/OS.
func resolveFromSession(ss *session.Store) sessionPersona {
	sessionID, source := SessionID(ss)
	if sessionID == "" {
		return sessionPersona{}
	}
	roster, err := ss.Load(sessionID)
	if err != nil {
		// A roster named explicitly by ETHOS_SESSION that exists but fails
		// to parse is a real error — warn rather than silently answering
		// with the git/OS identity. A not-found session is the soft
		// no-session contract and stays silent.
		if source == SessionSourceEnv && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "ethos: warning: ETHOS_SESSION %q names an unreadable roster: %v\n", sessionID, err)
		}
		return sessionPersona{}
	}
	agentID := os.Getenv("ETHOS_AGENT_ID")
	if agentID == "" {
		agentID = process.FindClaudePID()
	}
	p := roster.FindParticipant(agentID)
	if p == nil {
		return sessionPersona{}
	}
	// Participant found. If persona is empty, that's an explicit
	// "no persona configured" — not "try git/OS instead."
	if p.Persona == "" {
		return sessionPersona{found: true}
	}
	return sessionPersona{handle: p.Persona, found: true}
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
	v := strings.TrimSpace(os.Getenv("ETHOS_REPO_ROOT"))
	if v == "" {
		return "", false
	}
	if info, err := os.Stat(v); err != nil || !info.IsDir() {
		WarnOnce(fmt.Sprintf("ethos: ETHOS_REPO_ROOT=%q is not an existing directory; refusing to use it", v))
		return "", true
	}
	if requireStore {
		store := filepath.Join(v, ".punt-labs", "ethos")
		if info, err := os.Stat(store); err != nil || !info.IsDir() {
			WarnOnce(fmt.Sprintf("ethos: ETHOS_REPO_ROOT=%q has no %s store; refusing to use it",
				v, filepath.Join(".punt-labs", "ethos")))
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
		WarnOnce(fmt.Sprintf("ethos: warning: cannot read %s (%v); using the worktree store at %s", dotgit, err, worktree))
		return worktree
	}
	gd := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
	if gd == "" {
		WarnOnce(fmt.Sprintf("ethos: warning: %s has no gitdir pointer; using the worktree store at %s", dotgit, worktree))
		return worktree
	}
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(worktree, gd)
	}
	if _, err := os.Stat(gd); err != nil {
		// The gitdir target is gone — a stale worktree (its main repo moved
		// or was deleted), not a clean submodule. Do not silently treat it
		// as one.
		WarnOnce(fmt.Sprintf("ethos: warning: worktree git dir %s is unreadable (%v); the main repo may have moved — using the worktree store at %s", gd, err, worktree))
		return worktree
	}
	data, err = os.ReadFile(filepath.Join(gd, "commondir"))
	if err != nil {
		if !os.IsNotExist(err) {
			// A present-but-unreadable commondir is a real error, not the
			// clean submodule signal (which is commondir simply absent).
			WarnOnce(fmt.Sprintf("ethos: warning: cannot read %s (%v); using the worktree store at %s", filepath.Join(gd, "commondir"), err, worktree))
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

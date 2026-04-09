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

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"

	"gopkg.in/yaml.v3"
)

// RepoConfig holds the repo-local ethos configuration.
type RepoConfig struct {
	Agent string `yaml:"agent,omitempty"` // default agent identity handle
	Team  string `yaml:"team,omitempty"`  // team that owns this repo
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

// resolveFromSession uses FindClaudePID to locate the session via the
// PID-keyed current file, then returns the caller's persona from the
// roster. Returns found=false if no session or no matching participant.
// Returns found=true with empty handle if the participant exists but
// has no persona configured — callers must not fall through to git/OS.
func resolveFromSession(ss *session.Store) sessionPersona {
	pid := process.FindClaudePID()
	sessionID, err := ss.ReadCurrentSession(pid)
	if err != nil {
		return sessionPersona{}
	}
	roster, err := ss.Load(sessionID)
	if err != nil {
		return sessionPersona{}
	}
	p := roster.FindParticipant(pid)
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

// FindRepoEthosRoot returns the path to .punt-labs/ethos/ in the current
// git repo, or empty string if not in a repo or the directory doesn't exist.
func FindRepoEthosRoot() string {
	repoRoot := FindRepoRoot()
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

// FindRepoRoot walks from the current working directory upward looking
// for a .git directory. Returns empty string if no .git is found.
func FindRepoRoot() string {
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

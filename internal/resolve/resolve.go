// Package resolve implements identity resolution chains for humans
// and agents. Humans are resolved from iam declarations, git config,
// or OS user. Agents are resolved from per-repo config.
package resolve

import (
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
func Resolve(store *identity.Store, ss *session.Store) (string, error) {
	// Step 1: check for iam declaration via process tree.
	if ss != nil {
		sp := resolveFromSession(store, ss)
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
func resolveFromSession(store *identity.Store, ss *session.Store) sessionPersona {
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
	// Verify the persona exists in the store. If it doesn't,
	// return the handle anyway — the caller gets the configured
	// persona even if the identity file is missing. Load() will
	// produce the actual error with the handle name.
	return sessionPersona{handle: p.Persona, found: true}
}

// ResolveAgent returns the default agent identity handle for the repo.
// Reads .punt-labs/ethos/config.yaml "agent:" field from the given repo
// root. Returns empty string if not configured or not in a repo.
func ResolveAgent(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, ".punt-labs", "ethos", "config.yaml"))
	if err != nil {
		return ""
	}
	var cfg RepoConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.Agent
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

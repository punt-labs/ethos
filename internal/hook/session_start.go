package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/punt-labs/ethos/internal/team"
)

// SessionStartResult is the JSON output of the session-start hook.
type SessionStartResult struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

// SessionStartDeps holds the stores needed by the SessionStart hook.
type SessionStartDeps struct {
	Store    identity.IdentityStore
	Sessions *session.Store
	Teams    *team.LayeredStore
	Roles    *role.LayeredStore
}

// HandleSessionStart reads the SessionStart hook payload from stdin,
// resolves identity, creates a session roster, and emits context.
func HandleSessionStart(r io.Reader, deps SessionStartDeps) error {
	if deps.Store == nil || deps.Sessions == nil {
		return fmt.Errorf("session-start: Store and Sessions stores are required")
	}

	store := deps.Store
	ss := deps.Sessions
	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("session-start: %w", err)
	}

	sessionID, _ := input["session_id"].(string)

	// Resolve human identity with full attribute content.
	handle, err := resolve.Resolve(store, ss)
	var resolvedID *identity.Identity
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: identity resolution failed: %v (using OS username)\n", err)
	} else {
		id, loadErr := store.Load(handle)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: failed to load identity %q: %v\n", handle, loadErr)
		} else {
			for _, w := range id.Warnings {
				fmt.Fprintf(os.Stderr, "ethos: session-start: identity %q: %s\n", handle, w)
			}
			resolvedID = id
		}
	}

	// Clean stale PID files from previous sessions (fire-and-forget).
	if purged, purgeErr := ss.PurgeCurrent(); purgeErr != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: failed to purge stale PID files: %v\n", purgeErr)
	} else if len(purged) > 0 {
		fmt.Fprintf(os.Stderr, "ethos: session-start: cleaned %d stale PID file(s)\n", len(purged))
	}

	// Resolve agent persona from repo config. A non-nil error means
	// the config file exists but cannot be read or parsed — a loud
	// failure mode the user needs to see. Propagate fail-closed, same
	// pattern as the GenerateAgentFiles wrap below (ethos-9ai.6 C1).
	// The shell wrapper's `|| true` keeps Claude Code session startup
	// fail-open (cli.md §Hook Architecture); the non-zero exit code is
	// the signal for direct CLI invocation and `ethos doctor`.
	repoRoot := resolve.FindRepoRoot()
	agentPersona, err := resolve.ResolveAgent(repoRoot)
	if err != nil {
		return fmt.Errorf("resolving agent: %w", err)
	}

	// Create session roster if we have a session ID.
	if sessionID != "" {
		userID := os.Getenv("USER")
		if userID == "" {
			userID = "unknown"
		}
		userPersona := ""
		if resolvedID != nil {
			userPersona = resolvedID.Handle
		}
		if userPersona == "" {
			userPersona = userID
		}
		claudePID := process.FindClaudePID()

		root := session.Participant{AgentID: userID, Persona: userPersona}
		primary := session.Participant{AgentID: claudePID, Persona: agentPersona, Parent: userID}

		repo := resolveRepo()
		host := resolveHost()

		if createErr := ss.Create(sessionID, root, primary, repo, host); createErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: failed to create session roster: %v\n", createErr)
		} else if wcErr := ss.WriteCurrentSession(claudePID, sessionID); wcErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: failed to write current session: %v\n", wcErr)
		}
	}

	// Emit the agent's persona block (not the human's).
	// The agent persona comes from repo config (.punt-labs/ethos.yaml).
	if agentPersona == "" {
		// No agent persona configured — fall back to human identity one-liner.
		if resolvedID != nil {
			msg := fmt.Sprintf("Ethos session started. Active identity: %s (%s).", resolvedID.Name, resolvedID.Handle)
			result := SessionStartResult{}
			result.HookSpecificOutput.HookEventName = "SessionStart"
			result.HookSpecificOutput.AdditionalContext = msg
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		return nil
	}

	agentID, agentLoadErr := store.Load(agentPersona)
	if agentLoadErr != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: failed to load agent identity %q: %v\n", agentPersona, agentLoadErr)
		// Fall back to human identity one-liner.
		if resolvedID != nil {
			msg := fmt.Sprintf("Ethos session started. Active identity: %s (%s).", resolvedID.Name, resolvedID.Handle)
			result := SessionStartResult{}
			result.HookSpecificOutput.HookEventName = "SessionStart"
			result.HookSpecificOutput.AdditionalContext = msg
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		return nil
	}
	for _, w := range agentID.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: session-start: agent identity %q: %s\n", agentPersona, w)
	}

	// Install agent definitions from ethos agents dir into .claude/agents/.
	ethosRoot := resolve.FindRepoEthosRoot()
	if ethosRoot != "" {
		deployed, installErr := InstallAgentDefinitions(ethosRoot)
		if installErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: session-start: agent install failed: %v\n", installErr)
		}
		for _, name := range deployed {
			fmt.Fprintf(os.Stderr, "ethos: session-start: deployed agent definition %s\n", name)
		}
	}

	// Generate .claude/agents/<handle>.md from ethos identity data.
	// Propagates: the returned error is the single authoritative
	// signal the CLI can gate on. The shell wrapper's `|| true` keeps
	// Claude Code session startup fail-open (cli.md §Hook Architecture)
	// so a broken config does not brick sessions, but `ethos hook
	// session-start` invoked directly exits non-zero — useful for
	// `ethos doctor` and manual debugging.
	if repoRoot != "" && deps.Teams != nil && deps.Roles != nil {
		if genErr := GenerateAgentFiles(repoRoot, store, deps.Teams, deps.Roles); genErr != nil {
			return fmt.Errorf("generating agents: %w", genErr)
		}
	}

	// Build sections: persona, extension context, team — same as PreCompact.
	var sections []string
	if persona := BuildPersonaBlock(agentID); persona != "" {
		sections = append(sections, persona)
	} else {
		sections = append(sections, fmt.Sprintf("Ethos session started. Active identity: %s (%s).", agentID.Name, agentID.Handle))
	}
	if extCtx := BuildExtensionContext(agentID.Ext); extCtx != "" {
		sections = append(sections, extCtx)
	}
	if teamCtx := BuildTeamSection(deps.Teams, deps.Roles, store, agentPersona); teamCtx != "" {
		sections = append(sections, teamCtx)
	}

	result := SessionStartResult{}
	result.HookSpecificOutput.HookEventName = "SessionStart"
	result.HookSpecificOutput.AdditionalContext = strings.Join(sections, "\n\n")
	return json.NewEncoder(os.Stdout).Encode(result)
}

// resolveRepo extracts org/name from the git remote of the working directory.
func resolveRepo() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: could not resolve repo from git remote: %v\n", err)
		return ""
	}
	return ParseGitRemote(string(out))
}

// resolveHost returns the short hostname (no domain).
func resolveHost() string {
	name, err := os.Hostname()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: could not resolve hostname: %v\n", err)
		return ""
	}
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	return name
}

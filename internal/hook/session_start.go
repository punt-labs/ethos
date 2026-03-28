package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/session"
)

// SessionStartResult is the JSON output of the session-start hook.
type SessionStartResult struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

// HandleSessionStart reads the SessionStart hook payload from stdin,
// resolves identity, creates a session roster, and emits context.
func HandleSessionStart(r io.Reader, store *identity.Store, ss *session.Store) error {
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

	// Resolve agent persona from repo config.
	repoRoot := resolve.FindRepoRoot()
	agentPersona := resolve.ResolveAgent(repoRoot)

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

		if createErr := ss.Create(sessionID, root, primary); createErr != nil {
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

	// Try full persona block; fall back to one-line if no content.
	msg := BuildPersonaBlock(agentID)
	if msg == "" {
		msg = fmt.Sprintf("Ethos session started. Active identity: %s (%s).", agentID.Name, agentID.Handle)
	}
	if mem := BuildMemorySection(agentID.Ext, agentID.Handle); mem != "" {
		msg += "\n\n" + mem
	}

	result := SessionStartResult{}
	result.HookSpecificOutput.HookEventName = "SessionStart"
	result.HookSpecificOutput.AdditionalContext = msg
	return json.NewEncoder(os.Stdout).Encode(result)
}

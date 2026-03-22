package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
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
	input, _ := ReadInput(r, time.Second)

	sessionID, _ := input["session_id"].(string)

	// Resolve human identity.
	handle, err := resolve.Resolve(store, ss)
	humanName := ""
	humanHandle := ""
	if err == nil {
		id, loadErr := store.Load(handle, identity.Reference(true))
		if loadErr == nil {
			humanName = id.Name
			humanHandle = id.Handle
		}
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
		userPersona := humanHandle
		if userPersona == "" {
			userPersona = userID
		}
		claudePID := fmt.Sprintf("%d", os.Getppid())

		root := session.Participant{AgentID: userID, Persona: userPersona}
		primary := session.Participant{AgentID: claudePID, Persona: agentPersona, Parent: userID}

		if createErr := ss.Create(sessionID, root, primary); createErr == nil {
			_ = ss.WriteCurrentSession(claudePID, sessionID)
		}
	}

	// Emit context if we resolved an identity.
	if humanName != "" {
		msg := fmt.Sprintf("Ethos session started. Active identity: %s (%s).", humanName, humanHandle)
		result := SessionStartResult{}
		result.HookSpecificOutput.HookEventName = "SessionStart"
		result.HookSpecificOutput.AdditionalContext = msg
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	return nil
}

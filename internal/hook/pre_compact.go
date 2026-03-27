package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/session"
)

// HandlePreCompact reads the PreCompact hook payload from stdin,
// finds the current session's primary agent participant, and emits
// a condensed persona block as systemMessage so behavioral
// instructions survive context compaction.
func HandlePreCompact(r io.Reader, store *identity.Store, ss *session.Store) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("pre-compact: %w", err)
	}

	sessionID, _ := input["session_id"].(string)
	if sessionID == "" {
		return nil
	}

	roster, err := ss.Load(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: pre-compact: failed to load session %q: %v\n", sessionID, err)
		return nil // sidecar: don't block compaction
	}

	// Find the primary agent: the participant whose Parent is the
	// human (Participants[0]). This is stable regardless of how many
	// subagents have joined the roster.
	var agentPersona string
	if len(roster.Participants) > 0 {
		humanID := roster.Participants[0].AgentID
		for _, p := range roster.Participants[1:] {
			if p.Parent == humanID && p.Persona != "" {
				agentPersona = p.Persona
				break
			}
		}
	}
	if agentPersona == "" {
		return nil
	}

	// Load identity with full attribute content.
	id, err := store.Load(agentPersona)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: pre-compact: failed to load identity %q: %v\n", agentPersona, err)
		return nil // sidecar: don't block compaction
	}
	for _, w := range id.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: pre-compact: identity %q: %s\n", agentPersona, w)
	}

	msg := BuildCondensedPersona(id)
	if msg == "" {
		return nil
	}

	result := struct {
		SystemMessage string `json:"systemMessage"`
	}{SystemMessage: msg}
	return json.NewEncoder(os.Stdout).Encode(result)
}

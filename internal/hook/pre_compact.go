package hook

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/punt-labs/ethos/internal/team"
)

// PreCompactDeps holds the stores needed by the PreCompact hook.
type PreCompactDeps struct {
	Identities identity.IdentityStore
	Sessions   *session.Store
	Teams      *team.LayeredStore
	Roles      *role.LayeredStore
}

// HandlePreCompact reads the PreCompact hook payload from stdin,
// finds the current session's primary agent participant, and prints
// the full persona block plus team context as plain text so
// behavioral instructions survive context compaction.
func HandlePreCompact(r io.Reader, deps PreCompactDeps) error {
	if deps.Identities == nil || deps.Sessions == nil {
		return fmt.Errorf("pre-compact: Identities and Sessions stores are required")
	}

	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("pre-compact: %w", err)
	}

	sessionID, ok := input["session_id"].(string)
	if !ok || sessionID == "" {
		fmt.Fprintf(os.Stderr, "ethos: pre-compact: no session_id in payload, skipping context injection\n")
		return nil
	}

	roster, err := deps.Sessions.Load(sessionID)
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
		fmt.Fprintf(os.Stderr, "ethos: pre-compact: no agent persona found in session %q roster\n", sessionID)
		return nil
	}

	// Load identity with full attribute content.
	id, err := deps.Identities.Load(agentPersona)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: pre-compact: failed to load identity %q: %v\n", agentPersona, err)
		return nil // sidecar: don't block compaction
	}
	for _, w := range id.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: pre-compact: identity %q: %s\n", agentPersona, w)
	}

	// Build persona block (full content, not condensed).
	var sections []string
	if persona := BuildPersonaBlock(id); persona != "" {
		sections = append(sections, persona)
	}
	if extCtx := BuildExtensionContext(id.Ext); extCtx != "" {
		sections = append(sections, extCtx)
	}

	// Build team context from repo config.
	if teamCtx := buildTeamSection(deps, agentPersona); teamCtx != "" {
		sections = append(sections, teamCtx)
	}

	msg := strings.Join(sections, "\n\n")
	if msg == "" {
		return nil
	}

	_, err = fmt.Fprint(os.Stdout, msg)
	return err
}

// buildTeamSection resolves the team from repo config and builds
// the team context block. Returns empty string on any error.
func buildTeamSection(deps PreCompactDeps, selfHandle string) string {
	return BuildTeamSection(deps.Teams, deps.Roles, deps.Identities, selfHandle)
}

// BuildTeamSection resolves the team from repo config and builds the team
// context block. Returns empty string if teams is nil, no team is configured,
// or on any load error.
func BuildTeamSection(teams *team.LayeredStore, roles *role.LayeredStore, identities identity.IdentityStore, selfHandle string) string {
	if teams == nil {
		return ""
	}

	repoRoot := resolve.FindRepoRoot()
	teamName, err := resolve.ResolveTeam(repoRoot)
	if err != nil {
		// Fail-open per this function's documented contract
		// ("Returns empty string ... on any load error"). The log
		// preserves the visibility the old ResolveTeam's Fprintf gave
		// us before its signature change propagated the error here.
		fmt.Fprintf(os.Stderr, "ethos: pre-compact: %v\n", err)
		return ""
	}
	if teamName == "" {
		return ""
	}

	t, err := teams.Load(teamName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: team context: failed to load team %q: %v\n", teamName, err)
		return ""
	}

	return BuildTeamContext(t, roles, identities, selfHandle)
}

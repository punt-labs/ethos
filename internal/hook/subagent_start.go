package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
)

// SubagentStartResult is the JSON output of the subagent-start hook.
type SubagentStartResult struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

// SubagentStartDeps groups the dependencies HandleSubagentStartWithDeps
// needs. The legacy HandleSubagentStart entry point builds an empty
// deps struct and delegates here so existing callers and tests keep
// compiling without an extra plumbing pass.
//
// Identities and Sessions are required. Missions and HashSources are
// optional: if either is nil the verifier hash check is skipped (the
// installation has no mission store, the hook still emits the persona
// block as before).
type SubagentStartDeps struct {
	Identities identity.IdentityStore
	Sessions   *session.Store
	Missions   *mission.Store
	// Hash is the source bundle ComputeEvaluatorHash uses to recompute
	// the live evaluator content. Required when Missions is non-nil
	// and Phase 3.3 verifier discipline is in effect.
	Hash mission.HashSources
}

// HandleSubagentStart reads the SubagentStart hook payload from stdin,
// joins the subagent to the session roster, and emits persona context
// if the subagent matches an ethos identity. This is the legacy entry
// point — it skips the Phase 3.3 verifier hash check.
//
// New code should call HandleSubagentStartWithDeps so the verifier
// hash gate runs. The legacy signature is preserved so existing
// callers and tests in the hook package continue to compile.
func HandleSubagentStart(r io.Reader, store identity.IdentityStore, ss *session.Store) error {
	return HandleSubagentStartWithDeps(r, SubagentStartDeps{
		Identities: store,
		Sessions:   ss,
	})
}

// HandleSubagentStartWithDeps is the full subagent-start handler. In
// addition to the persona block emission HandleSubagentStart provides,
// it enforces DES-033's frozen-evaluator gate: when the spawning
// subagent matches the evaluator handle of any open mission, the
// hook recomputes the evaluator's current hash and refuses the spawn
// if any open mission's pinned hash disagrees.
//
// The mismatch error names the offending mission, the pinned and
// current hash prefixes, and the relaunch instruction the operator
// needs to recover. Hash success is silent — operators only see
// the hash when something is wrong.
func HandleSubagentStartWithDeps(r io.Reader, deps SubagentStartDeps) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("subagent-start: %w", err)
	}

	agentID, _ := input["agent_id"].(string)
	agentType, _ := input["agent_type"].(string)
	sessionID, _ := input["session_id"].(string)

	if agentID == "" || sessionID == "" {
		return nil
	}

	// Phase 3.3: verifier hash gate. Run BEFORE joining the session
	// roster — a refused spawn must leave no trace in the roster, and
	// the operator's diagnostic must be the hash mismatch, not a
	// confusing post-join failure. The check is a no-op when the
	// installation has no mission store wired in (legacy hook flow).
	if err := checkVerifierHash(agentType, deps); err != nil {
		// Return a non-nil error so cmd/ethos/hook.go's runner exits
		// non-zero, which Claude Code surfaces to the operator as a
		// fatal subagent launch failure. The error string carries
		// the diagnostic; the runner prints it verbatim.
		return err
	}

	// Resolve persona: if an identity exists with the same handle as
	// agent_type, use it as the persona.
	persona := ""
	if agentType != "" && deps.Identities.Exists(agentType) {
		persona = agentType
	}

	p := session.Participant{
		AgentID:   agentID,
		Persona:   persona,
		Parent:    process.FindClaudePID(),
		AgentType: agentType,
	}

	if joinErr := deps.Sessions.Join(sessionID, p); joinErr != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to join session %s: %v\n", sessionID, joinErr)
	}

	// If no persona matched, nothing more to do.
	if persona == "" {
		return nil
	}

	// Load identity with full attribute content for persona injection.
	id, err := deps.Identities.Load(persona)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: identity %q exists but attribute resolution failed: %v\n", persona, err)
		return nil
	}
	for _, w := range id.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: identity %q: %s\n", persona, w)
	}

	var sections []string

	block := BuildPersonaBlock(id)
	if block != "" {
		// Prepend parent context if we can resolve the parent from the roster.
		parentLine := resolveParentLine(deps.Sessions, sessionID, p.Parent, deps.Identities)
		if parentLine != "" {
			block = insertAfterFirstLine(block, parentLine)
		}
		sections = append(sections, block)
	}

	if extCtx := BuildExtensionContext(id.Ext); extCtx != "" {
		sections = append(sections, extCtx)
	}

	if len(sections) == 0 {
		return nil
	}

	result := SubagentStartResult{}
	result.HookSpecificOutput.HookEventName = "SubagentStart"
	result.HookSpecificOutput.AdditionalContext = strings.Join(sections, "\n\n")
	return json.NewEncoder(os.Stdout).Encode(result)
}

// checkVerifierHash recomputes the evaluator hash for every open
// mission whose Evaluator.Handle matches agentType, and returns a
// fatal error if any pinned hash disagrees with the current content.
//
// The check is intentionally conservative: a single drifted mission
// blocks the spawn even if other missions still match. This protects
// the per-mission verdict integrity DES-033 was written to enforce —
// silently allowing the spawn against a stale pinned hash would
// invalidate the original mission's launch contract.
//
// Returns nil and is a no-op when:
//   - Missions or Hash sources are unconfigured (legacy install)
//   - agentType is empty
//   - No open mission names agentType as evaluator
//   - All matching open missions have current content matching their
//     pinned hash
//
// On a real mismatch the error names the mission ID, evaluator handle,
// pinned hash prefix, current hash prefix, and the relaunch
// instruction. Hash prefixes are the leading 12 hex characters — long
// enough for visual disambiguation, short enough to fit on a single
// terminal line.
func checkVerifierHash(agentType string, deps SubagentStartDeps) error {
	if deps.Missions == nil {
		return nil // legacy install: no mission store
	}
	if err := deps.Hash.Validate(); err != nil {
		// Misconfiguration: a mission store is present but the hash
		// sources are not. Refusing spawns on misconfiguration is
		// the safe default — silently skipping the gate would let
		// stale evaluator content through.
		return fmt.Errorf("verifier hash gate misconfigured: %w", err)
	}
	if strings.TrimSpace(agentType) == "" {
		return nil
	}

	ids, err := deps.Missions.List()
	if err != nil {
		return fmt.Errorf("verifier hash gate: listing missions: %w", err)
	}

	var currentHash string
	for _, id := range ids {
		c, loadErr := deps.Missions.Load(id)
		if loadErr != nil {
			// A corrupt or unparseable mission file blocks the gate.
			// Silently skipping it would let an attacker bypass the
			// frozen evaluator by hand-corrupting the contract.
			return fmt.Errorf(
				"verifier hash gate: failed to load mission %q: %w",
				id, loadErr,
			)
		}
		if c.Status != mission.StatusOpen {
			continue
		}
		if c.Evaluator.Handle != agentType {
			continue
		}
		// Compute the current hash once — every matching open
		// mission compares against the same byte string.
		if currentHash == "" {
			currentHash, err = mission.ComputeEvaluatorHash(c.Evaluator.Handle, deps.Hash)
			if err != nil {
				return fmt.Errorf(
					"verifier hash gate: recomputing hash for evaluator %q: %w",
					c.Evaluator.Handle, err,
				)
			}
		}
		if c.Evaluator.Hash == "" {
			// Pre-3.3 mission with an empty pinned hash. Refusing
			// here would block legacy missions; logging and
			// allowing keeps the upgrade path open. The mission's
			// launch predates the gate.
			fmt.Fprintf(os.Stderr,
				"ethos: subagent-start: warning: mission %s has empty Evaluator.Hash (pre-3.3); skipping gate\n",
				c.MissionID,
			)
			continue
		}
		if c.Evaluator.Hash != currentHash {
			return fmt.Errorf(
				"refusing verifier spawn: evaluator %q content has drifted since mission %s was launched\n"+
					"  pinned hash:  %s\n"+
					"  current hash: %s\n"+
					"  to accept the new content, close mission %s and relaunch it",
				c.Evaluator.Handle,
				c.MissionID,
				hashPrefix(c.Evaluator.Hash),
				hashPrefix(currentHash),
				c.MissionID,
			)
		}
	}
	return nil
}

// hashPrefix returns the first 12 hex characters of a hash string,
// or the full string if shorter. Used in operator-facing error
// messages so the line stays readable while still distinguishing
// hashes for follow-up debugging.
func hashPrefix(h string) string {
	const n = 12
	if len(h) <= n {
		return h
	}
	return h[:n]
}

// resolveParentLine finds the primary Claude agent in the session roster
// and returns a "You report to Name (handle)." line. The primary agent
// is identified as the participant whose AgentID matches the subagent's
// Parent field (the Claude PID). Returns "" if the parent cannot be resolved.
func resolveParentLine(ss *session.Store, sessionID, parentID string, store identity.IdentityStore) string {
	if parentID == "" {
		return ""
	}
	roster, err := ss.Load(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: resolveParentLine: session load failed: %v\n", err)
		return ""
	}
	// Find the participant whose AgentID matches the subagent's parent.
	var parentHandle string
	for _, p := range roster.Participants {
		if p.AgentID == parentID && p.Persona != "" {
			parentHandle = p.Persona
			break
		}
	}
	if parentHandle == "" {
		return ""
	}
	parentIdentity, err := store.Load(parentHandle, identity.Reference(true))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: resolveParentLine: load identity %q: %v\n", parentHandle, err)
		return ""
	}
	return fmt.Sprintf("You report to %s (%s).", parentIdentity.Name, parentIdentity.Handle)
}

// insertAfterFirstLine inserts extra after the first line of text.
func insertAfterFirstLine(text, extra string) string {
	for i, c := range text {
		if c == '\n' {
			return text[:i] + "\n" + extra + text[i:]
		}
	}
	return text + "\n" + extra
}

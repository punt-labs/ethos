package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/resolve"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// missionTool defines the consolidated `mission` MCP tool. The single
// tool dispatches on the `method` enum so callers see one entry point in
// their MCP server's tool list, mirroring how the team and identity
// tools are exposed.
func (h *Handler) missionTool() mcplib.Tool {
	return mcplib.NewTool("mission",
		mcplib.WithDescription("Manage mission contracts (typed delegation artifacts). Methods: create, show, list, close, abandon, reflect, reflections, advance, result, results, log, correct. Create resolves the evaluator handle and pins a content hash; verifier spawns are refused if the content has drifted. Reflect submits a structured reflection for the current round, advance bumps to the next round, and reflections fetches the round-by-round log. Result submits the typed worker handoff for the current round; close refuses the terminal transition until a valid result exists. Abandon retires a mission that was created but never had a worker actually spawned — it refuses if any delegation record or result artifact exists, at any round; use close (after a result is submitted) for missions with real work. Log returns the append-only event audit trail for post-mortem analysis; filters by event type and RFC3339 timestamp. Correct files an additive-only annotation against a CLOSED mission — a fact discovered afterward, an integrity finding, or a leader's post-escalation decision — as a new event on the log; it never rewrites the contract, results, or reflections and is refused on an open mission."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("create", "show", "list", "close", "abandon", "reflect", "reflections", "advance", "result", "results", "log", "correct"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("mission_id",
			mcplib.Description("Mission ID or unique prefix. Required for show, close, abandon, reflect, reflections, advance, result, results, log, and correct."),
		),
		mcplib.WithString("contract",
			mcplib.Description("Full contract YAML body. Required for create."),
		),
		mcplib.WithString("reflection",
			mcplib.Description("Full reflection YAML body. Required for reflect."),
		),
		mcplib.WithString("result",
			mcplib.Description("Full result YAML body. Required for result."),
		),
		mcplib.WithString("correction",
			mcplib.Description("Full correction YAML body (mission, round, kind, author, claim, corrected, supersedes, evidence). Required for correct. kind is one of factual, fabrication, decision; claim is required unless kind is decision. author must resolve to a real identity."),
		),
		mcplib.WithString("actor",
			mcplib.Description("Optional handle to record on the round_advanced event for advance. Defaults to the contract's leader."),
		),
		mcplib.WithString("status",
			// No enum constraint: the valid values differ per method
			// (list accepts "open|closed|failed|escalated|abandoned|
			// all", close accepts "closed|failed|escalated" only —
			// "abandoned" is deliberately not a valid close status,
			// see Store.Close). A shared enum would advertise "open"
			// and "all" as valid for close, which is wrong. Each
			// handler validates its own input.
			mcplib.Description("Filter for list (open|closed|failed|escalated|abandoned|all) or terminal status for close (closed|failed|escalated)."),
		),
		mcplib.WithString("reason",
			mcplib.Description("Why the mission is being retired without a worker ever spawning. Required for abandon."),
		),
		mcplib.WithString("event",
			mcplib.Description("Optional comma-separated list of event types for log (e.g. create,close). Unknown types are accepted and return empty."),
		),
		mcplib.WithString("since",
			mcplib.Description("Optional RFC3339 timestamp for log; events on or after the cutoff are included."),
		),
	)
}

// handleMission dispatches on the method argument to the per-method
// handlers. Unknown methods return a tool error result rather than a Go
// error so the MCP client sees a structured failure.
func (h *Handler) handleMission(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if h.missionStore == nil {
		return mcplib.NewToolResultError("mission store not configured"), nil
	}
	method := stringArg(req, "method", "")
	switch method {
	case "create":
		return h.handleCreateMission(req)
	case "show":
		return h.handleShowMission(req)
	case "list":
		return h.handleListMissions(req)
	case "close":
		return h.handleCloseMission(req)
	case "abandon":
		return h.handleAbandonMission(req)
	case "reflect":
		return h.handleReflectMission(req)
	case "reflections":
		return h.handleReflectionsMission(req)
	case "advance":
		return h.handleAdvanceMission(req)
	case "result":
		return h.handleResultMission(req)
	case "results":
		return h.handleResultsMission(req)
	case "log":
		return h.handleLogMission(req)
	case "correct":
		return h.handleCorrectMission(req)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

// handleCreateMission parses the contract YAML, fills in
// server-controlled fields, and persists. The contract argument is the
// trust boundary; we use yaml.v3's KnownFields(true) so unrecognized
// keys are an error rather than silently dropped.
func (h *Handler) handleCreateMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	body := stringArg(req, "contract", "")
	if strings.TrimSpace(body) == "" {
		return mcplib.NewToolResultError("contract YAML body is required for create"), nil
	}

	// Strict decode via the shared helper: unknown fields, multi-doc
	// YAML, and trailing content are all rejected. CLI and MCP share
	// this entry point so the input trust boundary is enforced
	// identically regardless of how the YAML reached the store.
	parsed, err := mission.DecodeContractStrict([]byte(body), "mcp create request")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	// DES-049: warn about the deprecated inputs.bead only for the
	// contract the user submitted here, not for the old missions the
	// store scans during conflict checking (ethos-c0yp).
	if bead := mission.BeadAlias([]byte(body)); bead != "" {
		mission.WarnBeadDeprecated(os.Stderr, bead)
	}
	c := *parsed

	// Apply server-controlled fields (mission_id, status, timestamps,
	// evaluator.pinned_at, evaluator.hash) via the shared helper.
	// CLI and MCP entry points are in lockstep: any caller-supplied
	// values for these fields are overwritten identically regardless
	// of where the YAML came from. Hash sources resolve the evaluator
	// against the live identity, role, and team stores; an
	// unresolvable handle is fatal — see DES-033.
	//
	// NewLiveHashSources rejects nil role or team stores so an MCP
	// handler built without WithRoleStore/WithTeamStore fails loudly
	// here instead of silently pinning a role-free hash that the
	// verifier hook (always wired with both stores) could never match.
	sources, err := mission.NewLiveHashSources(h.store, h.roles, h.teams)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if err := h.missionStore.ApplyServerFields(&c, time.Now(), sources); err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	if err := h.missionStore.Create(&c); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to create mission: %v", err)), nil
	}
	// Parity with the CLI's create/dispatch: minting a fresh mission is
	// the leader naming one explicitly, so the session's active-mission
	// sidecar must follow immediately (ethos-5jsf) -- otherwise the very
	// next Agent() spawn in this session still writes its delegation
	// under whatever mission the sidecar named a moment ago, d-040's
	// exact pattern, reachable through the MCP surface even though
	// dispatchTierB's own status re-check (facet 2) already closes the
	// post-close half of the same root cause.
	if warnings := h.bindDispatchedMission(c.MissionID); len(warnings) > 0 {
		return jsonResult(createMissionResponse{Contract: &c, Warnings: warnings})
	}
	return jsonResult(&c)
}

// createMissionResponse is the wire shape for handleCreateMission's
// rebind warnings: the created contract plus an optional warnings
// list, present only when bindDispatchedMission's rebind left
// something for the caller to see. Mirrors ShowPayload/LogPayload's
// warnings convention (internal/mission/mission.go) as a local type
// rather than a shared one -- mission.go is outside this fix's
// write-set.
type createMissionResponse struct {
	*mission.Contract
	Warnings []string `json:"warnings,omitempty"`
}

// bindDispatchedMission mirrors the CLI's bindDispatchedMission
// (cmd/ethos/mission.go:2023) for the MCP create surface -- ethos-5jsf.
// Creating a mission is the leader naming one explicitly, so it is the
// moment the session's active-mission sidecar must follow: without
// this, the next Agent() spawn in this session still writes under
// whatever mission the sidecar named a moment ago (observed: d-078
// under m-017, d-040 under m-002).
//
// The binding is written with dispatch origin, not claim -- creating a
// mission on someone's behalf must not turn on commit trailers for
// this session; only an explicit `ethos mission claim` does that.
//
// Every step is advisory: a mission that was created stays created
// regardless of whether the rebind below succeeds. But "advisory"
// means the create call does not fail -- it does not mean the caller
// is left with no signal. No session store wired, or no session in
// context, means the rebind is skipped, and a warning says so: an MCP
// client that trusts "creating a mission binds the session" has no
// other way to distinguish "rebind happened" from "silently
// skipped." Failures and skips alike are returned as strings for the
// caller to fold into the result's warnings array -- MCP has no
// stderr channel to print the CLI's line to.
func (h *Handler) bindDispatchedMission(missionID string) []string {
	if h.sessionStore == nil {
		return []string{
			"binding mission: no session store wired -- active-mission sidecar not updated; " +
				"a subsequent Agent() spawn may still attribute under a previous MISSION_ID",
		}
	}
	sessionID, _ := resolve.SessionID(h.sessionStore)
	if sessionID == "" {
		return []string{
			"binding mission: no session in context -- active-mission sidecar not updated; " +
				"a subsequent Agent() spawn may still attribute under a previous MISSION_ID",
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{fmt.Sprintf("binding mission: user home dir: %v", err)}
	}
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")

	previous, prevErr := mission.ReadActiveMissionBinding(globalRoot, sessionID)
	var warnings []string
	if prevErr != nil {
		// Report and continue: the rebind below is what makes the next
		// spawn correct, and an unreadable sidecar is exactly the state
		// that must be overwritten.
		warnings = append(warnings, fmt.Sprintf("binding mission: reading active mission: %v", prevErr))
	}
	if err := mission.WriteActiveMissionOrigin(
		globalRoot, sessionID, missionID, mission.BindOriginDispatch,
	); err != nil {
		return append(warnings, fmt.Sprintf("binding mission: binding session %s to %s: %v", sessionID, missionID, err))
	}
	// create always mints a fresh mission ID, so a rebind onto the SAME
	// mission cannot arise; only the changed-mission case is reachable.
	if previous.MissionID == "" || previous.MissionID == missionID {
		return warnings
	}
	warnings = append(warnings, fmt.Sprintf(
		"session %s was bound to %s; rebound to %s -- delegations now file under %s, "+
			"and commit trailers are off until you run `ethos mission claim <id>`",
		sessionID, previous.MissionID, missionID, missionID))
	if err := mission.ClearDelegationBinding(globalRoot, sessionID); err != nil {
		warnings = append(warnings, fmt.Sprintf("binding mission: clearing delegation binding: %v", err))
	}
	return warnings
}

// handleShowMission resolves the requested mission by exact ID or
// prefix and returns its contract plus the round-by-round result
// log. Round 2 of Phase 3.6 added results to the show payload so
// the MCP surface carries the same verdict information as the CLI —
// otherwise a leader reading `mission show` via MCP would have the
// same invisible-verdict gap mdm flagged at the CLI.
//
// Results load is advisory: a corrupt sibling file produces a
// warnings entry in the payload (MCP has no stderr channel), but
// the show still succeeds with the contract visible. Round 3
// replaced the round-2 hand-rolled payload map with ShowPayload so
// the Contract's own json tags drive serialization and fields like
// `session` and `repo` round-trip without the handler having to
// enumerate them.
func (h *Handler) handleShowMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for show"), nil
	}
	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	c, err := h.missionStore.Load(id)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to load mission: %v", err)), nil
	}
	// Load results as an advisory extension. A missing or empty
	// sibling file becomes `[]` in the output (never null) so MCP
	// clients can decode into []mission.Result without a nil guard.
	// A load error becomes a warning in the payload so the caller
	// has a scriptable signal that the results file is corrupt;
	// without this, a corrupt file was indistinguishable from "no
	// result submitted".
	results, loadErr := h.missionStore.LoadResults(id)
	if results == nil {
		results = []mission.Result{}
	}
	payload := missionShowResponse{ShowPayload: mission.ShowPayload{Contract: c, Results: results}}
	if loadErr != nil {
		payload.Warnings = append(payload.Warnings,
			fmt.Sprintf("loading results: %v", loadErr))
	}
	// Corrections are advisory in the same way results are: a corrupt
	// event log becomes a warning, never a failed show — the payload
	// still carries the mission the caller asked for.
	corrections, corrWarnings, corrErr := h.missionStore.LoadCorrections(id)
	if corrections == nil {
		corrections = []mission.Correction{}
	}
	if corrErr != nil {
		payload.Warnings = append(payload.Warnings,
			fmt.Sprintf("loading corrections: %v", corrErr))
	}
	for _, w := range corrWarnings {
		payload.Warnings = append(payload.Warnings,
			fmt.Sprintf("loading corrections: %s", w))
	}
	payload.Corrections = corrections
	return jsonResult(payload)
}

// missionShowResponse extends mission.ShowPayload with a corrections
// section (DES-072). Defined here rather than adding a field to
// mission.ShowPayload itself — mission.go is outside this fix's
// write-set, and Go's JSON marshaler flattens an embedded struct's
// fields to the top level regardless of how many levels are embedded,
// so wrapping ShowPayload (which itself embeds *Contract) still
// produces one flat JSON object. Mirrors createMissionResponse's
// local-wrapper convention above.
type missionShowResponse struct {
	mission.ShowPayload
	Corrections []mission.Correction `json:"corrections"`
}

// handleListMissions returns the missions matching the status filter.
// The default filter is "open" so callers see their pending work, not
// closed historical records.
func (h *Handler) handleListMissions(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	status := stringArg(req, "status", "open")
	if !mission.IsValidStatusFilter(status) {
		return mcplib.NewToolResultError(fmt.Sprintf(
			"invalid status filter %q: must be one of open, closed, failed, escalated, abandoned, all",
			status,
		)), nil
	}
	ids, err := h.missionStore.List()
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list missions: %v", err)), nil
	}

	entries := []mission.ListEntry{}
	for _, id := range ids {
		c, loadErr := h.missionStore.Load(id)
		if loadErr != nil {
			// Skip corrupt rows; the CLI will surface them.
			continue
		}
		if !mission.StatusMatches(status, c.Status) {
			continue
		}
		entries = append(entries, mission.NewListEntry(c))
	}
	return jsonResult(entries)
}

// handleCloseMission resolves the mission by ID or prefix and applies
// the requested terminal status. Defaults to StatusClosed when no status
// argument is supplied.
func (h *Handler) handleCloseMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for close"), nil
	}
	status := stringArg(req, "status", mission.StatusClosed)

	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	result, err := h.missionStore.Close(id, status)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to close mission: %v", err)), nil
	}
	payload := map[string]any{
		"mission_id": id,
		"status":     status,
		"round":      result.Round,
		"verdict":    result.Verdict,
	}
	// Parity with the CLI close: a terminal transition ends this
	// session's work on the mission, so clear its sidecars. Closing via
	// MCP used to skip this, leaving the active-mission sidecar in place
	// and the commit-msg hook tagging later missionless commits with the
	// closed mission (ethos-jawp).
	if warnings := h.clearClosedMissionBindings(id); len(warnings) > 0 {
		payload["warnings"] = warnings
	}
	return jsonResult(payload)
}

// handleAbandonMission resolves the mission by ID or prefix and
// retires it via Store.Abandon — a distinct, more narrowly gated
// operation from Close (see the Abandon doc comment in
// internal/mission/store.go). reason is required.
func (h *Handler) handleAbandonMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for abandon"), nil
	}
	reason := stringArg(req, "reason", "")
	if strings.TrimSpace(reason) == "" {
		return mcplib.NewToolResultError("reason is required for abandon"), nil
	}

	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	c, err := h.missionStore.Abandon(id, reason)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to abandon mission: %v", err)), nil
	}
	payload := map[string]any{
		"mission_id": id,
		"status":     c.Status,
		"reason":     reason,
	}
	// Parity with close: a terminal transition ends this session's work
	// on the mission, so clear its sidecars.
	if warnings := h.clearClosedMissionBindings(id); len(warnings) > 0 {
		payload["warnings"] = warnings
	}
	return jsonResult(payload)
}

// clearClosedMissionBindings clears the calling session's mission
// sidecars for a mission that just closed, and returns one warning per
// genuine failure — nil when there was nothing to do.
//
// The close is already on disk and cannot be undone, so a cleanup
// failure must not turn the call into an error result. It rides back in
// the successful result's `warnings` array instead, which the formatter
// renders under a Warnings header: the mission closed, but a sidecar may
// still be tagging commits.
//
// No session in context means no sidecars to clear, which is ordinary
// and silent.
func (h *Handler) clearClosedMissionBindings(missionID string) []string {
	if h.sessionStore == nil {
		return nil
	}
	sessionID, _ := resolve.SessionID(h.sessionStore)
	if sessionID == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{fmt.Sprintf("clearing mission bindings: user home dir: %v", err)}
	}
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	return clearBindingWarnings(mission.ClearMissionBindings(globalRoot, sessionID, missionID))
}

// clearBindingWarnings flattens a ClearMissionBindings error into one
// warning per cause. The two sidecars are independent, so the error may
// carry two causes joined by errors.Join, which reports them through
// Unwrap() []error. One entry per cause keeps each on its own bullet in
// the formatter's Warnings section — a single joined string would render
// as one bullet with an embedded newline.
func clearBindingWarnings(err error) []string {
	if err == nil {
		return nil
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return []string{"clearing mission bindings: " + err.Error()}
	}
	causes := joined.Unwrap()
	out := make([]string, 0, len(causes))
	for _, c := range causes {
		out = append(out, "clearing mission bindings: "+c.Error())
	}
	return out
}

// handleReflectMission parses the reflection YAML and appends it to
// the mission's round-by-round reflection log via the store. The
// reflection argument is the trust boundary; the strict decoder
// rejects unknown keys, multi-document YAML, and trailing content
// the same way the contract create path does.
func (h *Handler) handleReflectMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for reflect"), nil
	}
	body := stringArg(req, "reflection", "")
	if strings.TrimSpace(body) == "" {
		return mcplib.NewToolResultError("reflection YAML body is required for reflect"), nil
	}
	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	r, err := mission.DecodeReflectionStrict([]byte(body), "mcp reflect request")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if err := h.missionStore.AppendReflection(id, r); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to record reflection: %v", err)), nil
	}
	return jsonResult(map[string]any{
		"mission_id":     id,
		"round":          r.Round,
		"recommendation": r.Recommendation,
		"created_at":     r.CreatedAt,
	})
}

// handleReflectionsMission returns the round-by-round reflection
// log for a mission. Always returns an array, never null, so MCP
// clients can decode into a typed slice without a presence check.
func (h *Handler) handleReflectionsMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for reflections"), nil
	}
	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	rs, err := h.missionStore.LoadReflections(id)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to load reflections: %v", err)), nil
	}
	if rs == nil {
		rs = []mission.Reflection{}
	}
	return jsonResult(rs)
}

// handleAdvanceMission attempts to advance the mission to the next
// round. The store enforces every gate (open status, reflection
// present, recommendation not stop/escalate, budget not exhausted);
// any refusal becomes a structured tool error so the MCP client
// sees the operator-facing reason.
func (h *Handler) handleAdvanceMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for advance"), nil
	}
	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	actor := stringArg(req, "actor", "")
	if actor == "" {
		// Default to the mission's leader so the call site can omit
		// the actor argument and still get a meaningful audit trail.
		// The fallback path mirrors the CLI's resolveActor.
		c, loadErr := h.missionStore.Load(id)
		if loadErr != nil {
			return mcplib.NewToolResultError(loadErr.Error()), nil
		}
		actor = c.Leader
	}
	newRound, err := h.missionStore.AdvanceRound(id, actor)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to advance mission: %v", err)), nil
	}
	return jsonResult(map[string]any{
		"mission_id":    id,
		"current_round": newRound,
	})
}

// handleResultMission parses the result YAML and appends it to the
// mission's round-by-round result log via the store. The result
// argument is the trust boundary; the strict decoder rejects unknown
// keys, multi-document YAML, and trailing content the same way the
// contract and reflection create paths do.
//
// Phase 3.6: this is the MCP parallel of the CLI `mission result`
// subcommand. The CLI+MCP parity test covers both surfaces to
// prevent either from regressing in isolation.
func (h *Handler) handleResultMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for result"), nil
	}
	body := stringArg(req, "result", "")
	if strings.TrimSpace(body) == "" {
		return mcplib.NewToolResultError("result YAML body is required for result"), nil
	}
	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	r, err := mission.DecodeResultStrict([]byte(body), "mcp result request")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if err := h.missionStore.AppendResult(id, r); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to record result: %v", err)), nil
	}
	return jsonResult(map[string]any{
		"mission_id": id,
		"round":      r.Round,
		"verdict":    r.Verdict,
		"confidence": r.Confidence,
		"created_at": r.CreatedAt,
	})
}

// handleResultsMission returns the round-by-round result log for a
// mission, plus the corrections filed against it (DES-072). Results
// and corrections always return as arrays, never null, so MCP
// clients can decode into typed slices without a presence check — the
// same convention handleReflectionsMission uses for reflections.
// Corrections are sourced separately from results because a
// correction never touches the results file — it lives on the event
// log instead.
func (h *Handler) handleResultsMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for results"), nil
	}
	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	rs, err := h.missionStore.LoadResults(id)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to load results: %v", err)), nil
	}
	if rs == nil {
		rs = []mission.Result{}
	}
	// Corrections are advisory here too (DES-072/ethos-268t): a
	// corrupt event log must not block reading results.yaml through
	// this path — it surfaces as a warning in the response, the same
	// treatment handleShowMission already gives corrections.
	cs, corrWarnings, corrErr := h.missionStore.LoadCorrections(id)
	if cs == nil {
		cs = []mission.Correction{}
	}
	resp := missionResultsResponse{Results: rs, Corrections: cs}
	if corrErr != nil {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("loading corrections: %v", corrErr))
	}
	for _, w := range corrWarnings {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("loading corrections: %s", w))
	}
	return jsonResult(resp)
}

// missionResultsResponse is the wire shape for handleResultsMission
// (DES-072): results plus the corrections filed against them.
// Warnings is omitempty and carries advisory corruption signals from
// loading corrections — a corrupt event log must not fail the whole
// response, mirroring handleShowMission's warnings field.
type missionResultsResponse struct {
	Results     []mission.Result     `json:"results"`
	Corrections []mission.Correction `json:"corrections"`
	Warnings    []string             `json:"warnings,omitempty"`
}

// handleCorrectMission files a correction against a closed mission.
// The correction YAML is decoded strictly, its author is resolved
// against the live identity store, and the event is appended via
// Store.Correct — the same three-step pipeline the CLI's
// runMissionCorrect runs. Author resolution happens here, not inside
// Store.Correct: the mission package carries no identity-store
// dependency (Store.ApplyServerFields takes HashSources as an
// explicit parameter for the same reason), so CLI and MCP both run
// ValidateCorrectionAuthor before calling the store.
func (h *Handler) handleCorrectMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for correct"), nil
	}
	body := stringArg(req, "correction", "")
	if strings.TrimSpace(body) == "" {
		return mcplib.NewToolResultError("correction YAML body is required for correct"), nil
	}
	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	c, err := mission.DecodeCorrectionStrict([]byte(body), "mcp correct request")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	sources, err := mission.NewLiveHashSources(h.store, h.roles, h.teams)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if err := mission.ValidateCorrectionAuthor(c.Author, sources.Identities); err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	if err := h.missionStore.Correct(id, *c); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to record correction: %v", err)), nil
	}
	// Parity with the CLI: seal the checkout's mission-log tail so the
	// correction is durable on disk even if no commit immediately
	// follows. Best-effort — the correction is already recorded.
	var warnings []string
	if repoRoot := resolve.EnvRepoRoot(); repoRoot != "" {
		if _, sErr := hook.SealMission(repoRoot, id, time.Now().UTC(), hook.SealOptions{}); sErr != nil {
			warnings = append(warnings, fmt.Sprintf("sealing mission log: %v", sErr))
		}
	}
	payload := map[string]any{
		"mission_id": id,
		"kind":       string(c.Kind),
		"round":      c.Round,
		"author":     c.Author,
	}
	if len(warnings) > 0 {
		payload["warnings"] = warnings
	}
	return jsonResult(payload)
}

// handleLogMission returns the append-only mission event log for
// post-mortem analysis. Phase 3.7 parallel of the CLI `mission log`
// subcommand — the two surfaces share FilterEvents and emit the
// same LogPayload wire shape so CLI and MCP consumers see the same
// audit trail.
//
// Filters AND-compose: `event` is a comma-separated list of types,
// `since` is an RFC3339 cutoff. Both are optional. An invalid
// `since` is a structured tool error so the caller can fix the
// timestamp and retry; an unknown event type in `event` is not
// rejected because event types are forward-compatible.
//
// A corrupt line in the on-disk log does not fail the call — the
// payload carries a warnings slice naming the failing line numbers,
// and events contains every parseable line before and after. An
// I/O failure reading the file (permission denied, other os.Open
// errors) is a structured tool error with the operator-facing
// reason.
func (h *Handler) handleLogMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for log"), nil
	}
	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	events, warnings, err := h.missionStore.LoadEvents(id)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to load events: %v", err)), nil
	}
	types := parseEventTypeList(stringArg(req, "event", ""))
	since := stringArg(req, "since", "")
	filtered, err := mission.FilterEvents(events, types, since)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if filtered == nil {
		filtered = []mission.Event{}
	}
	payload := mission.LogPayload{Events: filtered, Warnings: warnings}
	return jsonResult(payload)
}

// parseEventTypeList splits a comma-separated event filter into
// trimmed, non-empty slugs. Mirrors the CLI's parseEventTypes but
// lives here because the MCP package cannot import cmd/ethos —
// and a shared helper in internal/mission would drag string-list
// parsing into the trust-boundary package for no gain. The two
// copies are identical and test coverage on both surfaces keeps
// them in lockstep.
//
// mirror: cmd/ethos/mission.go parseEventTypes — add or remove in
// both places. Round 2 K1: kept as a deliberate 13-line
// duplication rather than a shared helper, per mdm's argument
// against coupling the trust-boundary package to CLI argument
// parsing. If a third call site lands, hoist then.
func parseEventTypeList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

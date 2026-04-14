package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/punt-labs/ethos/internal/mission"
)

// formatResult is the JSON output of the format-output hook.
type formatResult struct {
	HookSpecificOutput struct {
		HookEventName        string `json:"hookEventName"`
		UpdatedMCPToolOutput string `json:"updatedMCPToolOutput"`
		AdditionalContext    string `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

// HandleFormatOutput reads a PostToolUse hook payload from r and
// emits two-channel display output to w: a compact summary in
// updatedMCPToolOutput and full data in additionalContext.
func HandleFormatOutput(r io.Reader, w io.Writer) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("format-output: %w", err)
	}

	// Extract tool name — strip the MCP namespace prefix.
	toolFull, _ := input["tool_name"].(string)
	if toolFull == "" {
		return nil
	}
	parts := strings.Split(toolFull, "__")
	toolName := parts[len(parts)-1]

	// Check for MCP-level error — let Claude Code show it.
	if isMCPError(input) {
		return nil
	}

	// Extract the result payload.
	result := extractResult(input)
	if result == "" || result == "null" {
		return nil
	}

	// Check for error in result.
	if errMsg := jsonString(result, "error"); errMsg != "" {
		return emitSimple(w, "error: "+errMsg)
	}

	// Extract method for consolidated tools.
	method := extractMethod(input)

	// Dispatch to tool-specific formatter.
	switch toolName {
	case "identity":
		return formatIdentity(w, method, result)
	case "talent", "personality", "writing_style":
		return formatAttribute(w, toolName, method, result)
	case "session":
		return formatSession(w, method, result)
	case "ext":
		return formatExt(w, method, result)
	case "doctor":
		return formatDoctor(w, result)
	case "team":
		return formatTeam(w, method, result)
	case "role":
		return formatRole(w, method, result)
	case "mission":
		return formatMission(w, method, result)
	case "adr":
		return formatADR(w, method, result)
	default:
		return emitSimple(w, truncate(result, 200))
	}
}

// --- Identity tool formatters ---

func formatIdentity(w io.Writer, method, result string) error {
	switch method {
	case "whoami", "get":
		return formatIdentityDetail(w, result)
	case "list":
		return formatIdentityList(w, result)
	case "create":
		name := jsonString(result, "name")
		if name == "" {
			name = "identity"
		}
		return emit(w, "Created "+name, result)
	default:
		return emitSimple(w, truncate(result, 200))
	}
}

func formatIdentityDetail(w io.Writer, result string) error {
	name := jsonString(result, "name")
	if name == "" {
		return emitSimple(w, truncate(result, 200))
	}

	var lines []string
	handle := jsonString(result, "handle")
	kind := jsonString(result, "kind")
	lines = append(lines, fmt.Sprintf("%s (%s) — %s", name, handle, kind))

	if v := jsonString(result, "email"); v != "" {
		lines = append(lines, "Email: "+v)
	}
	if v := jsonString(result, "github"); v != "" {
		lines = append(lines, "GitHub: "+v)
	}
	if v := jsonString(result, "agent"); v != "" {
		lines = append(lines, "Agent: "+v)
	}
	if v := jsonString(result, "personality"); v != "" {
		lines = append(lines, "Personality: "+v)
	}
	if v := jsonString(result, "writing_style"); v != "" {
		lines = append(lines, "Writing: "+v)
	}
	if talents := jsonStringArray(result, "talents"); len(talents) > 0 {
		lines = append(lines, "Talents: "+strings.Join(talents, ", "))
	}

	summary := strings.Join(lines, "\n")
	return emit(w, summary, summary)
}

func formatIdentityList(w io.Writer, result string) error {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		return emitSimple(w, truncate(result, 200))
	}

	if len(entries) == 0 {
		return emit(w, "0 identities", "(none)")
	}

	noun := "identities"
	if len(entries) == 1 {
		noun = "identity"
	}
	summary := fmt.Sprintf("%d %s", len(entries), noun)

	// Build columnar table.
	headers := []string{"HANDLE", "NAME", "KIND", "PERSONALITY", "WRITING"}
	rows := make([][]string, len(entries))
	for i, e := range entries {
		handle, _ := e["handle"].(string)
		name, _ := e["name"].(string)
		kind, _ := e["kind"].(string)
		personality, _ := e["personality"].(string)
		writing, _ := e["writing_style"].(string)
		if personality == "" {
			personality = "-"
		}
		if writing == "" {
			writing = "-"
		}
		rows[i] = []string{handle, name, kind, personality, writing}
	}

	return emit(w, summary, FormatTable(headers, rows))
}

// FormatTable renders a columnar table with headers and rows.
// The last column is not right-padded. Columns are separated by two spaces.
func FormatTable(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var buf strings.Builder
	// Header row with ▶ prefix (matches biff /who style).
	buf.WriteString("▶  ")
	lastCol := len(headers) - 1
	for i, h := range headers {
		if i > 0 {
			buf.WriteString("  ")
		}
		if i == lastCol {
			buf.WriteString(h)
		} else {
			buf.WriteString(fmt.Sprintf("%-*s", widths[i], h))
		}
	}

	for _, row := range rows {
		buf.WriteString("\n   ") // 3-space indent to align with header
		n := len(row)
		if n > len(headers) {
			n = len(headers)
		}
		lastCol := n - 1
		for i := 0; i < n; i++ {
			if i > 0 {
				buf.WriteString("  ")
			}
			if i == lastCol {
				buf.WriteString(row[i])
			} else {
				buf.WriteString(fmt.Sprintf("%-*s", widths[i], row[i]))
			}
		}
	}

	return buf.String()
}

// --- Attribute tool formatters ---

// toolNoun returns the human-readable noun for an attribute tool name.
func toolNoun(tool string, count int) string {
	var singular, plural string
	switch tool {
	case "talent":
		singular, plural = "talent", "talents"
	case "personality":
		singular, plural = "personality", "personalities"
	case "writing_style":
		singular, plural = "writing style", "writing styles"
	default:
		singular, plural = tool, tool+"s"
	}
	if count == 1 {
		return singular
	}
	return plural
}

func formatAttribute(w io.Writer, tool, method, result string) error {
	switch method {
	case "list":
		slugs := jsonNestedStringArray(result, "attributes", "slug")
		n := len(slugs)
		summary := fmt.Sprintf("%d %s", n, toolNoun(tool, n))
		if n == 0 {
			return emit(w, summary, "(none)")
		}
		rows := make([][]string, n)
		for i, s := range slugs {
			rows[i] = []string{s}
		}
		return emit(w, summary, FormatTable([]string{"SLUG"}, rows))
	case "show":
		content := jsonString(result, "content")
		if content == "" {
			content = truncate(result, 200)
		}
		return emit(w, content, result)
	case "create":
		slug := jsonString(result, "slug")
		if slug == "" {
			slug = tool
		}
		return emit(w, "Created "+slug, result)
	case "delete", "set", "add", "remove":
		return emitSimple(w, truncate(result, 200))
	default:
		return emitSimple(w, truncate(result, 200))
	}
}

// --- Session tool formatters ---

func formatSession(w io.Writer, method, result string) error {
	switch method {
	case "roster":
		return formatSessionRoster(w, result)
	case "iam":
		return emitSimple(w, truncate(result, 200))
	default:
		return emitSimple(w, truncate(result, 200))
	}
}

func formatSessionRoster(w io.Writer, result string) error {
	var m map[string]any
	if err := json.Unmarshal([]byte(result), &m); err != nil {
		return emit(w, "Roster loaded", result)
	}

	sessionID, _ := m["session"].(string)
	arr, _ := m["participants"].([]any)
	n := len(arr)

	noun := "participants"
	if n == 1 {
		noun = "participant"
	}
	summary := fmt.Sprintf("%d %s", n, noun)
	if sessionID != "" {
		summary += fmt.Sprintf(" (session %s)", sessionID)
	}

	if n == 0 {
		return emit(w, summary, "(none)")
	}

	headers := []string{"AGENT_ID", "PERSONA", "PARENT", "TYPE"}
	rows := make([][]string, n)
	for i, v := range arr {
		p, _ := v.(map[string]any)
		agentID, _ := p["agent_id"].(string)
		persona, _ := p["persona"].(string)
		parent, _ := p["parent"].(string)
		pType, _ := p["agent_type"].(string)
		if agentID == "" {
			agentID = "-"
		}
		if persona == "" {
			persona = "-"
		}
		if parent == "" {
			parent = "-"
		}
		if pType == "" {
			pType = "-"
		}
		rows[i] = []string{agentID, persona, parent, pType}
	}

	return emit(w, summary, FormatTable(headers, rows))
}

// --- Ext tool formatters ---

func formatExt(w io.Writer, method, result string) error {
	switch method {
	case "list":
		return formatExtList(w, result)
	case "get":
		return formatExtGet(w, result)
	default:
		return emitSimple(w, truncate(result, 200))
	}
}

func formatExtList(w io.Writer, result string) error {
	var namespaces []string
	if err := json.Unmarshal([]byte(result), &namespaces); err != nil {
		return emit(w, "Extensions", result)
	}

	n := len(namespaces)
	noun := "namespaces"
	if n == 1 {
		noun = "namespace"
	}
	summary := fmt.Sprintf("%d %s", n, noun)

	if n == 0 {
		return emit(w, summary, "(none)")
	}

	rows := make([][]string, n)
	for i, ns := range namespaces {
		rows[i] = []string{ns}
	}
	return emit(w, summary, FormatTable([]string{"NAMESPACE"}, rows))
}

func formatExtGet(w io.Writer, result string) error {
	var m map[string]any
	if err := json.Unmarshal([]byte(result), &m); err != nil {
		return emit(w, "Extensions", result)
	}

	n := len(m)
	noun := "keys"
	if n == 1 {
		noun = "key"
	}
	summary := fmt.Sprintf("%d %s", n, noun)

	if n == 0 {
		return emit(w, summary, "(none)")
	}

	// Collect keys sorted for deterministic output.
	keys := make([]string, 0, n)
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([][]string, len(keys))
	for i, k := range keys {
		val := fmt.Sprintf("%v", m[k])
		rows[i] = []string{k, val}
	}
	return emit(w, summary, FormatTable([]string{"KEY", "VALUE"}, rows))
}

// --- Output helpers ---

func emit(w io.Writer, summary, ctx string) error {
	r := formatResult{}
	r.HookSpecificOutput.HookEventName = "PostToolUse"
	r.HookSpecificOutput.UpdatedMCPToolOutput = summary
	r.HookSpecificOutput.AdditionalContext = ctx
	return json.NewEncoder(w).Encode(r)
}

func emitSimple(w io.Writer, summary string) error {
	r := formatResult{}
	r.HookSpecificOutput.HookEventName = "PostToolUse"
	r.HookSpecificOutput.UpdatedMCPToolOutput = summary
	return json.NewEncoder(w).Encode(r)
}

// --- Doctor tool formatter ---

// formatDoctor splits the doctor output into a summary panel and a check table context.
// The doctor tool returns "N checks, M passed\n\n<table>".
func formatDoctor(w io.Writer, result string) error {
	parts := strings.SplitN(result, "\n\n", 2)
	summary := parts[0]
	ctx := result
	if len(parts) == 2 {
		ctx = parts[1]
	}
	return emit(w, summary, ctx)
}

// --- Team tool formatters ---

func formatTeam(w io.Writer, method, result string) error {
	switch method {
	case "list":
		return formatTeamList(w, result)
	case "show":
		return formatTeamShow(w, result)
	case "create":
		name := jsonString(result, "name")
		if name == "" {
			name = "team"
		}
		return emit(w, "Created "+name, result)
	case "delete":
		return emitSimple(w, truncate(result, 200))
	case "add_member":
		return emitSimple(w, truncate(result, 200))
	case "remove_member":
		return emitSimple(w, truncate(result, 200))
	case "add_collab":
		return emitSimple(w, truncate(result, 200))
	case "for_repo":
		return formatTeamForRepo(w, result)
	default:
		return emitSimple(w, truncate(result, 200))
	}
}

func formatTeamList(w io.Writer, result string) error {
	var names []string
	if err := json.Unmarshal([]byte(result), &names); err != nil {
		return emitSimple(w, truncate(result, 200))
	}

	n := len(names)
	noun := "teams"
	if n == 1 {
		noun = "team"
	}
	summary := fmt.Sprintf("%d %s", n, noun)

	if n == 0 {
		return emit(w, summary, "(none)")
	}

	rows := make([][]string, n)
	for i, name := range names {
		rows[i] = []string{name}
	}
	return emit(w, summary, FormatTable([]string{"TEAM"}, rows))
}

func formatTeamShow(w io.Writer, result string) error {
	name := jsonString(result, "name")
	if name == "" {
		return emitSimple(w, truncate(result, 200))
	}

	// Parse the full team object.
	var m map[string]any
	if err := json.Unmarshal([]byte(result), &m); err != nil {
		return emitSimple(w, truncate(result, 200))
	}

	// Count members for summary.
	members, _ := m["members"].([]any)
	noun := "members"
	if len(members) == 1 {
		noun = "member"
	}
	summary := fmt.Sprintf("%s (%d %s)", name, len(members), noun)

	// Build context.
	var ctx strings.Builder
	ctx.WriteString("Name: " + name)

	// Repositories.
	if repos := jsonStringArray(result, "repositories"); len(repos) > 0 {
		ctx.WriteString("\nRepositories: " + strings.Join(repos, ", "))
	}

	// Members table.
	if len(members) > 0 {
		ctx.WriteString("\n\n")
		rows := make([][]string, len(members))
		for i, v := range members {
			p, _ := v.(map[string]any)
			ident, _ := p["identity"].(string)
			role, _ := p["role"].(string)
			rows[i] = []string{ident, role}
		}
		ctx.WriteString(FormatTable([]string{"IDENTITY", "ROLE"}, rows))
	}

	// Collaborations table.
	collabs, _ := m["collaborations"].([]any)
	if len(collabs) > 0 {
		ctx.WriteString("\n\n")
		rows := make([][]string, len(collabs))
		for i, v := range collabs {
			c, _ := v.(map[string]any)
			from, _ := c["from"].(string)
			to, _ := c["to"].(string)
			ctype, _ := c["type"].(string)
			rows[i] = []string{from, to, ctype}
		}
		ctx.WriteString(FormatTable([]string{"FROM", "TO", "TYPE"}, rows))
	}

	return emit(w, summary, ctx.String())
}

func formatTeamForRepo(w io.Writer, result string) error {
	var teams []map[string]any
	if err := json.Unmarshal([]byte(result), &teams); err != nil {
		return emitSimple(w, truncate(result, 200))
	}

	n := len(teams)
	if n == 0 {
		return emit(w, "no teams found", "(none)")
	}

	noun := "teams"
	if n == 1 {
		noun = "team"
	}
	summary := fmt.Sprintf("%d %s for repo", n, noun)

	var ctx strings.Builder
	for i, t := range teams {
		if i > 0 {
			ctx.WriteString("\n\n")
		}
		name, _ := t["name"].(string)
		ctx.WriteString("Name: " + name)

		if repoArr, ok := t["repositories"].([]any); ok && len(repoArr) > 0 {
			var rs []string
			for _, r := range repoArr {
				if s, ok := r.(string); ok {
					rs = append(rs, s)
				}
			}
			ctx.WriteString("\nRepositories: " + strings.Join(rs, ", "))
		}

		if members, ok := t["members"].([]any); ok && len(members) > 0 {
			ctx.WriteString("\n")
			rows := make([][]string, len(members))
			for j, v := range members {
				p, _ := v.(map[string]any)
				ident, _ := p["identity"].(string)
				role, _ := p["role"].(string)
				rows[j] = []string{ident, role}
			}
			ctx.WriteString(FormatTable([]string{"IDENTITY", "ROLE"}, rows))
		}
	}

	return emit(w, summary, ctx.String())
}

// --- Role tool formatters ---

func formatRole(w io.Writer, method, result string) error {
	switch method {
	case "list":
		return formatRoleList(w, result)
	case "show":
		return formatRoleShow(w, result)
	case "create":
		name := jsonString(result, "name")
		if name == "" {
			name = "role"
		}
		return emit(w, "Created "+name, result)
	case "delete":
		return emitSimple(w, truncate(result, 200))
	default:
		return emitSimple(w, truncate(result, 200))
	}
}

func formatRoleList(w io.Writer, result string) error {
	var names []string
	if err := json.Unmarshal([]byte(result), &names); err != nil {
		return emitSimple(w, truncate(result, 200))
	}

	n := len(names)
	noun := "roles"
	if n == 1 {
		noun = "role"
	}
	summary := fmt.Sprintf("%d %s", n, noun)

	if n == 0 {
		return emit(w, summary, "(none)")
	}

	rows := make([][]string, n)
	for i, name := range names {
		rows[i] = []string{name}
	}
	return emit(w, summary, FormatTable([]string{"ROLE"}, rows))
}

func formatRoleShow(w io.Writer, result string) error {
	name := jsonString(result, "name")
	if name == "" {
		return emitSimple(w, truncate(result, 200))
	}

	var ctx strings.Builder
	ctx.WriteString("Name: " + name)

	if resps := jsonStringArray(result, "responsibilities"); len(resps) > 0 {
		ctx.WriteString("\nResponsibilities:")
		for _, r := range resps {
			ctx.WriteString("\n  - " + r)
		}
	}

	if perms := jsonStringArray(result, "permissions"); len(perms) > 0 {
		ctx.WriteString("\nPermissions:")
		for _, p := range perms {
			ctx.WriteString("\n  - " + p)
		}
	}

	return emit(w, name, ctx.String())
}

// --- Mission tool formatters ---

// formatMission dispatches on method for the mission MCP tool. Each
// mission method has a distinct result shape:
//
//	create        → a single Contract object
//	show          → a single Contract object
//	list          → an array of {mission_id, status, leader, ...} summaries
//	close         → {mission_id, status}
//	reflect       → {mission_id, round, recommendation, created_at}
//	reflections   → an array of Reflection objects (one per round)
//	advance       → {mission_id, current_round}
//	result        → {mission_id, round, verdict, confidence, created_at}
//	results       → an array of Result objects (one per round)
func formatMission(w io.Writer, method, result string) error {
	switch method {
	case "create":
		return formatMissionCreate(w, result)
	case "show":
		return formatMissionShow(w, result)
	case "list":
		return formatMissionList(w, result)
	case "close":
		return formatMissionClose(w, result)
	case "reflect":
		return formatMissionReflect(w, result)
	case "reflections":
		return formatMissionReflections(w, result)
	case "advance":
		return formatMissionAdvance(w, result)
	case "result":
		return formatMissionResult(w, result)
	case "results":
		return formatMissionResults(w, result)
	case "log":
		return formatMissionLog(w, result)
	default:
		return emitSimple(w, truncate(result, 200))
	}
}

// formatMissionLog renders the log method's payload as one bullet
// per event. The summary line counts the entries; an empty array
// becomes "(none)" so the operator distinguishes "no events
// recorded" from a tool error. Warnings from a partial decode
// surface as a trailing Warnings section so the operator never
// misses a corrupt line.
//
// Phase 3.7 parallel of formatMissionResults — the two share the
// one-bullet-per-round rendering convention so post-mortem tooling
// has a consistent visual shape across sibling artifacts.
func formatMissionLog(w io.Writer, result string) error {
	var payload map[string]any
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		return emitSimple(w, truncate(result, 200))
	}
	entriesRaw, _ := payload["events"].([]any)
	n := len(entriesRaw)
	noun := "events"
	if n == 1 {
		noun = "event"
	}
	summary := fmt.Sprintf("%d %s", n, noun)
	if n == 0 {
		// Still surface warnings on empty — a corrupt log with zero
		// good lines would otherwise render "(none)" with no hint
		// why the events are missing.
		var ctx strings.Builder
		ctx.WriteString("(none)")
		if warnings, _ := payload["warnings"].([]any); len(warnings) > 0 {
			writeMissionWarnings(&ctx, warnings)
		}
		return emit(w, summary, ctx.String())
	}
	var ctx strings.Builder
	for i, e := range entriesRaw {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		ts, _ := em["ts"].(string)
		evType, _ := em["event"].(string)
		actor, _ := em["actor"].(string)
		if i > 0 {
			ctx.WriteString("\n")
		}
		fmt.Fprintf(&ctx, "  - %s  %s  by %s", FormatLocalTime(ts), evType, actor)
		details, _ := em["details"].(map[string]any)
		// Inner variable is intentionally named detailSummary, not
		// summary, so it does not shadow the outer function-scope
		// `summary` (the panel title like "3 events"). Go's scoping
		// rules make the shadow harmless today, but the reused name
		// is a maintenance tripwire for any future reader — flagged
		// by Bugbot on PR #184 (round 4 B2).
		if detailSummary := summarizeEventDetailsRaw(evType, details); detailSummary != "" {
			fmt.Fprintf(&ctx, "  %s", detailSummary)
		}
	}
	if warnings, _ := payload["warnings"].([]any); len(warnings) > 0 {
		writeMissionWarnings(&ctx, warnings)
	}
	return emit(w, summary, ctx.String())
}

// summarizeEventDetailsRaw extracts a short human-readable payload
// summary from a decoded-from-JSON Details map. The logic mirrors
// cmd/ethos.summarizeEventDetails — the two cannot share code
// because this package must not import internal/mission — but they
// agree on which fields to surface for each known event type. New
// event types grow one case here and one in the CLI helper;
// forward-compatibility means an unknown type renders no details
// (just the base row) rather than a guessed field set.
func summarizeEventDetailsRaw(evType string, details map[string]any) string {
	if len(details) == 0 {
		return ""
	}
	switch evType {
	case "create":
		ticketVal := eventStr(details, "ticket")
		if ticketVal == "" {
			ticketVal = eventStr(details, "bead") // backward-compat
		}
		return joinEventParts(
			eventKV("worker", eventStr(details, "worker")),
			eventKV("evaluator", eventStr(details, "evaluator")),
			eventKV("ticket", ticketVal),
		)
	case "close":
		return joinEventParts(
			eventKV("status", eventStr(details, "status")),
			eventKV("verdict", eventStr(details, "verdict")),
			eventKVRound("round", eventRound(details, "round")),
		)
	case "result":
		return joinEventParts(
			eventKVRound("round", eventRound(details, "round")),
			eventKV("verdict", eventStr(details, "verdict")),
		)
	case "reflect":
		return joinEventParts(
			eventKVRound("round", eventRound(details, "round")),
			eventKV("rec", eventStr(details, "recommendation")),
		)
	case "round_advanced":
		from := eventRound(details, "from_round")
		to := eventRound(details, "to_round")
		if from > 0 && to > 0 {
			return fmt.Sprintf("round %d -> %d", int(from), int(to))
		}
	}
	return ""
}

// eventStr extracts a string value from an event details map.
func eventStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// eventRound extracts a numeric round from an event details map.
func eventRound(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

// eventKV returns "key=val" when val is non-empty.
func eventKV(key, val string) string {
	if val == "" {
		return ""
	}
	return key + "=" + val
}

// eventKVRound returns "key=N" when round > 0.
func eventKVRound(key string, round float64) string {
	if round <= 0 {
		return ""
	}
	return fmt.Sprintf("%s=%d", key, int(round))
}

// joinEventParts joins non-empty parts with spaces.
func joinEventParts(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// formatMissionResult renders the result method's confirmation:
// "<mission_id> round N (<verdict>)". Short enough for a single
// tool-result row but specific enough to confirm the verdict the
// worker submitted.
func formatMissionResult(w io.Writer, result string) error {
	var c map[string]any
	if err := json.Unmarshal([]byte(result), &c); err != nil {
		return emitSimple(w, truncate(result, 200))
	}
	missionID, _ := c["mission_id"].(string)
	verdict, _ := c["verdict"].(string)
	round, ok := c["round"].(float64)
	if missionID == "" || !ok {
		return emitSimple(w, truncate(result, 200))
	}
	return emitSimple(w, fmt.Sprintf("Result %s round %d (%s)", missionID, int(round), verdict))
}

// formatMissionResults renders the results method's array as one
// bullet per round. The summary line counts the entries; an empty
// array becomes "(none)" so the operator distinguishes "no results
// yet" from a tool error.
func formatMissionResults(w io.Writer, result string) error {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		return emitSimple(w, truncate(result, 200))
	}
	n := len(entries)
	noun := "results"
	if n == 1 {
		noun = "result"
	}
	summary := fmt.Sprintf("%d %s", n, noun)
	if n == 0 {
		return emit(w, summary, "(none)")
	}
	var ctx strings.Builder
	for i, e := range entries {
		round, _ := e["round"].(float64)
		verdict, _ := e["verdict"].(string)
		author, _ := e["author"].(string)
		confidence, _ := e["confidence"].(float64)
		if i > 0 {
			ctx.WriteString("\n")
		}
		fmt.Fprintf(&ctx, "  - round %d (%s) by %s — confidence=%.2f",
			int(round), verdict, author, confidence)
		if files, ok := e["files_changed"].([]any); ok && len(files) > 0 {
			for _, f := range files {
				if fc, ok := f.(map[string]any); ok {
					path, _ := fc["path"].(string)
					added, _ := fc["added"].(float64)
					removed, _ := fc["removed"].(float64)
					fmt.Fprintf(&ctx, "\n      • %s: +%d -%d", path, int(added), int(removed))
				}
			}
		}
		if evidence, ok := e["evidence"].([]any); ok && len(evidence) > 0 {
			for _, ev := range evidence {
				if em, ok := ev.(map[string]any); ok {
					name, _ := em["name"].(string)
					status, _ := em["status"].(string)
					fmt.Fprintf(&ctx, "\n      • %s: %s", name, status)
				}
			}
		}
	}
	return emit(w, summary, ctx.String())
}

// formatMissionReflect renders the reflect method's confirmation:
// "<mission_id> round N (<recommendation>)" — short enough to fit
// on a single tool-result row but specific enough to confirm what
// was recorded.
func formatMissionReflect(w io.Writer, result string) error {
	var c map[string]any
	if err := json.Unmarshal([]byte(result), &c); err != nil {
		return emitSimple(w, truncate(result, 200))
	}
	missionID, _ := c["mission_id"].(string)
	rec, _ := c["recommendation"].(string)
	round, ok := c["round"].(float64)
	if missionID == "" || !ok {
		return emitSimple(w, truncate(result, 200))
	}
	return emitSimple(w, fmt.Sprintf("Reflected %s round %d (%s)", missionID, int(round), rec))
}

// formatMissionAdvance renders the advance method's confirmation:
// "<mission_id> → round N". On error the MCP layer surfaces the
// gate refusal verbatim, so the formatter only handles the success
// shape.
func formatMissionAdvance(w io.Writer, result string) error {
	var c map[string]any
	if err := json.Unmarshal([]byte(result), &c); err != nil {
		return emitSimple(w, truncate(result, 200))
	}
	missionID, _ := c["mission_id"].(string)
	round, ok := c["current_round"].(float64)
	if missionID == "" || !ok {
		return emitSimple(w, truncate(result, 200))
	}
	return emitSimple(w, fmt.Sprintf("Advanced %s to round %d", missionID, int(round)))
}

// formatMissionReflections renders the reflections method's array
// as one bullet per round. The summary line counts the entries; an
// empty array becomes "(none)" so the operator distinguishes "no
// reflections yet" from a tool error.
func formatMissionReflections(w io.Writer, result string) error {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		return emitSimple(w, truncate(result, 200))
	}
	n := len(entries)
	noun := "reflections"
	if n == 1 {
		noun = "reflection"
	}
	summary := fmt.Sprintf("%d %s", n, noun)
	if n == 0 {
		return emit(w, summary, "(none)")
	}
	var ctx strings.Builder
	for i, e := range entries {
		round, _ := e["round"].(float64)
		rec, _ := e["recommendation"].(string)
		author, _ := e["author"].(string)
		converging, _ := e["converging"].(bool)
		if i > 0 {
			ctx.WriteString("\n")
		}
		// Uppercase terminal recommendations (stop, escalate) so the
		// operator scanning a long reflection log spots a blocking
		// decision at a glance.
		if mission.IsTerminalRecommendation(rec) {
			rec = strings.ToUpper(rec)
		}
		fmt.Fprintf(&ctx, "  - round %d (%s) by %s — converging=%t",
			int(round), rec, author, converging)
		if signals, ok := e["signals"].([]any); ok {
			for _, s := range signals {
				if str, ok := s.(string); ok {
					fmt.Fprintf(&ctx, "\n      • %s", str)
				}
			}
		}
		if reason, _ := e["reason"].(string); reason != "" {
			fmt.Fprintf(&ctx, "\n      reason: %s", reason)
		}
	}
	return emit(w, summary, ctx.String())
}

// formatMissionShow renders a single contract in a tabwriter-aligned
// field block. The summary line is "<mission_id> (<status>)"; the
// context is the field block plus write_set / tools / success_criteria
// as bullet lists, and — when round 2 of Phase 3.6 landed the
// results field on the show payload — a round-by-round Results
// section below the contract so the MCP hook surface does not hide
// the verdict that authorized a close. The pattern parallels
// formatMissionResults; the two share one bullet shape.
func formatMissionShow(w io.Writer, result string) error {
	var c map[string]any
	if err := json.Unmarshal([]byte(result), &c); err != nil {
		return emitSimple(w, truncate(result, 200))
	}
	missionID, _ := c["mission_id"].(string)
	status, _ := c["status"].(string)
	if missionID == "" {
		return emitSimple(w, truncate(result, 200))
	}

	summary := fmt.Sprintf("%s (%s)", missionID, status)

	var ctx strings.Builder
	writeMissionFields(&ctx, c)
	// The show payload (round 2+) carries a top-level `results`
	// array; render it under the contract block. Missing or nil
	// means a pre-3.6 consumer fed us a bare contract — no
	// results section, no error. An empty array renders "(none)"
	// so the operator sees the section exists and is empty.
	if raw, ok := c["results"]; ok {
		writeMissionResults(&ctx, raw)
	}
	// Surface any warnings from a corrupt sibling file. Round 3
	// added this — without it, a corrupted results file was
	// indistinguishable from "no results yet" for MCP callers.
	if raw, ok := c["warnings"]; ok {
		writeMissionWarnings(&ctx, raw)
	}

	return emit(w, summary, ctx.String())
}

// writeMissionResults renders the top-level `results` array from a
// show payload as a Results section under the contract block. Empty
// or missing arrays render "(none)" so the operator distinguishes
// "no result submitted yet" from "formatter forgot to render the
// section". The per-round bullet shape matches formatMissionResults'
// array method — one helper, one visual convention.
func writeMissionResults(ctx *strings.Builder, raw any) {
	entries, ok := raw.([]any)
	if !ok {
		return
	}
	ctx.WriteString("\n\nResults:")
	if len(entries) == 0 {
		ctx.WriteString("\n  (none)")
		return
	}
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		round, _ := em["round"].(float64)
		verdict, _ := em["verdict"].(string)
		author, _ := em["author"].(string)
		confidence, _ := em["confidence"].(float64)
		fmt.Fprintf(ctx, "\n  - round %d (%s) by %s — confidence=%.2f",
			int(round), verdict, author, confidence)
		if prose, _ := em["prose"].(string); prose != "" {
			// First line of prose only; multi-line narrative
			// belongs in the dedicated `mission results` call.
			line := strings.SplitN(prose, "\n", 2)[0]
			fmt.Fprintf(ctx, "\n      %s", line)
		}
	}
}

// writeMissionWarnings renders a top-level `warnings` array (when
// non-empty) as a Warnings section. The show payload emits this
// only when an advisory sibling load failed; the operator must see
// the failure even in JSON-parsed MCP mode.
func writeMissionWarnings(ctx *strings.Builder, raw any) {
	entries, ok := raw.([]any)
	if !ok || len(entries) == 0 {
		return
	}
	ctx.WriteString("\n\nWarnings:")
	for _, e := range entries {
		if s, ok := e.(string); ok {
			ctx.WriteString("\n  - " + s)
		}
	}
}

// formatMissionCreate reuses the show layout — create returns the full
// persisted contract — but uses a "Created <id>" summary line.
func formatMissionCreate(w io.Writer, result string) error {
	var c map[string]any
	if err := json.Unmarshal([]byte(result), &c); err != nil {
		return emitSimple(w, truncate(result, 200))
	}
	missionID, _ := c["mission_id"].(string)
	if missionID == "" {
		// Early guard: a contract without a mission_id is malformed.
		// Surface that explicitly rather than rendering a partial card
		// that looks legitimate.
		return emit(w, "Created (malformed contract)", "(malformed contract — missing mission_id)")
	}

	summary := "Created " + missionID

	var ctx strings.Builder
	writeMissionFields(&ctx, c)

	return emit(w, summary, ctx.String())
}

// writeMissionFields writes a contract's key fields into ctx using
// text/tabwriter, matching the CLI's printContract layout. The single
// shared layout means the CLI and the MCP formatter never drift.
//
// Loose JSON-map coupling is intentional: this formatter must not
// import internal/mission. Other format* functions in this file
// follow the same convention.
func writeMissionFields(ctx *strings.Builder, c map[string]any) {
	missionID, _ := c["mission_id"].(string)
	if missionID == "" {
		ctx.WriteString("(malformed contract — missing mission_id)")
		return
	}
	status, _ := c["status"].(string)
	leader, _ := c["leader"].(string)
	worker, _ := c["worker"].(string)

	tw := tabwriter.NewWriter(ctx, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Mission:\t%s\n", missionID)
	fmt.Fprintf(tw, "Status:\t%s\n", status)
	createdAt, _ := c["created_at"].(string)
	if createdAt != "" {
		fmt.Fprintf(tw, "Created:\t%s\n", FormatLocalTime(createdAt))
	}
	if updatedAt, _ := c["updated_at"].(string); updatedAt != "" && updatedAt != createdAt {
		fmt.Fprintf(tw, "Updated:\t%s\n", FormatLocalTime(updatedAt))
	}
	if closedAt, _ := c["closed_at"].(string); closedAt != "" {
		fmt.Fprintf(tw, "Closed:\t%s\n", FormatLocalTime(closedAt))
	}
	fmt.Fprintf(tw, "Leader:\t%s\n", leader)
	fmt.Fprintf(tw, "Worker:\t%s\n", worker)

	if ev, ok := c["evaluator"].(map[string]any); ok {
		handle, _ := ev["handle"].(string)
		pinned, _ := ev["pinned_at"].(string)
		hash, _ := ev["hash"].(string)
		line := handle
		if pinned != "" {
			line += fmt.Sprintf(" (pinned %s", FormatLocalTime(pinned))
			if hash != "" {
				line += ", hash " + hash
			}
			line += ")"
		}
		fmt.Fprintf(tw, "Evaluator:\t%s\n", line)
	}

	if budget, ok := c["budget"].(map[string]any); ok {
		rounds, roundsOK := budget["rounds"].(float64)
		reflect, _ := budget["reflection_after_each"].(bool)
		// Skip the budget line if rounds is missing or out of range
		// rather than emitting "0 round(s)" which would look like a
		// real (and invalid) value.
		if roundsOK && rounds >= 1 && rounds <= 10 {
			fmt.Fprintf(tw, "Budget:\t%d round(s), reflection_after_each=%t\n", int(rounds), reflect)
			// 3.4: render the current round so the operator can see
			// progress at a glance. The current_round field is only
			// present on 3.4+ contracts; pre-3.4 missions decode it
			// as zero, which we skip rather than emit "0 of 3".
			if cr, ok := c["current_round"].(float64); ok && cr >= 1 {
				fmt.Fprintf(tw, "Round:\t%d of %d\n", int(cr), int(rounds))
			}
		}
	}

	_ = tw.Flush()

	// Labeled/multi-value sections. Match the CLI's printContract
	// order: Inputs, Write set, Tools, Success criteria, Context.
	writeMissionInputs(ctx, c["inputs"])
	writeMissionBulletSection(ctx, "Write set", c["write_set"])
	writeMissionBulletSection(ctx, "Tools", c["tools"])
	writeMissionBulletSection(ctx, "Success criteria", c["success_criteria"])
	if contextStr, _ := c["context"].(string); contextStr != "" {
		ctx.WriteString("\n\nContext:\n")
		ctx.WriteString(contextStr)
	}
}

// writeMissionInputs renders the Inputs section for writeMissionFields,
// matching the CLI's printContract layout: labeled lines under an
// "Inputs:" header (ticket, file, ref). Accepts both "ticket" and
// legacy "bead" keys for backward compatibility with old event logs.
// Empty or missing inputs emit nothing.
func writeMissionInputs(ctx *strings.Builder, raw any) {
	inputs, ok := raw.(map[string]any)
	if !ok {
		return
	}
	ticket, _ := inputs["ticket"].(string)
	if ticket == "" {
		ticket, _ = inputs["bead"].(string) // backward-compat
	}
	files, _ := inputs["files"].([]any)
	refs, _ := inputs["references"].([]any)
	if ticket == "" && len(files) == 0 && len(refs) == 0 {
		return
	}
	ctx.WriteString("\n\nInputs:")
	if ticket != "" {
		ctx.WriteString("\n  ticket: " + ticket)
	}
	for _, f := range files {
		if s, ok := f.(string); ok {
			ctx.WriteString("\n  file: " + s)
		}
	}
	for _, r := range refs {
		if s, ok := r.(string); ok {
			ctx.WriteString("\n  ref:  " + s)
		}
	}
}

// FormatLocalTime converts an RFC3339 timestamp to a local-time
// display form (`2006-01-02 15:04 MST`). The year and zone
// (local zone abbreviation when available, numeric offset such
// as `+0530` otherwise) are present so two operators in
// different timezones reading the same mission log identify
// the same event without ambiguity. Exported so cmd/ethos can
// share the same formatter across session, mission, and any
// future command that renders a timestamp to the user — one
// implementation, one visual convention. If parsing fails,
// returns the raw string unchanged so the caller still gets
// something displayable.
func FormatLocalTime(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.Local().Format("2006-01-02 15:04 MST")
}

// writeMissionBulletSection writes a section header and a bullet list
// of string values. Non-string entries are skipped. Empty or missing
// sections emit nothing — the formatter stays tidy when optional fields
// are absent.
func writeMissionBulletSection(ctx *strings.Builder, title string, raw any) {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return
	}
	ctx.WriteString("\n\n" + title + ":")
	for _, v := range arr {
		if s, ok := v.(string); ok {
			ctx.WriteString("\n  - " + s)
		}
	}
}

// formatMissionList renders the list method's summary as a table with
// one row per mission.
func formatMissionList(w io.Writer, result string) error {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		return emitSimple(w, truncate(result, 200))
	}

	n := len(entries)
	noun := "missions"
	if n == 1 {
		noun = "mission"
	}
	summary := fmt.Sprintf("%d %s", n, noun)

	if n == 0 {
		return emit(w, summary, "(none)")
	}

	headers := []string{"MISSION", "STATUS", "LEADER", "WORKER", "EVALUATOR", "CREATED"}
	rows := make([][]string, n)
	for i, e := range entries {
		missionID, _ := e["mission_id"].(string)
		status, _ := e["status"].(string)
		leader, _ := e["leader"].(string)
		worker, _ := e["worker"].(string)
		evaluator, _ := e["evaluator"].(string)
		createdAt, _ := e["created_at"].(string)
		rows[i] = []string{missionID, status, leader, worker, evaluator, FormatLocalTime(createdAt)}
	}
	return emit(w, summary, FormatTable(headers, rows))
}

// formatMissionClose renders the close method's confirmation as a
// single-line summary.
func formatMissionClose(w io.Writer, result string) error {
	var c map[string]any
	if err := json.Unmarshal([]byte(result), &c); err != nil {
		return emitSimple(w, truncate(result, 200))
	}
	missionID, _ := c["mission_id"].(string)
	status, _ := c["status"].(string)
	if missionID == "" {
		return emitSimple(w, truncate(result, 200))
	}
	return emitSimple(w, fmt.Sprintf("Closed %s as %s", missionID, status))
}

// --- ADR tool formatters ---

func formatADR(w io.Writer, method, result string) error {
	switch method {
	case "create":
		id := jsonString(result, "id")
		title := jsonString(result, "title")
		if id == "" {
			return emitSimple(w, truncate(result, 200))
		}
		return emit(w, fmt.Sprintf("Created %s: %s", id, title), result)
	case "list":
		return formatADRList(w, result)
	case "show":
		return formatADRShow(w, result)
	default:
		return emitSimple(w, truncate(result, 200))
	}
}

func formatADRList(w io.Writer, result string) error {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		return emitSimple(w, truncate(result, 200))
	}

	n := len(entries)
	noun := "ADRs"
	if n == 1 {
		noun = "ADR"
	}
	summary := fmt.Sprintf("%d %s", n, noun)

	if n == 0 {
		return emit(w, summary, "(none)")
	}

	headers := []string{"ID", "STATUS", "TITLE", "AUTHOR"}
	rows := make([][]string, n)
	for i, e := range entries {
		id, _ := e["id"].(string)
		status, _ := e["status"].(string)
		title, _ := e["title"].(string)
		author, _ := e["author"].(string)
		if author == "" {
			author = "-"
		}
		rows[i] = []string{id, status, title, author}
	}
	return emit(w, summary, FormatTable(headers, rows))
}

func formatADRShow(w io.Writer, result string) error {
	id := jsonString(result, "id")
	status := jsonString(result, "status")
	title := jsonString(result, "title")
	if id == "" {
		return emitSimple(w, truncate(result, 200))
	}

	summary := fmt.Sprintf("%s (%s): %s", id, status, title)

	var ctx strings.Builder
	ctx.WriteString("ID: " + id)
	ctx.WriteString("\nTitle: " + title)
	ctx.WriteString("\nStatus: " + status)
	if author := jsonString(result, "author"); author != "" {
		ctx.WriteString("\nAuthor: " + author)
	}
	if context := jsonString(result, "context"); context != "" {
		ctx.WriteString("\n\nContext:\n  " + context)
	}
	if decision := jsonString(result, "decision"); decision != "" {
		ctx.WriteString("\n\nDecision:\n  " + decision)
	}
	if alts := jsonStringArray(result, "alternatives"); len(alts) > 0 {
		ctx.WriteString("\n\nAlternatives:")
		for _, a := range alts {
			ctx.WriteString("\n  - " + a)
		}
	}

	return emit(w, summary, ctx.String())
}

// --- JSON extraction helpers ---

// isMCPError checks if the tool response indicates an MCP-level error.
func isMCPError(input map[string]any) bool {
	resp, ok := input["tool_response"]
	if !ok {
		return false
	}
	if arr, ok := resp.([]any); ok && len(arr) > 0 {
		if m, ok := arr[0].(map[string]any); ok {
			if isErr, ok := m["is_error"].(bool); ok {
				return isErr
			}
		}
	}
	return false
}

// extractResult unpacks the result text from a PostToolUse payload.
// Handles string-encoded JSON, arrays, and objects.
func extractResult(input map[string]any) string {
	resp, ok := input["tool_response"]
	if !ok {
		return ""
	}

	var text string
	switch v := resp.(type) {
	case []any:
		if len(v) > 0 {
			if m, ok := v[0].(map[string]any); ok {
				text, _ = m["text"].(string)
			}
		}
	case map[string]any:
		data, _ := json.Marshal(v)
		text = string(data)
	case string:
		text = v
	}

	if text == "" {
		return ""
	}

	// Try to unwrap string-encoded JSON.
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		if m, ok := parsed.(map[string]any); ok {
			// Unwrap a "result" wrapper if present.
			if inner, ok := m["result"]; ok {
				data, _ := json.Marshal(inner)
				return string(data)
			}
			return text
		}
		if _, ok := parsed.([]any); ok {
			return text
		}
	}

	return text
}

// extractMethod gets the method parameter from the tool input.
func extractMethod(input map[string]any) string {
	if ti, ok := input["tool_input"].(map[string]any); ok {
		if m, ok := ti["method"].(string); ok {
			return m
		}
	}
	return ""
}

// jsonString extracts a string field from a JSON string.
func jsonString(jsonStr, key string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// jsonStringArray extracts a string array field from a JSON string.
func jsonStringArray(jsonStr, key string) []string {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return nil
	}
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// jsonNestedStringArray extracts a string field from each element of
// a nested array. E.g., jsonNestedStringArray(json, "attributes", "slug")
// extracts slug from each element of the attributes array.
func jsonNestedStringArray(jsonStr, arrayKey, fieldKey string) []string {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return nil
	}
	arr, ok := m[arrayKey].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, v := range arr {
		if obj, ok := v.(map[string]any); ok {
			if s, ok := obj[fieldKey].(string); ok {
				result = append(result, s)
			}
		}
	}
	return result
}

// truncate limits a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

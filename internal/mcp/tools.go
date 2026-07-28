// Package mcp provides MCP tool definitions and handlers for ethos.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/punt-labs/ethos/internal/adr"
	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/doctor"
	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/repomiss"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/schema"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/punt-labs/ethos/internal/team"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Handler groups MCP tool handlers with shared stores.
type Handler struct {
	store         identity.IdentityStore
	sessionStore  *session.Store
	talents       *attribute.Store
	personalities *attribute.Store
	writingStyles *attribute.Store
	roles         *role.LayeredStore
	teams         *team.LayeredStore
	missionStore  *mission.Store
	adrStore      *adr.Store
	vendor        VendorRunner
}

// Option configures optional Handler fields.
type Option func(*Handler)

// WithSessionStore sets the session store on the handler.
func WithSessionStore(ss *session.Store) Option {
	return func(h *Handler) { h.sessionStore = ss }
}

// WithRoleStore sets the role store on the handler.
func WithRoleStore(rs *role.LayeredStore) Option {
	return func(h *Handler) { h.roles = rs }
}

// WithTeamStore sets the team store on the handler.
func WithTeamStore(ts *team.LayeredStore) Option {
	return func(h *Handler) { h.teams = ts }
}

// WithMissionStore sets the mission store on the handler.
func WithMissionStore(ms *mission.Store) Option {
	return func(h *Handler) { h.missionStore = ms }
}

// WithADRStore sets the ADR store on the handler.
func WithADRStore(as *adr.Store) Option {
	return func(h *Handler) { h.adrStore = as }
}

// NewHandler creates a Handler with the given stores.
// Panics if identity store is nil. Session store may be nil (session
// tools will return errors if called without it). Attribute stores
// must be provided by the caller to ensure both repo and global roots
// are searched when a layered identity store is in use.
func NewHandler(s identity.IdentityStore, talents, personalities, writingStyles *attribute.Store, ss ...*session.Store) *Handler {
	if s == nil {
		panic("mcp.NewHandler: store must not be nil")
	}
	if talents == nil {
		panic("mcp.NewHandler: talents store must not be nil")
	}
	if personalities == nil {
		panic("mcp.NewHandler: personalities store must not be nil")
	}
	if writingStyles == nil {
		panic("mcp.NewHandler: writingStyles store must not be nil")
	}
	h := &Handler{
		store:         s,
		talents:       talents,
		personalities: personalities,
		writingStyles: writingStyles,
	}
	if len(ss) > 0 {
		h.sessionStore = ss[0]
	}
	return h
}

// NewHandlerWithOptions creates a Handler with required stores and options.
func NewHandlerWithOptions(s identity.IdentityStore, talents, personalities, writingStyles *attribute.Store, opts ...Option) *Handler {
	if s == nil {
		panic("mcp.NewHandlerWithOptions: store must not be nil")
	}
	if talents == nil {
		panic("mcp.NewHandlerWithOptions: talents store must not be nil")
	}
	if personalities == nil {
		panic("mcp.NewHandlerWithOptions: personalities store must not be nil")
	}
	if writingStyles == nil {
		panic("mcp.NewHandlerWithOptions: writingStyles store must not be nil")
	}
	h := &Handler{
		store:         s,
		talents:       talents,
		personalities: personalities,
		writingStyles: writingStyles,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// RegisterTools adds all ethos MCP tools to the given server.
func (h *Handler) RegisterTools(s *mcpserver.MCPServer) {
	// Identity tool (consolidated)
	s.AddTool(h.identityTool(), h.handleIdentity)
	// Extension tool (consolidated)
	s.AddTool(h.extTool(), h.handleExt)
	// Session tool (consolidated)
	s.AddTool(h.sessionTool(), h.handleSession)
	// Doctor tool (standalone admin tool)
	s.AddTool(h.doctorTool(), h.handleDoctor)
	// Attribute tools (consolidated)
	h.registerAttributeTools(s)
	// Role tool (if store configured)
	if h.roles != nil {
		s.AddTool(h.roleTool(), h.handleRole)
	}
	// Team tool (if store configured)
	if h.teams != nil {
		s.AddTool(h.teamTool(), h.handleTeam)
	}
	// Mission tool (if store configured)
	if h.missionStore != nil {
		s.AddTool(h.missionTool(), h.handleMission)
	}
	// ADR tool (if store configured)
	if h.adrStore != nil {
		s.AddTool(h.adrTool(), h.handleADR)
	}
	// Vendor tool (if a runner is configured)
	if h.vendor != nil {
		s.AddTool(h.vendorTool(), h.handleVendor)
	}
}

// --- Tool Definitions ---

func (h *Handler) identityTool() mcplib.Tool {
	// method and reference are dispatch-level, not identity fields; the rest
	// (name, handle, kind, email, github, agent, writing_style, personality,
	// talents) are generated from the schema registry.
	fixed := []mcplib.ToolOption{
		mcplib.WithDescription("Manage identities. Methods: whoami, list, get, create."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("whoami", "list", "get", "create"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithBoolean("reference",
			mcplib.Description("If true, return attribute slugs only without resolving .md content. For whoami, get."),
		),
	}
	return mcplib.NewTool("identity", withOptions(fixed, schema.Identity)...)
}

// --- Tool Handlers ---

func (h *Handler) handleIdentity(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	method := stringArg(req, "method", "")
	switch method {
	case "whoami":
		return h.handleWhoami(ctx, req)
	case "list":
		return h.handleListIdentities(ctx, req)
	case "get":
		return h.handleGetIdentity(ctx, req)
	case "create":
		return h.handleCreateIdentity(ctx, req)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

func (h *Handler) handleIam(_ context.Context, req mcplib.CallToolRequest, sessionID string) (*mcplib.CallToolResult, error) {
	persona := stringArg(req, "persona", "")
	if persona == "" {
		return mcplib.NewToolResultError("persona is required for iam"), nil
	}

	// Key the participant on ETHOS_AGENT_ID then the Claude PID — parity
	// with the CLI so the same declaration records the same agent key on
	// both surfaces (DES-061 R4).
	agentID := os.Getenv("ETHOS_AGENT_ID")
	if agentID == "" {
		agentID = process.FindClaudePID()
	}

	if err := h.sessionStore.Join(sessionID, session.Participant{
		AgentID: agentID,
		Persona: persona,
	}); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to set persona: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Set persona %q for %s in session %s", persona, agentID, sessionID)), nil
}

func (h *Handler) handleWhoami(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle, err := resolve.Resolve(h.store, h.sessionStore)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("no identity resolved: %v", err)), nil
	}

	var opts []identity.LoadOption
	if boolArg(req, "reference", false) {
		opts = append(opts, identity.Reference(true))
	}

	id, loadErr := h.store.Load(handle, opts...)
	if loadErr != nil {
		return identityLoadError(handle, loadErr), nil
	}

	return jsonResult(id)
}

// identityLoadError renders a failed identity Load for MCP. An
// incomplete repo-authoritative set carries its own fully-formed
// diagnostic — which refs are missing, where ethos looked, and the
// vendor command that fixes it — so it is surfaced verbatim rather than
// flattened into a generic "not found" that discards all of it
// (DES-057 Part A, DES-020).
func identityLoadError(handle string, err error) *mcplib.CallToolResult {
	var incomplete *repomiss.ErrIncompleteRepoSet
	if errors.As(err, &incomplete) {
		return mcplib.NewToolResultError(err.Error())
	}
	return mcplib.NewToolResultError(fmt.Sprintf("identity %q not found: %v", handle, err))
}

func (h *Handler) handleListIdentities(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	result, err := h.store.List()
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list identities: %v", err)), nil
	}

	type entry struct {
		Handle       string `json:"handle"`
		Name         string `json:"name"`
		Kind         string `json:"kind"`
		Personality  string `json:"personality,omitempty"`
		WritingStyle string `json:"writing_style,omitempty"`
	}

	entries := make([]entry, 0, len(result.Identities))
	for _, id := range result.Identities {
		entries = append(entries, entry{
			Handle:       id.Handle,
			Name:         id.Name,
			Kind:         id.Kind,
			Personality:  id.Personality,
			WritingStyle: id.WritingStyle,
		})
	}

	return jsonResult(entries)
}

func (h *Handler) handleGetIdentity(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	if handle == "" {
		return mcplib.NewToolResultError("handle is required"), nil
	}

	var opts []identity.LoadOption
	if boolArg(req, "reference", false) {
		opts = append(opts, identity.Reference(true))
	}

	id, err := h.store.Load(handle, opts...)
	if err != nil {
		return identityLoadError(handle, err), nil
	}

	return jsonResult(id)
}

func (h *Handler) handleCreateIdentity(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	// Read every field strictly: a wrong-typed value must fail loud, not
	// coerce to "" (silently dropping an optional field) or to a bare
	// "required" error that hides the real cause. Same silent-drop class as
	// the role and team create paths.
	fields := map[string]string{}
	for _, key := range []string{"name", "handle", "kind", "email", "github", "agent", "writing_style", "personality"} {
		v, err := stringArgStrict(req, key)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		fields[key] = v
	}
	for _, key := range []string{"name", "handle", "kind"} {
		if fields[key] == "" {
			return mcplib.NewToolResultError(fmt.Sprintf("%s is required for create", key)), nil
		}
	}
	talents, err := stringListArg(req, "talents")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	id := &identity.Identity{
		Name:         fields["name"],
		Handle:       fields["handle"],
		Kind:         fields["kind"],
		Email:        fields["email"],
		GitHub:       fields["github"],
		Agent:        fields["agent"],
		WritingStyle: fields["writing_style"],
		Personality:  fields["personality"],
		Talents:      talents,
	}

	if err := id.Validate(); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("validation failed: %v", err)), nil
	}
	if err := h.store.Save(id); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to save: %v", err)), nil
	}

	return jsonResult(id)
}

// --- Extension Tool (consolidated) ---

func (h *Handler) extTool() mcplib.Tool {
	return mcplib.NewTool("ext",
		mcplib.WithDescription("Manage tool-scoped extensions on identities. Methods: get, set, del, list."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("get", "set", "del", "list"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("handle", mcplib.Required(),
			mcplib.Description("Identity handle."),
		),
		mcplib.WithString("namespace",
			mcplib.Description("Tool namespace (e.g. beadle, biff). Required for get, set, del."),
		),
		mcplib.WithString("key",
			mcplib.Description("Key name. Required for set. Optional for get (omit for all keys) and del (omit to delete namespace)."),
		),
		mcplib.WithString("value",
			mcplib.Description("Value to store. Required for set."),
		),
	)
}

func (h *Handler) handleExt(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	method := stringArg(req, "method", "")
	handle := stringArg(req, "handle", "")
	namespace := stringArg(req, "namespace", "")
	key := stringArg(req, "key", "")
	value := stringArg(req, "value", "")

	if handle == "" {
		return mcplib.NewToolResultError("handle is required"), nil
	}

	switch method {
	case "get":
		if namespace == "" {
			return mcplib.NewToolResultError("namespace is required for get"), nil
		}
		m, err := h.store.ExtGet(handle, namespace, key)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		return jsonResult(m)
	case "set":
		if namespace == "" {
			return mcplib.NewToolResultError("namespace is required for set"), nil
		}
		if key == "" {
			return mcplib.NewToolResultError("key is required for set"), nil
		}
		if _, ok := req.GetArguments()["value"]; !ok {
			return mcplib.NewToolResultError("value is required for set"), nil
		}
		if err := h.store.ExtSet(handle, namespace, key, value); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("set %s/%s/%s", handle, namespace, key)), nil
	case "del":
		if namespace == "" {
			return mcplib.NewToolResultError("namespace is required for del"), nil
		}
		if err := h.store.ExtDel(handle, namespace, key); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		if key == "" {
			return mcplib.NewToolResultText(fmt.Sprintf("deleted namespace %s/%s", handle, namespace)), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("deleted %s/%s/%s", handle, namespace, key)), nil
	case "list":
		namespaces, err := h.store.ExtList(handle)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		if namespaces == nil {
			namespaces = []string{}
		}
		return jsonResult(namespaces)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

// --- Session Tool (consolidated) ---

func (h *Handler) sessionTool() mcplib.Tool {
	return mcplib.NewTool("session",
		mcplib.WithDescription("Manage session roster. Methods: roster, join, leave, iam. Session ID is auto-discovered if omitted."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("roster", "join", "leave", "iam"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("session_id",
			mcplib.Description("Session ID. Omit to auto-discover via process tree."),
		),
		mcplib.WithString("agent_id",
			mcplib.Description("Agent ID. Required for join, leave."),
		),
		mcplib.WithString("persona",
			mcplib.Description("Persona handle. Optional for join."),
		),
		mcplib.WithString("parent",
			mcplib.Description("Parent agent ID. Optional for join."),
		),
		mcplib.WithString("agent_type",
			mcplib.Description("Agent type (e.g. code-reviewer, Explore). Optional for join."),
		),
	)
}

// resolveSessionID discovers the session ID: the session_id arg, then the
// shared harness-neutral chain (ETHOS_SESSION, then the Claude PID walk) —
// parity with the CLI (DES-061 R4).
func (h *Handler) resolveSessionID(req mcplib.CallToolRequest) (string, error) {
	sessionID := stringArg(req, "session_id", "")
	if sessionID != "" {
		return sessionID, nil
	}
	if h.sessionStore == nil {
		return "", fmt.Errorf("session store not configured")
	}
	if sid, _ := resolve.SessionID(h.sessionStore); sid != "" {
		return sid, nil
	}
	return "", fmt.Errorf("no active session; run `ethos session start` or pass session_id")
}

func (h *Handler) handleSession(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if h.sessionStore == nil {
		return mcplib.NewToolResultError("session store not configured"), nil
	}
	method := stringArg(req, "method", "")
	sessionID, err := h.resolveSessionID(req)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	switch method {
	case "roster":
		roster, err := h.sessionStore.Load(sessionID)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to load roster: %v", err)), nil
		}
		return jsonResult(roster)

	case "join":
		if stringArg(req, "agent_id", "") == "" {
			return mcplib.NewToolResultError("agent_id is required for join"), nil
		}
		p := session.Participant{
			AgentID:   stringArg(req, "agent_id", ""),
			Persona:   stringArg(req, "persona", ""),
			Parent:    stringArg(req, "parent", ""),
			AgentType: stringArg(req, "agent_type", ""),
		}
		if err := h.sessionStore.Join(sessionID, p); err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to join: %v", err)), nil
		}
		return jsonResult(p)

	case "leave":
		agentID := stringArg(req, "agent_id", "")
		if agentID == "" {
			return mcplib.NewToolResultError("agent_id is required for leave"), nil
		}
		if err := h.sessionStore.Leave(sessionID, agentID); err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to leave: %v", err)), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("Removed %s from session %s", agentID, sessionID)), nil

	case "iam":
		return h.handleIam(ctx, req, sessionID)

	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

// --- Doctor Tool (standalone admin) ---

func (h *Handler) doctorTool() mcplib.Tool {
	return mcplib.NewTool("doctor",
		mcplib.WithDescription("Check installation health: identity directory, human identity, default agent, duplicate fields."),
	)
}

func (h *Handler) handleDoctor(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	results := doctor.RunAll(h.store, h.sessionStore, resolve.FindRepoRoot(), resolve.StoreRepoRoot(), h.teams)

	// Format as table text per DES-020.
	headers := []string{"NAME", "STATUS", "DETAIL"}
	rows := make([][]string, len(results))
	for i, r := range results {
		rows[i] = []string{r.Name, r.Status, r.Detail}
	}

	summary := fmt.Sprintf("%d checks, %d passed", len(results), doctor.PassedCount(results))
	if w := doctor.WarnCount(results); w > 0 {
		summary += fmt.Sprintf(", %d warning(s)", w)
	}
	table := hook.FormatTable(headers, rows)
	return mcplib.NewToolResultText(summary + "\n\n" + table), nil
}

// --- Helpers ---

func stringArg(req mcplib.CallToolRequest, key, fallback string) string {
	args := req.GetArguments()
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return fallback
}

func boolArg(req mcplib.CallToolRequest, key string, fallback bool) bool {
	args := req.GetArguments()
	if v, ok := args[key]; ok {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			return strings.EqualFold(b, "true") || b == "1"
		}
	}
	return fallback
}

// stringArgStrict reads a string argument, erroring when the key is present
// but not a string. Unlike stringArg it does not coerce a wrong-typed value
// to a fallback, which would let a malformed field slip through create as if
// omitted — the silent-drop class DES-066 exists to close. An absent key
// returns the empty string, so a required-field caller still checks for "".
func stringArgStrict(req mcplib.CallToolRequest, key string) (string, error) {
	v, ok := req.GetArguments()[key]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", key, v)
	}
	return s, nil
}

// stringListArg reads a list-of-string argument, erroring when the key is
// present but is not an array or holds a non-string element. It never
// silently drops a malformed value or element — the silent-drop class
// DES-066 exists to close. An absent key returns nil, matching an omitted
// optional list.
func stringListArg(req mcplib.CallToolRequest, key string) ([]string, error) {
	rawVal, ok := req.GetArguments()[key]
	if !ok {
		return nil, nil
	}
	raw, ok := rawVal.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%s must be an array, got %T", key, rawVal)
	}
	var out []string
	for i, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string, got %T", key, i, v)
		}
		out = append(out, s)
	}
	return out, nil
}

func jsonResult(v any) (*mcplib.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("JSON marshal error: %v", err)), nil
	}
	return mcplib.NewToolResultText(string(data)), nil
}

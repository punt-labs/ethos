package diagnostic

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/punt-labs/ethos/v4/internal/redact"
)

type hookKey struct{}

// HookInvocation carries metadata from hook dispatch to the invocation log.
type HookInvocation struct {
	mu                        sync.Mutex
	project, checkout, tool   string
	sessionID, agentID, agent string
}

func WithHookInvocation(ctx context.Context) (context.Context, *HookInvocation) {
	h := &HookInvocation{project: "unknown"}
	ctx = context.WithValue(ctx, hookKey{}, h)
	SetHookIdentity(ctx, os.Getenv("ETHOS_SESSION"), os.Getenv("ETHOS_AGENT_ID"), os.Getenv("CLAUDE_AGENT_TYPE"))
	return ctx, h
}

// SetHookProject records names only, not the absolute paths of local checkouts.
func SetHookProject(ctx context.Context, project, checkout string) {
	if h, ok := ctx.Value(hookKey{}).(*HookInvocation); ok {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.project, h.checkout = "none", ""
		if project != "" {
			h.project = redact.Prose(filepath.Base(project))
		} else if checkout != "" {
			h.project = "unknown"
		}
		if checkout != "" && checkout != project {
			h.checkout = redact.Prose(filepath.Base(checkout))
		}
	}
}

var hookToolName = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)
var hookAgent = regexp.MustCompile(`^([a-zA-Z0-9_-]{2,127}:)?[a-zA-Z0-9_-]{1,128}$`)

func safeHookAgent(agent string) bool {
	return len(agent) <= 128 && hookAgent.MatchString(agent) && redact.SecretsString(agent) == agent
}

// SetHookInput records allowlisted identifiers, never tool inputs or output.
func SetHookInput(ctx context.Context, input map[string]any) {
	sessionID, _ := input["session_id"].(string)
	agentID, _ := input["agent_id"].(string)
	agentType, _ := input["agent_type"].(string)
	if handle, _ := input["agent_handle"].(string); safeHookAgent(handle) {
		agentType = handle
	}
	SetHookIdentity(ctx, sessionID, agentID, agentType)
	if h, ok := ctx.Value(hookKey{}).(*HookInvocation); ok {
		if tool, _ := input["tool_name"].(string); hookToolName.MatchString(tool) && redact.SecretsString(tool) == tool {
			h.mu.Lock()
			h.tool = tool
			h.mu.Unlock()
		}
	}
}

// SetHookIdentity enriches the log with session or agent identity already known to the hook.
func SetHookIdentity(ctx context.Context, sessionID, agentID, agent string) {
	if h, ok := ctx.Value(hookKey{}).(*HookInvocation); ok {
		h.mu.Lock()
		defer h.mu.Unlock()
		if redact.IsSessionID(sessionID) {
			if h.sessionID != "" && h.sessionID != sessionID {
				h.agentID, h.agent = "", ""
			}
			h.sessionID = sessionID
		}
		if redact.IsSessionID(agentID) {
			if agentID != h.agentID {
				h.agent = ""
			}
			h.agentID = agentID
		}
		if safeHookAgent(agent) {
			h.agent = agent
		}
	}
}

func (h *HookInvocation) Attrs() []any {
	h.mu.Lock()
	defer h.mu.Unlock()
	attrs := []any{"project", h.project, "pid", os.Getpid()}
	if h.checkout != "" {
		attrs = append(attrs, "checkout", h.checkout)
	}
	if h.tool != "" {
		attrs = append(attrs, "tool", h.tool)
	}
	if h.sessionID != "" {
		attrs = append(attrs, "session_id", h.sessionID)
	}
	if h.agentID != "" {
		attrs = append(attrs, "agent_id", h.agentID)
	}
	if h.agent != "" {
		attrs = append(attrs, "agent", h.agent)
	}
	return attrs
}

// LogHookCheck records a check's runtime without logging its arguments or output.
func LogHookCheck(ctx context.Context, checkout, cwd, stage, name, outcome string, duration time.Duration) {
	if h, ok := ctx.Value(hookKey{}).(*HookInvocation); ok {
		attrs := append(h.Attrs(), "check_checkout", redact.Prose(filepath.Base(checkout)), "stage", stage, "check", redact.Prose(name), "outcome", outcome, "duration_ms", duration.Milliseconds())
		if cwd == "" {
			cwd = checkout
		}
		if dir, err := filepath.Rel(checkout, cwd); err == nil && filepath.IsLocal(dir) {
			attrs = append(attrs, "dir", redact.Prose(filepath.ToSlash(dir)))
		}
		FromContext(ctx).Info("Check finished", attrs...)
	}
}

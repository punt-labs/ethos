package diagnostic

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

type hookKey struct{}

// HookInvocation carries metadata from hook dispatch to the invocation log.
type HookInvocation struct {
	mu                      sync.Mutex
	project, checkout, tool string
}

func WithHookInvocation(ctx context.Context) (context.Context, *HookInvocation) {
	h := &HookInvocation{project: "unknown"}
	return context.WithValue(ctx, hookKey{}, h), h
}

// SetHookProject records names only, not the absolute paths of local checkouts.
func SetHookProject(ctx context.Context, project, checkout string) {
	if h, ok := ctx.Value(hookKey{}).(*HookInvocation); ok {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.project, h.checkout = "none", ""
		if project != "" {
			h.project = filepath.Base(project)
		} else if checkout != "" {
			h.project = "unknown"
		}
		if checkout != "" && checkout != project {
			h.checkout = filepath.Base(checkout)
		}
	}
}

var hookToolName = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)

// SetHookTool records a tool identifier, never its inputs or output.
func SetHookTool(ctx context.Context, input map[string]any) {
	if h, ok := ctx.Value(hookKey{}).(*HookInvocation); ok {
		if tool, _ := input["tool_name"].(string); hookToolName.MatchString(tool) {
			h.mu.Lock()
			h.tool = tool
			h.mu.Unlock()
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
	return attrs
}

// LogHookCheck records a check's runtime without logging its arguments or output.
func LogHookCheck(ctx context.Context, checkout, stage, name, outcome string, duration time.Duration) {
	if h, ok := ctx.Value(hookKey{}).(*HookInvocation); ok {
		attrs := append(h.Attrs(), "check_checkout", filepath.Base(checkout), "stage", stage, "check", name, "outcome", outcome, "duration_ms", duration.Milliseconds())
		FromContext(ctx).Info("Check finished", attrs...)
	}
}

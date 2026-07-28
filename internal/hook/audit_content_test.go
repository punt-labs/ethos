package hook

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBareToolName covers the MCP namespace stripping the policy match
// depends on: a plugin-namespaced tool and a built-in must reduce to
// the same key shape.
func TestBareToolName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"mcp__plugin_beadle_email__send_email", "send_email"},
		{"mcp__beadle-email__send_email", "send_email"},
		{"send_email", "send_email"},
		{"Bash", "Bash"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, bareToolName(tt.in))
		})
	}
}

// TestRedactSensitiveContent is the policy table. The send_email cases
// come from the line that reached the public-website repo (ethos-ggtu):
// a full recap body and its recipient, committed.
func TestRedactSensitiveContent(t *testing.T) {
	tests := []struct {
		name        string
		tool        string
		input       map[string]any
		want        map[string]any
		wantChanged bool
	}{
		{
			name: "send_email keeps subject and reduces the rest",
			tool: "mcp__plugin_beadle_email__send_email",
			input: map[string]any{
				"to":      "jim@punt-labs.com",
				"subject": "Recap: tool-state cleanup",
				"body":    "Recap — ethos office hours, first issue handled end-to-end.",
			},
			want: map[string]any{
				"to":      "[redacted]",
				"subject": "Recap: tool-state cleanup",
				"body":    "[redacted]",
			},
			wantChanged: true,
		},
		{
			name: "send_email reduces a field the policy never named",
			tool: "send_email",
			input: map[string]any{
				"subject":     "hello",
				"cc":          "someone@example.com",
				"attachments": []any{"~/secret.pdf"},
				"html":        true,
			},
			want: map[string]any{
				"subject":     "hello",
				"cc":          "[redacted]",
				"attachments": "[redacted]",
				"html":        "[redacted]",
			},
			wantChanged: true,
		},
		{
			name: "send_email subject still loses an address",
			tool: "send_email",
			input: map[string]any{
				"subject": "reply to jim@punt-labs.com about the recap",
				"body":    "…",
			},
			want: map[string]any{
				"subject": "reply to [redacted-email] about the recap",
				"body":    "[redacted]",
			},
			wantChanged: true,
		},
		{
			name: "CronCreate prompt loses the address",
			tool: "CronCreate",
			input: map[string]any{
				"cron":   "*/2 * * * *",
				"prompt": "Poll PR 257 and email jim@punt-labs.com when it merges.",
			},
			want: map[string]any{
				"cron":   "*/2 * * * *",
				"prompt": "Poll PR 257 and email [redacted-email] when it merges.",
			},
			wantChanged: true,
		},
		{
			name: "address nested under a prompt is swept at depth",
			tool: "Agent",
			input: map[string]any{
				"prompt": map[string]any{
					"steps": []any{"notify bwk@punt-labs.com"},
				},
			},
			want: map[string]any{
				"prompt": map[string]any{
					"steps": []any{"notify [redacted-email]"},
				},
			},
			wantChanged: true,
		},
		{
			name:        "Read is untouched",
			tool:        "Read",
			input:       map[string]any{"file_path": "<repo>/internal/hook/audit_log.go"},
			want:        map[string]any{"file_path": "<repo>/internal/hook/audit_log.go"},
			wantChanged: false,
		},
		{
			name: "Edit is untouched",
			tool: "Edit",
			input: map[string]any{
				"file_path":  "~/notes.md",
				"old_string": "a",
				"new_string": "b",
			},
			want: map[string]any{
				"file_path":  "~/notes.md",
				"old_string": "a",
				"new_string": "b",
			},
			wantChanged: false,
		},
		{
			name:        "a structured field is not swept",
			tool:        "Bash",
			input:       map[string]any{"command": "git log --author=jim@punt-labs.com"},
			want:        map[string]any{"command": "git log --author=jim@punt-labs.com"},
			wantChanged: false,
		},
		{
			name:        "a module version is not mistaken for an address",
			tool:        "Bash",
			input:       map[string]any{"description": "go get gopkg.in/yaml.v3@v3.0.1"},
			want:        map[string]any{"description": "go get gopkg.in/yaml.v3@v3.0.1"},
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := redactSensitiveContent(tt.tool, tt.input)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantChanged, changed)
		})
	}

	t.Run("nil input stays nil", func(t *testing.T) {
		got, changed := redactSensitiveContent("send_email", nil)
		assert.Nil(t, got)
		assert.False(t, changed)
	})
}

// TestBuildAuditEntry_HashOverRedactedForm is the ordering invariant
// (DES-054): the stored hash must be the hash of what is on disk. A
// hash over the raw form would make every send_email line
// unverifiable and would break cross-machine collision detection.
func TestBuildAuditEntry_HashOverRedactedForm(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	input := map[string]any{
		"tool_name": "mcp__plugin_beadle_email__send_email",
		"tool_input": map[string]any{
			"to":      "jim@punt-labs.com",
			"subject": "Recap",
			"body":    "the full body that must not be committed",
		},
	}

	entry := buildAuditEntry(input, "sess-1", "", now)

	assert.True(t, entry.Redacted, "a reduced line must carry the marker")
	assert.Equal(t, "Recap", entry.ToolInput["subject"])
	assert.Equal(t, "[redacted]", entry.ToolInput["to"])
	assert.Equal(t, "[redacted]", entry.ToolInput["body"])

	// The hash is the hash of what is stored, not of what came in.
	wantHash := hashToolInput(map[string]any{"tool_input": entry.ToolInput})
	assert.Equal(t, wantHash, entry.ToolInputHash)

	rawHash := hashToolInput(map[string]any{"tool_input": input["tool_input"]})
	assert.NotEqual(t, rawHash, entry.ToolInputHash,
		"hashing the raw form would defeat the point of redacting it")

	// The line as it lands on disk carries neither the body nor the
	// recipient — the assertion written the way the defect was found.
	line, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(line), "jim@punt-labs.com")
	assert.NotContains(t, string(line), "the full body that must not be committed")
}

// TestBuildAuditEntry_NonSensitiveUnchanged pins the no-weakening
// requirement: a tool outside the policy produces the same line it did
// before the policy existed, marker absent from the JSON entirely.
func TestBuildAuditEntry_NonSensitiveUnchanged(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	input := map[string]any{
		"tool_name": "Read",
		"tool_input": map[string]any{
			"file_path": "/tmp/ethos/internal/hook/audit_log.go",
		},
	}

	entry := buildAuditEntry(input, "sess-1", "/tmp/ethos", now)

	assert.False(t, entry.Redacted)
	assert.Equal(t, map[string]any{"file_path": "<repo>/internal/hook/audit_log.go"},
		entry.ToolInput)

	line, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(line), `"redacted"`),
		"the marker must be omitted, not written as false, so unaffected lines keep their bytes")
}

// TestBuildAuditEntry_CronPromptPII covers the second committed leak:
// an address inside a CronCreate prompt.
func TestBuildAuditEntry_CronPromptPII(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	input := map[string]any{
		"tool_name": "CronCreate",
		"tool_input": map[string]any{
			"cron":   "*/2 * * * *",
			"prompt": "When PR 257 merges, email jim@punt-labs.com a recap.",
		},
	}

	entry := buildAuditEntry(input, "sess-1", "", now)

	assert.True(t, entry.Redacted)
	assert.Equal(t, "*/2 * * * *", entry.ToolInput["cron"],
		"a structured sibling field must survive verbatim")
	assert.Equal(t, "When PR 257 merges, email [redacted-email] a recap.",
		entry.ToolInput["prompt"])
	assert.NotContains(t, entry.ToolInputPreview, "jim@punt-labs.com",
		"the preview is derived from the redacted form")
}

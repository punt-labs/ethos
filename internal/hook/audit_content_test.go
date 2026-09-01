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
			name: "reply_message keeps subject and reduces the rest",
			tool: "mcp__plugin_beadle_email__reply_message",
			input: map[string]any{
				"message_id": "42",
				"subject":    "Re: recap",
				"body":       "The full reply body that must not be committed.",
				"to":         "jim@punt-labs.com",
				"cc":         "bwk@punt-labs.com",
			},
			want: map[string]any{
				"message_id": "[redacted]",
				"subject":    "Re: recap",
				"body":       "[redacted]",
				"to":         "[redacted]",
				"cc":         "[redacted]",
			},
			wantChanged: true,
		},
		{
			name: "create_draft keeps subject and reduces the rest",
			tool: "mcp__plugin_beadle_email__create_draft",
			input: map[string]any{
				"subject": "Draft recap",
				"body":    "Draft body that must not be committed.",
				"to":      "jim@punt-labs.com",
				"bcc":     "someone@example.com",
			},
			want: map[string]any{
				"subject": "Draft recap",
				"body":    "[redacted]",
				"to":      "[redacted]",
				"bcc":     "[redacted]",
			},
			wantChanged: true,
		},
		{
			name: "an unenrolled tool still cannot leak a recipient",
			tool: "mcp__some_future_plugin__dispatch_notice",
			input: map[string]any{
				"to":   "jim@punt-labs.com",
				"cc":   []any{"a@example.com", "b@example.com"},
				"note": "unswept",
			},
			want: map[string]any{
				"to":   "[redacted]",
				"cc":   []any{"[redacted]", "[redacted]"},
				"note": "unswept",
			},
			wantChanged: true,
		},
		{
			// A mail header is "Display Name <addr>". Sweeping the
			// address alone would commit the name, which identifies
			// the person just as well.
			name: "a display name beside an address goes with it",
			tool: "mcp__some_future_plugin__dispatch_notice",
			input: map[string]any{
				"to":       "Jim Freeman <jim@punt-labs.com>",
				"reply_to": []any{"Brian K <bwk@punt-labs.com>"},
			},
			want: map[string]any{
				"to":       "[redacted]",
				"reply_to": []any{"[redacted]"},
			},
			wantChanged: true,
		},
		{
			// A recipient key nested under a prompt is still a
			// recipient key: the widest reduction wins.
			name: "a recipient nested in a prompt is reduced whole",
			tool: "Agent",
			input: map[string]any{
				"prompt": map[string]any{
					"to":   "Jim Freeman <jim@punt-labs.com>",
					"step": "send the recap to jim@punt-labs.com",
				},
			},
			want: map[string]any{
				"prompt": map[string]any{
					"to":   "[redacted]",
					"step": "send the recap to [redacted-email]",
				},
			},
			wantChanged: true,
		},
		{
			// The address book is a list of real people: a legal
			// name, an address, and nicknames, none of it
			// git-tracked-file material.
			name: "add_contact keeps nothing",
			tool: "mcp__plugin_beadle_email__add_contact",
			input: map[string]any{
				"name":        "Jim Freeman",
				"email":       "jim@punt-labs.com",
				"aliases":     []any{"jim", "jmf"},
				"notes":       "operator",
				"permissions": "rwx",
			},
			want: map[string]any{
				"name":        "[redacted]",
				"email":       "[redacted]",
				"aliases":     "[redacted]",
				"notes":       "[redacted]",
				"permissions": "[redacted]",
			},
			wantChanged: true,
		},
		{
			name:        "remove_contact keeps nothing",
			tool:        "mcp__plugin_beadle_email__remove_contact",
			input:       map[string]any{"name": "Jim Freeman"},
			want:        map[string]any{"name": "[redacted]"},
			wantChanged: true,
		},
		{
			// find_contact looks a person up by "name, email, or
			// alias" — the address arrives under query, which no
			// other pass covered.
			name:        "find_contact loses an address in its query",
			tool:        "mcp__plugin_beadle_email__find_contact",
			input:       map[string]any{"query": "jim@punt-labs.com"},
			want:        map[string]any{"query": "[redacted-email]"},
			wantChanged: true,
		},
		{
			name:        "a query naming no address survives",
			tool:        "mcp__plugin_quarry_quarry__search",
			input:       map[string]any{"query": "what were Q3 margins"},
			want:        map[string]any{"query": "what were Q3 margins"},
			wantChanged: false,
		},
		{
			// search_messages narrows by sender substring, which is
			// not an address and stays legible.
			name: "a from substring is not an address",
			tool: "mcp__plugin_beadle_email__search_messages",
			input: map[string]any{
				"from":   "jim",
				"folder": "INBOX",
			},
			want: map[string]any{
				"from":   "jim",
				"folder": "INBOX",
			},
			wantChanged: false,
		},
		{
			name: "a recipient key holding an agent name is left alone",
			tool: "SendMessage",
			input: map[string]any{
				"recipient": "bwk-audit-seal-r2",
				"summary":   "assign task 1",
			},
			want: map[string]any{
				"recipient": "bwk-audit-seal-r2",
				"summary":   "assign task 1",
			},
			wantChanged: false,
		},
		{
			name: "a biff handle in a to field survives",
			tool: "mcp__plugin_biff_tty__write",
			input: map[string]any{
				"to":      "claude:tty16",
				"message": "ack",
			},
			want: map[string]any{
				"to":      "claude:tty16",
				"message": "ack",
			},
			wantChanged: false,
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

	t.Run("a sensitive call that supplied only kept fields is not marked", func(t *testing.T) {
		got, changed := redactSensitiveContent("send_email",
			map[string]any{"subject": "no address here"})
		assert.Equal(t, map[string]any{"subject": "no address here"}, got)
		assert.False(t, changed, "nothing was removed, so the line lost no content")
	})
}

// TestRedactToolInput_NoHomeDropsInput is the audit path's fail-closed
// rule. Path redaction needs a usable home prefix to know which
// absolute path is the operator's; without one the input cannot be
// redacted, so it is dropped rather than stored raw. Audit logging must
// not block a tool call, so failing closed here costs the payload, not
// the call.
//
// "/" is covered alongside unset because os.UserHomeDir returns it
// without an error on ios; mission.NewPathRedactor refuses it on every
// platform, so the assertion holds wherever the test runs.
func TestRedactToolInput_NoHomeDropsInput(t *testing.T) {
	for _, home := range []string{"", "/"} {
		t.Run("HOME="+home, func(t *testing.T) {
			t.Setenv("HOME", home)

			got, redacted := redactToolInput(map[string]any{
				"tool_input": map[string]any{"file_path": "/Users/jdoe/.ssh/id_ed25519"},
			}, "Read", "")

			assert.Nil(t, got, "unredactable input must not reach the entry")
			assert.True(t, redacted, "the line must say its content was removed")
		})
	}
}

// TestBuildAuditEntry_NoHomeKeepsTheTrail asserts the line survives the
// dropped payload: an operator reconstructing the session still gets
// the timestamp, the tool, and the delegation linkage.
func TestBuildAuditEntry_NoHomeKeepsTheTrail(t *testing.T) {
	t.Setenv("HOME", "/")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	entry := buildAuditEntry(map[string]any{
		"tool_name":     "Bash",
		"delegation_id": "d-2026-07-28-001",
		"tool_input":    map[string]any{"command": "cat /Users/jdoe/.ssh/id_ed25519"},
	}, "sess-1", "", now)

	assert.Equal(t, "Bash", entry.Tool)
	assert.Equal(t, "d-2026-07-28-001", entry.DelegationID)
	assert.True(t, entry.Redacted)
	assert.Nil(t, entry.ToolInput)
	assert.Empty(t, entry.ToolInputHash)
	assert.Empty(t, entry.ToolInputPreview)

	line, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(line), "/Users/jdoe")
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

// TestBuildAuditEntry_ReplyMessage covers the sibling defect: enrolling
// only send_email left reply_message — same body, same recipients, one
// name over — writing both into the git-tracked log. The assertions are
// the ones that would have caught it: nothing of the body or the
// addresses in the marshalled line, and the hash over the stored form.
func TestBuildAuditEntry_ReplyMessage(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	input := map[string]any{
		"tool_name": "mcp__plugin_beadle_email__reply_message",
		"tool_input": map[string]any{
			"message_id": "4711",
			"subject":    "Re: recap",
			"body":       "the full reply that must not be committed",
			"to":         "jim@punt-labs.com",
			"cc":         []any{"bwk@punt-labs.com"},
		},
	}

	entry := buildAuditEntry(input, "sess-1", "", now)

	assert.True(t, entry.Redacted)
	assert.Equal(t, "Re: recap", entry.ToolInput["subject"],
		"the subject is what keeps the line worth reading")
	assert.Equal(t, "[redacted]", entry.ToolInput["body"])
	assert.Equal(t, "[redacted]", entry.ToolInput["to"])
	assert.Equal(t, "[redacted]", entry.ToolInput["cc"])

	wantHash := hashToolInput(map[string]any{"tool_input": entry.ToolInput})
	assert.Equal(t, wantHash, entry.ToolInputHash,
		"the hash must be over what is on disk (DES-054)")

	line, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(line), "jim@punt-labs.com")
	assert.NotContains(t, string(line), "bwk@punt-labs.com")
	assert.NotContains(t, string(line), "the full reply that must not be committed")
}

// TestBuildAuditEntry_AddContact covers the address book. add_contact
// is the one beadle tool that writes a person's legal name and address
// together, and it composes no message, so nothing in the call survives.
func TestBuildAuditEntry_AddContact(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	entry := buildAuditEntry(map[string]any{
		"tool_name": "mcp__plugin_beadle_email__add_contact",
		"tool_input": map[string]any{
			"name":  "Jim Freeman",
			"email": "jim@punt-labs.com",
		},
	}, "sess-1", "", now)

	assert.True(t, entry.Redacted)
	assert.Equal(t, "[redacted]", entry.ToolInput["name"])
	assert.Equal(t, "[redacted]", entry.ToolInput["email"])

	line, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(line), "Jim Freeman",
		"a contact's name is as identifying as the address beside it")
	assert.NotContains(t, string(line), "jim@punt-labs.com")
	assert.Contains(t, string(line), `"name"`,
		"the keys stay so the line still shows a contact was added")
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

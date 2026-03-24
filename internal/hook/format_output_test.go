package hook

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runFormat calls HandleFormatOutput with the given payload and returns
// the output written to the writer. Does not touch os.Stdout.
func runFormat(t *testing.T, payload []byte) string {
	t.Helper()
	var buf bytes.Buffer
	_ = HandleFormatOutput(bytes.NewReader(payload), &buf)
	return buf.String()
}

// makeToolPayload creates a PostToolUse hook JSON payload.
func makeToolPayload(toolName string, method string, resultJSON string) []byte {
	input := map[string]any{
		"tool_name": "mcp__plugin_ethos_self__" + toolName,
		"tool_response": []any{
			map[string]any{
				"text":     resultJSON,
				"is_error": false,
			},
		},
	}
	if method != "" {
		input["tool_input"] = map[string]any{"method": method}
	}
	data, _ := json.Marshal(input)
	return data
}

func parseFormatResult(t *testing.T, output string) formatResult {
	t.Helper()
	var r formatResult
	require.NoError(t, json.Unmarshal([]byte(output), &r))
	return r
}

// --- Identity tool tests ---

func TestFormatOutput_Identity_Whoami(t *testing.T) {
	result := `{"name":"Alice","handle":"alice","kind":"human","email":"alice@example.com","github":"alice-gh","personality":"friendly","writing_style":"concise","talents":["go","testing"]}`
	payload := makeToolPayload("identity", "whoami", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "PostToolUse", r.HookSpecificOutput.HookEventName)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Alice (alice) — human")
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Email: alice@example.com")
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Talents: go, testing")
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Identity_List(t *testing.T) {
	result := `[{"handle":"alice","name":"Alice","kind":"human","personality":"friendly","active":true},{"handle":"bob","name":"Bob","kind":"agent","personality":"","active":false}]`
	payload := makeToolPayload("identity", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	// Panel: count summary.
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "2 identities")
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "1 active")
	// Context: columnar table with headers.
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "HANDLE")
	assert.Contains(t, ctx, "NAME")
	assert.Contains(t, ctx, "KIND")
	assert.Contains(t, ctx, "PERSONALITY")
	assert.Contains(t, ctx, "ACTIVE")
	// Data rows with alignment.
	assert.Contains(t, ctx, "alice")
	assert.Contains(t, ctx, "bob")
	// Active markers.
	assert.Contains(t, ctx, "*")
	assert.Contains(t, ctx, "-")
}

func TestFormatOutput_Identity_List_Singular(t *testing.T) {
	result := `[{"handle":"alice","name":"Alice","kind":"human","personality":"friendly","active":true}]`
	payload := makeToolPayload("identity", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "1 identity,")
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "1 active")
}

func TestFormatOutput_Identity_List_Empty(t *testing.T) {
	result := `[]`
	payload := makeToolPayload("identity", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "0 identities", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Equal(t, "(none)", r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Identity_Get(t *testing.T) {
	result := `{"name":"Alice","handle":"alice","kind":"human","email":"alice@example.com"}`
	payload := makeToolPayload("identity", "get", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Alice (alice)")
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Identity_Create(t *testing.T) {
	result := `{"name":"Bob","handle":"bob","kind":"agent"}`
	payload := makeToolPayload("identity", "create", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Created Bob", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

// --- Attribute tool tests ---

func TestFormatOutput_Talent_List(t *testing.T) {
	result := `{"attributes":[{"slug":"go"},{"slug":"testing"}]}`
	payload := makeToolPayload("talent", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "go, testing", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_Talent_Show(t *testing.T) {
	result := `{"slug":"go","content":"# Go Development\nExpert in Go."}`
	payload := makeToolPayload("talent", "show", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "# Go Development")
}

func TestFormatOutput_Talent_Create(t *testing.T) {
	result := `{"slug":"go-dev"}`
	payload := makeToolPayload("talent", "create", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Created go-dev", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_Talent_Delete(t *testing.T) {
	// MCP delete returns plain text, not JSON.
	result := `"Deleted talent \"go-dev\""`
	payload := makeToolPayload("talent", "delete", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Deleted talent")
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "go-dev")
}

func TestFormatOutput_Personality_Delete(t *testing.T) {
	result := `"Deleted personality \"friendly\""`
	payload := makeToolPayload("personality", "delete", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Deleted personality")
}

func TestFormatOutput_WritingStyle_Delete(t *testing.T) {
	result := `"Deleted writing style \"concise\""`
	payload := makeToolPayload("writing_style", "delete", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Deleted writing style")
}

func TestFormatOutput_Talent_Add(t *testing.T) {
	result := `"Added talent go to alice"`
	payload := makeToolPayload("talent", "add", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Added talent go to alice")
}

func TestFormatOutput_Personality_Set(t *testing.T) {
	result := `"Set personality friendly on alice"`
	payload := makeToolPayload("personality", "set", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Set personality friendly on alice")
}

// --- Session tool tests ---

func TestFormatOutput_Session_Roster(t *testing.T) {
	result := `{"session":"abc","participants":[{"agent_id":"user1"}]}`
	payload := makeToolPayload("session", "roster", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Roster loaded", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Identity_Iam(t *testing.T) {
	result := `"Set persona claude for 12345 in session abc"`
	payload := makeToolPayload("identity", "iam", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Set persona")
}

// --- Ext tool tests ---

func TestFormatOutput_Ext_Get(t *testing.T) {
	result := `{"tty":"s001"}`
	payload := makeToolPayload("ext", "get", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Extensions", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Ext_Set(t *testing.T) {
	result := `"set alice/biff/tty"`
	payload := makeToolPayload("ext", "set", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "set alice/biff/tty")
}

// --- Edge cases ---

func TestFormatOutput_EmptyInput(t *testing.T) {
	var buf bytes.Buffer
	_ = HandleFormatOutput(bytes.NewReader(nil), &buf)
	assert.Empty(t, buf.String())
}

func TestFormatOutput_MCPError(t *testing.T) {
	input := map[string]any{
		"tool_name": "mcp__plugin_ethos_self__identity",
		"tool_response": []any{
			map[string]any{
				"text":     `{"error": "something broke"}`,
				"is_error": true,
			},
		},
	}
	data, _ := json.Marshal(input)

	var buf bytes.Buffer
	_ = HandleFormatOutput(bytes.NewReader(data), &buf)
	assert.Empty(t, buf.String()) // MCP errors are passed through to Claude Code.
}

func TestFormatOutput_ResultError(t *testing.T) {
	result := `{"error": "identity not found"}`
	payload := makeToolPayload("identity", "get", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "error: identity not found", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_UnknownTool(t *testing.T) {
	result := `"some result"`
	payload := makeToolPayload("unknown_tool", "", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "some result")
}

package hook

import (
	"bytes"
	"encoding/json"
	"strings"
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

// --- FormatTable direct tests ---

func TestFormatTable(t *testing.T) {
	t.Run("header has arrow prefix", func(t *testing.T) {
		got := FormatTable([]string{"A", "B"}, nil)
		assert.True(t, strings.HasPrefix(got, "▶  "), "header must start with ▶ prefix")
	})

	t.Run("data rows have 3-space indent", func(t *testing.T) {
		got := FormatTable([]string{"X"}, [][]string{{"val"}})
		lines := strings.Split(got, "\n")
		require.Equal(t, 2, len(lines))
		assert.True(t, strings.HasPrefix(lines[1], "   "), "data row must have 3-space indent")
	})

	t.Run("column alignment and no trailing padding on last column", func(t *testing.T) {
		headers := []string{"NAME", "AGE"}
		rows := [][]string{
			{"Alice", "30"},
			{"Bo", "7"},
		}
		got := FormatTable(headers, rows)
		lines := strings.Split(got, "\n")
		require.Equal(t, 3, len(lines))
		// Header: "▶  NAME   AGE" — NAME padded to 5 (max of "Alice"), AGE not padded.
		assert.Equal(t, "▶  NAME   AGE", lines[0])
		// Row 1: "   Alice  30" — Alice is 5 chars (matches width), no pad on last col.
		assert.Equal(t, "   Alice  30", lines[1])
		// Row 2: "   Bo     7" — Bo padded to 5, last col no pad.
		assert.Equal(t, "   Bo     7", lines[2])
	})

	t.Run("zero rows", func(t *testing.T) {
		got := FormatTable([]string{"COL"}, nil)
		assert.Equal(t, "▶  COL", got)
		assert.NotContains(t, got, "\n")
	})

	t.Run("zero rows empty slice", func(t *testing.T) {
		got := FormatTable([]string{"COL"}, [][]string{})
		assert.Equal(t, "▶  COL", got)
	})

	t.Run("one row", func(t *testing.T) {
		got := FormatTable([]string{"K", "V"}, [][]string{{"foo", "bar"}})
		lines := strings.Split(got, "\n")
		require.Equal(t, 2, len(lines))
		assert.Equal(t, "▶  K    V", lines[0])
		assert.Equal(t, "   foo  bar", lines[1])
	})

	t.Run("multiple rows", func(t *testing.T) {
		got := FormatTable([]string{"A", "B", "C"}, [][]string{
			{"1", "22", "3"},
			{"44", "5", "666"},
		})
		lines := strings.Split(got, "\n")
		require.Equal(t, 3, len(lines))
		// Widths: A=2(44), B=2(22), C=3(666). Last col not padded.
		assert.Equal(t, "▶  A   B   C", lines[0])
		assert.Equal(t, "   1   22  3", lines[1])
		assert.Equal(t, "   44  5   666", lines[2])
	})

	t.Run("empty cells", func(t *testing.T) {
		got := FormatTable([]string{"H1", "H2"}, [][]string{
			{"", "val"},
			{"x", ""},
		})
		lines := strings.Split(got, "\n")
		require.Equal(t, 3, len(lines))
		assert.Equal(t, "▶  H1  H2", lines[0])
		assert.Equal(t, "       val", lines[1])
		assert.Equal(t, "   x   ", lines[2])
	})

	t.Run("row longer than headers is clamped", func(t *testing.T) {
		// Must not panic — extra cells are skipped.
		got := FormatTable([]string{"A"}, [][]string{{"x", "extra", "more"}})
		lines := strings.Split(got, "\n")
		require.Equal(t, 2, len(lines))
		assert.Equal(t, "▶  A", lines[0])
		assert.Equal(t, "   x", lines[1])
	})
}

// --- Identity tool tests ---

func TestFormatOutput_Identity_Whoami(t *testing.T) {
	result := `{"name":"Alice","handle":"alice","kind":"human","email":"alice@example.com","github":"alice-gh","agent":".claude/agents/alice.md","personality":"friendly","writing_style":"concise","talents":["go","testing"]}`
	payload := makeToolPayload("identity", "whoami", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "PostToolUse", r.HookSpecificOutput.HookEventName)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Alice (alice) — human")
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Email: alice@example.com")
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Talents: go, testing")
	// Context should contain the same formatted field list, not raw JSON.
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Alice (alice) — human")
	assert.Contains(t, ctx, "Email: alice@example.com")
	assert.Contains(t, ctx, "GitHub: alice-gh")
	assert.Contains(t, ctx, "Agent: .claude/agents/alice.md")
	assert.Contains(t, ctx, "Personality: friendly")
	assert.Contains(t, ctx, "Writing: concise")
	assert.Contains(t, ctx, "Talents: go, testing")
	assert.NotContains(t, ctx, `"name"`) // Must not be raw JSON
}

func TestFormatOutput_Identity_List(t *testing.T) {
	result := `[{"handle":"alice","name":"Alice","kind":"human","personality":"friendly"},{"handle":"bob","name":"Bob","kind":"agent","personality":""}]`
	payload := makeToolPayload("identity", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	// Panel: count summary.
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "2 identities")
	// Context: columnar table with headers.
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "HANDLE")
	assert.Contains(t, ctx, "NAME")
	assert.Contains(t, ctx, "KIND")
	assert.Contains(t, ctx, "PERSONALITY")
	assert.NotContains(t, ctx, "ACTIVE")
	// Data rows with alignment.
	assert.Contains(t, ctx, "alice")
	assert.Contains(t, ctx, "bob")
}

func TestFormatOutput_Identity_List_Singular(t *testing.T) {
	result := `[{"handle":"alice","name":"Alice","kind":"human","personality":"friendly"}]`
	payload := makeToolPayload("identity", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "1 identity")
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
	// Context should be the formatted field list, not raw JSON.
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Alice (alice) — human")
	assert.Contains(t, ctx, "Email: alice@example.com")
	assert.NotContains(t, ctx, `"name"`)
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
	assert.Equal(t, "2 talents", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "SLUG")
	assert.Contains(t, ctx, "go")
	assert.Contains(t, ctx, "testing")
}

func TestFormatOutput_Talent_List_Rich(t *testing.T) {
	result := `{"attributes":[{"slug":"go"},{"slug":"testing"},{"slug":"design"}]}`
	payload := makeToolPayload("talent", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "3 talents", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "▶")
	assert.Contains(t, ctx, "SLUG")
	assert.Contains(t, ctx, "go")
	assert.Contains(t, ctx, "testing")
	assert.Contains(t, ctx, "design")
}

func TestFormatOutput_Talent_List_Empty_Rich(t *testing.T) {
	result := `{"attributes":[]}`
	payload := makeToolPayload("talent", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "0 talents", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Equal(t, "(none)", r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Talent_List_Singular(t *testing.T) {
	result := `{"attributes":[{"slug":"go"}]}`
	payload := makeToolPayload("talent", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "1 talent", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_Personality_List_Noun(t *testing.T) {
	result := `{"attributes":[{"slug":"friendly"},{"slug":"formal"}]}`
	payload := makeToolPayload("personality", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "2 personalities", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_WritingStyle_List_Noun(t *testing.T) {
	result := `{"attributes":[{"slug":"concise"}]}`
	payload := makeToolPayload("writing_style", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "1 writing style", r.HookSpecificOutput.UpdatedMCPToolOutput)
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
	assert.Equal(t, "1 participant (session abc)", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "AGENT_ID")
	assert.Contains(t, ctx, "user1")
}

func TestFormatOutput_Session_Roster_Rich(t *testing.T) {
	result := `{"session":"abc123","participants":[{"agent_id":"jfreeman","persona":"jfreeman","agent_type":"human"},{"agent_id":"37569","persona":"claude","parent":"jfreeman","agent_type":"cli"}]}`
	payload := makeToolPayload("session", "roster", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "2 participants (session abc123)", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "▶")
	assert.Contains(t, ctx, "AGENT_ID")
	assert.Contains(t, ctx, "PERSONA")
	assert.Contains(t, ctx, "PARENT")
	assert.Contains(t, ctx, "TYPE")
	assert.Contains(t, ctx, "jfreeman")
	assert.Contains(t, ctx, "37569")
	assert.Contains(t, ctx, "claude")
	assert.Contains(t, ctx, "human")
	assert.Contains(t, ctx, "cli")
}

func TestFormatOutput_Session_Roster_Empty(t *testing.T) {
	result := `{"session":"abc","participants":[]}`
	payload := makeToolPayload("session", "roster", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "0 participants (session abc)", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Equal(t, "(none)", r.HookSpecificOutput.AdditionalContext)
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
	assert.Equal(t, "1 key", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "KEY")
	assert.Contains(t, ctx, "VALUE")
	assert.Contains(t, ctx, "tty")
	assert.Contains(t, ctx, "s001")
}

func TestFormatOutput_Ext_Get_Rich(t *testing.T) {
	result := `{"provider":"elevenlabs","voice_id":"helmut"}`
	payload := makeToolPayload("ext", "get", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "2 keys", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "▶")
	assert.Contains(t, ctx, "KEY")
	assert.Contains(t, ctx, "VALUE")
	assert.Contains(t, ctx, "provider")
	assert.Contains(t, ctx, "elevenlabs")
	assert.Contains(t, ctx, "voice_id")
	assert.Contains(t, ctx, "helmut")
}

func TestFormatOutput_Ext_Get_Empty(t *testing.T) {
	result := `{}`
	payload := makeToolPayload("ext", "get", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "0 keys", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Equal(t, "(none)", r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Ext_List_Rich(t *testing.T) {
	result := `["biff","vox"]`
	payload := makeToolPayload("ext", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "2 namespaces", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "▶")
	assert.Contains(t, ctx, "NAMESPACE")
	assert.Contains(t, ctx, "biff")
	assert.Contains(t, ctx, "vox")
}

func TestFormatOutput_Ext_List_Empty(t *testing.T) {
	result := `[]`
	payload := makeToolPayload("ext", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "0 namespaces", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Equal(t, "(none)", r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Ext_List_Singular(t *testing.T) {
	result := `["biff"]`
	payload := makeToolPayload("ext", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "1 namespace", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_Ext_Set(t *testing.T) {
	result := `"set alice/biff/tty"`
	payload := makeToolPayload("ext", "set", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "set alice/biff/tty")
}

// --- Team tool tests ---

func TestFormatOutput_Team_List(t *testing.T) {
	result := `["engineering","website"]`
	payload := makeToolPayload("team", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "2 teams", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "TEAM")
	assert.Contains(t, ctx, "engineering")
	assert.Contains(t, ctx, "website")
}

func TestFormatOutput_Team_List_Empty(t *testing.T) {
	result := `[]`
	payload := makeToolPayload("team", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "0 teams", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Equal(t, "(none)", r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Team_Show(t *testing.T) {
	result := `{"name":"engineering","repositories":["punt-labs/ethos","punt-labs/biff"],"members":[{"identity":"alice","role":"lead-engineer"},{"identity":"claude","role":"engineer"}],"collaborations":[{"from":"engineer","to":"lead-engineer","type":"reports_to"}]}`
	payload := makeToolPayload("team", "show", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "engineering (2 members)", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Name: engineering")
	assert.Contains(t, ctx, "Repositories: punt-labs/ethos, punt-labs/biff")
	assert.Contains(t, ctx, "IDENTITY")
	assert.Contains(t, ctx, "ROLE")
	assert.Contains(t, ctx, "alice")
	assert.Contains(t, ctx, "lead-engineer")
	assert.Contains(t, ctx, "claude")
	assert.Contains(t, ctx, "engineer")
	assert.Contains(t, ctx, "FROM")
	assert.Contains(t, ctx, "TO")
	assert.Contains(t, ctx, "TYPE")
	assert.Contains(t, ctx, "reports_to")
}

func TestFormatOutput_Team_ForRepo(t *testing.T) {
	result := `[{"name":"engineering","repositories":["punt-labs/ethos"],"members":[{"identity":"alice","role":"lead"}]}]`
	payload := makeToolPayload("team", "for_repo", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "1 team for repo", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Name: engineering")
	assert.Contains(t, ctx, "Repositories: punt-labs/ethos")
	assert.Contains(t, ctx, "alice")
	assert.Contains(t, ctx, "lead")
}

func TestFormatOutput_Team_ForRepo_Empty(t *testing.T) {
	result := `[]`
	payload := makeToolPayload("team", "for_repo", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "no teams found", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Equal(t, "(none)", r.HookSpecificOutput.AdditionalContext)
}

// --- Role tool tests ---

func TestFormatOutput_Role_List(t *testing.T) {
	result := `["engineer","lead-engineer","product-manager"]`
	payload := makeToolPayload("role", "list", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "3 roles", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "ROLE")
	assert.Contains(t, ctx, "engineer")
	assert.Contains(t, ctx, "lead-engineer")
	assert.Contains(t, ctx, "product-manager")
}

func TestFormatOutput_Role_Show(t *testing.T) {
	result := `{"name":"lead-engineer","responsibilities":["code review","architecture decisions"],"permissions":["merge","deploy"]}`
	payload := makeToolPayload("role", "show", result)

	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "lead-engineer", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Name: lead-engineer")
	assert.Contains(t, ctx, "Responsibilities:")
	assert.Contains(t, ctx, "- code review")
	assert.Contains(t, ctx, "- architecture decisions")
	assert.Contains(t, ctx, "Permissions:")
	assert.Contains(t, ctx, "- merge")
	assert.Contains(t, ctx, "- deploy")
}

// --- Mission tool tests ---

// missionContractJSON is a full contract as returned by create/show.
// Bead lives at inputs.bead — the single source of truth. An earlier
// draft carried a top-level "bead" too, but that duplication was
// removed when Copilot caught the divergence risk.
const missionContractJSON = `{
  "mission_id": "m-2026-04-07-001",
  "status": "open",
  "created_at": "2026-04-07T21:30:00Z",
  "updated_at": "2026-04-07T21:30:00Z",
  "leader": "claude",
  "worker": "bwk",
  "evaluator": {
    "handle": "djb",
    "pinned_at": "2026-04-07T21:30:00Z"
  },
  "inputs": {"bead": "ethos-07m.5"},
  "write_set": ["internal/mission/", "cmd/ethos/mission.go"],
  "tools": ["Read", "Write"],
  "success_criteria": ["make check passes"],
  "budget": {"rounds": 3, "reflection_after_each": true}
}`

// missionContractJSONClosed is a contract that was closed — exercises
// the closed_at rendering path.
const missionContractJSONClosed = `{
  "mission_id": "m-2026-04-07-002",
  "status": "closed",
  "created_at": "2026-04-07T21:30:00Z",
  "updated_at": "2026-04-07T22:00:00Z",
  "closed_at": "2026-04-07T22:00:00Z",
  "leader": "claude",
  "worker": "bwk",
  "evaluator": {
    "handle": "djb",
    "pinned_at": "2026-04-07T21:30:00Z"
  },
  "inputs": {},
  "write_set": ["internal/mission/"],
  "success_criteria": ["make check passes"],
  "budget": {"rounds": 3, "reflection_after_each": true}
}`

// missionContractJSONMinimal is a contract with no optional fields:
// no bead, no tools, no inputs, no closed_at. Exercises the conditional
// rendering branches.
const missionContractJSONMinimal = `{
  "mission_id": "m-2026-04-07-003",
  "status": "open",
  "created_at": "2026-04-07T21:30:00Z",
  "updated_at": "2026-04-07T21:30:00Z",
  "leader": "claude",
  "worker": "bwk",
  "evaluator": {
    "handle": "djb",
    "pinned_at": "2026-04-07T21:30:00Z"
  },
  "inputs": {},
  "write_set": ["internal/mission/"],
  "success_criteria": ["make check passes"],
  "budget": {"rounds": 3, "reflection_after_each": true}
}`

// The tabwriter pads field labels to the longest label ("Evaluator:" =
// 10 chars) plus 2 columns of padding, so "Mission:" (8 chars) becomes
// "Mission:" + 4 spaces, "Status:" (7 chars) becomes "Status:" + 5
// spaces, etc. Tests use these helpers so the magic widths live in
// one place.
func missionLabel(t *testing.T, label string) string {
	t.Helper()
	const widestLabel = len("Evaluator:") // 10
	const padding = 2
	pad := widestLabel + padding - len(label)
	return label + strings.Repeat(" ", pad)
}

func TestFormatOutput_Mission_Create(t *testing.T) {
	payload := makeToolPayload("mission", "create", missionContractJSON)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Created m-2026-04-07-001", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, missionLabel(t, "Mission:")+"m-2026-04-07-001")
	assert.Contains(t, ctx, missionLabel(t, "Status:")+"open")
	assert.Contains(t, ctx, missionLabel(t, "Leader:")+"claude")
	assert.Contains(t, ctx, missionLabel(t, "Worker:")+"bwk")
	assert.Contains(t, ctx, missionLabel(t, "Evaluator:")+"djb (pinned ")
	assert.Contains(t, ctx, missionLabel(t, "Budget:")+"3 round(s), reflection_after_each=true")
	// Bead lives at inputs.bead — rendered inside the Inputs
	// section, not as a header row. No top-level Bead: field any more.
	assert.Contains(t, ctx, "bead: ethos-07m.5")
	assert.NotContains(t, ctx, missionLabel(t, "Bead:")+"ethos-07m.5")
	// Created uses local-time formatting; the date is 7 Apr UTC which
	// renders as April in every timezone (TZ shifts can move the day
	// of month but not the month within a 24h window).
	assert.Contains(t, ctx, "Apr")
	assert.Contains(t, ctx, "Write set:")
	assert.Contains(t, ctx, "- internal/mission/")
	assert.Contains(t, ctx, "Tools:")
	assert.Contains(t, ctx, "- Read")
	assert.Contains(t, ctx, "- Write")
	assert.Contains(t, ctx, "Success criteria:")
	assert.Contains(t, ctx, "- make check passes")
	assert.NotContains(t, ctx, `"mission_id":`) // Not raw JSON
	// Raw RFC3339 timestamp should not appear — FormatLocalTime
	// converts it.
	assert.NotContains(t, ctx, "2026-04-07T21:30:00Z")
}

func TestFormatOutput_Mission_Create_MissingMissionID(t *testing.T) {
	// The early guard short-circuits with a clear malformed banner
	// rather than rendering a partial card that looks legitimate.
	payload := makeToolPayload("mission", "create", `{"status":"open"}`)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Created (malformed contract)", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Contains(t, r.HookSpecificOutput.AdditionalContext, "malformed contract")
}

func TestFormatOutput_Mission_Show(t *testing.T) {
	payload := makeToolPayload("mission", "show", missionContractJSON)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "m-2026-04-07-001 (open)", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, missionLabel(t, "Mission:")+"m-2026-04-07-001")
	assert.Contains(t, ctx, "Success criteria:")
	assert.Contains(t, ctx, "- make check passes")
}

func TestFormatOutput_Mission_Show_Closed(t *testing.T) {
	// Closed contract — verifies the closed_at row appears in the
	// header block via FormatLocalTime.
	payload := makeToolPayload("mission", "show", missionContractJSONClosed)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "m-2026-04-07-002 (closed)", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, missionLabel(t, "Status:")+"closed")
	// Closed: row must be present with a formatted timestamp.
	assert.Regexp(t, `Closed:\s+\w+ Apr`, ctx)
}

func TestFormatOutput_Mission_Show_NoOptionalFields(t *testing.T) {
	// Minimal contract — no bead, no tools, no closed_at, no
	// inputs.bead. Optional sections must be skipped, not emit empty
	// headers.
	payload := makeToolPayload("mission", "show", missionContractJSONMinimal)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, missionLabel(t, "Mission:")+"m-2026-04-07-003")
	assert.NotContains(t, ctx, "Bead:")
	assert.NotContains(t, ctx, "Closed:")
	assert.NotContains(t, ctx, "Tools:")
}

func TestFormatOutput_Mission_Show_Malformed(t *testing.T) {
	payload := makeToolPayload("mission", "show", "not-json")
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "not-json")
}

func TestFormatOutput_Mission_List(t *testing.T) {
	result := `[
		{"mission_id":"m-2026-04-07-001","status":"open","leader":"claude","worker":"bwk","evaluator":"djb","created_at":"2026-04-07T21:30:00Z"},
		{"mission_id":"m-2026-04-07-002","status":"closed","leader":"claude","worker":"rmh","evaluator":"djb","created_at":"2026-04-07T22:00:00Z"}
	]`
	payload := makeToolPayload("mission", "list", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "2 missions", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "MISSION")
	assert.Contains(t, ctx, "STATUS")
	assert.Contains(t, ctx, "LEADER")
	assert.Contains(t, ctx, "WORKER")
	assert.Contains(t, ctx, "EVALUATOR")
	assert.Contains(t, ctx, "m-2026-04-07-001")
	assert.Contains(t, ctx, "m-2026-04-07-002")
	assert.Contains(t, ctx, "open")
	assert.Contains(t, ctx, "closed")
	assert.Contains(t, ctx, "bwk")
	assert.Contains(t, ctx, "rmh")
}

func TestFormatOutput_Mission_List_Singular(t *testing.T) {
	result := `[{"mission_id":"m-2026-04-07-001","status":"open","leader":"claude","worker":"bwk","evaluator":"djb","created_at":"2026-04-07T21:30:00Z"}]`
	payload := makeToolPayload("mission", "list", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "1 mission", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_Mission_List_Empty(t *testing.T) {
	result := `[]`
	payload := makeToolPayload("mission", "list", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "0 missions", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Equal(t, "(none)", r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Mission_Close(t *testing.T) {
	result := `{"mission_id":"m-2026-04-07-001","status":"closed"}`
	payload := makeToolPayload("mission", "close", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Closed m-2026-04-07-001 as closed", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_Mission_Close_Failed(t *testing.T) {
	result := `{"mission_id":"m-2026-04-07-001","status":"failed"}`
	payload := makeToolPayload("mission", "close", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Closed m-2026-04-07-001 as failed", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_Mission_Close_Escalated(t *testing.T) {
	result := `{"mission_id":"m-2026-04-07-001","status":"escalated"}`
	payload := makeToolPayload("mission", "close", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Closed m-2026-04-07-001 as escalated", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

// --- 3.4: reflect, advance, reflections formatter tests ---

func TestFormatOutput_Mission_Reflect(t *testing.T) {
	result := `{"mission_id":"m-2026-04-07-001","round":1,"recommendation":"continue","created_at":"2026-04-08T08:00:00Z"}`
	payload := makeToolPayload("mission", "reflect", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	assert.Equal(t, "Reflected m-2026-04-07-001 round 1 (continue)", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_Mission_Reflect_Stop(t *testing.T) {
	result := `{"mission_id":"m-2026-04-07-001","round":2,"recommendation":"stop","created_at":"2026-04-08T08:00:00Z"}`
	payload := makeToolPayload("mission", "reflect", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	assert.Equal(t, "Reflected m-2026-04-07-001 round 2 (stop)", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_Mission_Advance(t *testing.T) {
	result := `{"mission_id":"m-2026-04-07-001","current_round":2}`
	payload := makeToolPayload("mission", "advance", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	assert.Equal(t, "Advanced m-2026-04-07-001 to round 2", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

func TestFormatOutput_Mission_Reflections_Empty(t *testing.T) {
	payload := makeToolPayload("mission", "reflections", `[]`)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	assert.Equal(t, "0 reflections", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Equal(t, "(none)", r.HookSpecificOutput.AdditionalContext)
}

func TestFormatOutput_Mission_Reflections_Singular(t *testing.T) {
	result := `[{"round":1,"author":"claude","converging":true,"signals":["tests pass"],"recommendation":"continue","reason":"ok"}]`
	payload := makeToolPayload("mission", "reflections", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	assert.Equal(t, "1 reflection", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "round 1 (continue) by claude")
	assert.Contains(t, ctx, "tests pass")
	assert.Contains(t, ctx, "reason: ok")
}

func TestFormatOutput_Mission_Reflections_Multiple(t *testing.T) {
	result := `[
{"round":1,"author":"claude","converging":true,"signals":["a"],"recommendation":"continue","reason":""},
{"round":2,"author":"claude","converging":false,"signals":["b","c"],"recommendation":"pivot","reason":"new approach"}
]`
	payload := makeToolPayload("mission", "reflections", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	assert.Equal(t, "2 reflections", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "round 1 (continue)")
	assert.Contains(t, ctx, "round 2 (pivot)")
	assert.Contains(t, ctx, "converging=true")
	assert.Contains(t, ctx, "converging=false")
	assert.Contains(t, ctx, "reason: new approach")
}

// TestFormatOutput_MissionShow_RendersResults asserts the A1
// round-3 fix: formatMissionShow renders the round-by-round
// results array under the contract block. Round 2 added the
// `results` field to the show payload but the hook formatter
// dropped it silently; an agent reading `mission show` through
// the MCP hook saw the contract and no verdict — undoing H2 for
// the MCP surface. The walker mirrors formatMissionResults' bullet
// shape so the two rendering paths stay visually consistent.
func TestFormatOutput_MissionShow_RendersResults(t *testing.T) {
	result := `{
  "mission_id": "m-2026-04-07-001",
  "status": "closed",
  "created_at": "2026-04-07T21:30:00Z",
  "updated_at": "2026-04-07T22:00:00Z",
  "closed_at": "2026-04-07T22:00:00Z",
  "leader": "claude",
  "worker": "bwk",
  "evaluator": {"handle":"djb","pinned_at":"2026-04-07T21:30:00Z"},
  "inputs": {},
  "write_set": ["internal/mission/"],
  "success_criteria": ["make check passes"],
  "budget": {"rounds": 3, "reflection_after_each": true},
  "current_round": 1,
  "results": [
    {
      "mission": "m-2026-04-07-001",
      "round": 1,
      "author": "bwk",
      "verdict": "pass",
      "confidence": 0.95,
      "files_changed": [],
      "evidence": [{"name":"make check","status":"pass"}],
      "prose": "Round 1 delivered the typed result artifact."
    }
  ]
}`
	payload := makeToolPayload("mission", "show", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Results:",
		"mission show must render a Results: header, got: %s", ctx)
	assert.Contains(t, ctx, "round 1",
		"mission show must render the round number, got: %s", ctx)
	assert.Contains(t, ctx, "pass",
		"mission show must render the verdict, got: %s", ctx)
	assert.Contains(t, ctx, "bwk",
		"mission show must render the author, got: %s", ctx)
	assert.Contains(t, ctx, "confidence=0.95",
		"mission show must render the confidence, got: %s", ctx)
}

// TestFormatOutput_MissionShow_EmptyResultsSection asserts the A1
// round-3 fix empty-state: a show payload with `results: []`
// renders "Results:" + "(none)" so the operator sees the section
// exists and is empty. Without this, the formatter would either
// print nothing or print a dangling Results: header with no body.
func TestFormatOutput_MissionShow_EmptyResultsSection(t *testing.T) {
	result := `{
  "mission_id": "m-2026-04-07-001",
  "status": "open",
  "created_at": "2026-04-07T21:30:00Z",
  "updated_at": "2026-04-07T21:30:00Z",
  "leader": "claude",
  "worker": "bwk",
  "evaluator": {"handle":"djb","pinned_at":"2026-04-07T21:30:00Z"},
  "inputs": {},
  "write_set": ["internal/mission/"],
  "success_criteria": ["make check passes"],
  "budget": {"rounds": 3, "reflection_after_each": true},
  "current_round": 1,
  "results": []
}`
	payload := makeToolPayload("mission", "show", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Results:",
		"empty results must still render the Results: header, got: %s", ctx)
	assert.Contains(t, ctx, "(none)",
		"empty results must render the (none) marker, got: %s", ctx)
}

// TestFormatOutput_MissionShow_RendersWarnings asserts the D1
// round-3 fix: formatMissionShow surfaces a top-level `warnings`
// array in the show payload (emitted when LoadResults fails) so
// the MCP caller sees the corruption signal even through the hook
// formatter's re-rendering layer.
func TestFormatOutput_MissionShow_RendersWarnings(t *testing.T) {
	result := `{
  "mission_id": "m-2026-04-07-001",
  "status": "open",
  "created_at": "2026-04-07T21:30:00Z",
  "updated_at": "2026-04-07T21:30:00Z",
  "leader": "claude",
  "worker": "bwk",
  "evaluator": {"handle":"djb","pinned_at":"2026-04-07T21:30:00Z"},
  "inputs": {},
  "write_set": ["internal/mission/"],
  "success_criteria": ["make check passes"],
  "budget": {"rounds": 3, "reflection_after_each": true},
  "current_round": 1,
  "results": [],
  "warnings": ["loading results: invalid yaml in sibling file"]
}`
	payload := makeToolPayload("mission", "show", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Warnings:",
		"warnings must render a Warnings: header, got: %s", ctx)
	assert.Contains(t, ctx, "loading results",
		"warnings must carry the load-failure message, got: %s", ctx)
}

// TestFormatOutput_Mission_Show_RendersRound asserts that the show
// formatter surfaces the new "Round: N of M" line. The contract
// JSON gains a current_round field; the formatter must include it.
func TestFormatOutput_Mission_Show_RendersRound(t *testing.T) {
	result := `{
  "mission_id": "m-2026-04-07-001",
  "status": "open",
  "created_at": "2026-04-07T21:30:00Z",
  "updated_at": "2026-04-07T21:30:00Z",
  "leader": "claude",
  "worker": "bwk",
  "evaluator": {"handle":"djb","pinned_at":"2026-04-07T21:30:00Z"},
  "inputs": {},
  "write_set": ["internal/mission/"],
  "success_criteria": ["make check passes"],
  "budget": {"rounds": 3, "reflection_after_each": true},
  "current_round": 2
}`
	payload := makeToolPayload("mission", "show", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Round:")
	assert.Contains(t, ctx, "2 of 3")
}

func TestFormatOutput_Mission_UnknownMethod(t *testing.T) {
	payload := makeToolPayload("mission", "bogus", `{"mission_id":"x"}`)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	// Unknown method falls back to emitSimple + truncate.
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "mission_id")
}

// TestFormatOutput_MissionLog_RendersEventRows asserts that the
// Phase 3.7 log formatter renders every event as one bullet row
// with timestamp, type, actor, and a short details summary. The
// walker parallels formatMissionResults so the two rendering
// paths stay visually consistent.
func TestFormatOutput_MissionLog_RendersEventRows(t *testing.T) {
	result := `{
  "events": [
    {"ts":"2026-04-08T22:00:00Z","event":"create","actor":"claude","details":{"worker":"bwk","evaluator":"djb","bead":"ethos-07m.11"}},
    {"ts":"2026-04-08T22:00:05Z","event":"result","actor":"bwk","details":{"round":1,"verdict":"pass"}},
    {"ts":"2026-04-08T22:00:10Z","event":"close","actor":"claude","details":{"status":"closed","round":1,"verdict":"pass"}}
  ]
}`
	payload := makeToolPayload("mission", "log", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	assert.Equal(t, "3 events", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "create")
	assert.Contains(t, ctx, "by claude")
	assert.Contains(t, ctx, "worker=bwk")
	assert.Contains(t, ctx, "evaluator=djb")
	assert.Contains(t, ctx, "bead=ethos-07m.11")
	assert.Contains(t, ctx, "result")
	assert.Contains(t, ctx, "verdict=pass")
	assert.Contains(t, ctx, "close")
	assert.Contains(t, ctx, "status=closed")
}

// TestFormatOutput_MissionLog_Empty_RendersNone asserts an empty
// events array renders "(none)" so the operator sees a clean
// "no events yet" signal instead of a bare header.
func TestFormatOutput_MissionLog_Empty_RendersNone(t *testing.T) {
	result := `{"events": []}`
	payload := makeToolPayload("mission", "log", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	assert.Equal(t, "0 events", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Contains(t, r.HookSpecificOutput.AdditionalContext, "(none)")
}

// TestFormatOutput_MissionLog_Warnings asserts that a corrupt-line
// warnings slice surfaces under a Warnings section even when the
// events array is non-empty. Symmetric with the show formatter's
// D1 warnings handling.
func TestFormatOutput_MissionLog_Warnings(t *testing.T) {
	result := `{
  "events": [
    {"ts":"2026-04-08T22:00:00Z","event":"create","actor":"claude"}
  ],
  "warnings": ["line 2: decoding event: invalid character 'g'"]
}`
	payload := makeToolPayload("mission", "log", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Warnings:")
	assert.Contains(t, ctx, "line 2")
}

// TestFormatOutput_MissionLog_EmptyWithWarnings asserts that a
// fully-corrupt log (zero good events, one or more warnings) still
// surfaces the warnings so the operator does not see a bare
// "(none)" and assume the log is simply empty.
func TestFormatOutput_MissionLog_EmptyWithWarnings(t *testing.T) {
	result := `{
  "events": [],
  "warnings": ["line 1: decoding event: invalid"]
}`
	payload := makeToolPayload("mission", "log", result)
	out := runFormat(t, payload)
	r := parseFormatResult(t, out)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "(none)")
	assert.Contains(t, ctx, "Warnings:")
	assert.Contains(t, ctx, "line 1")
}

func TestFormatMissionTime(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string // empty means "must equal raw"
	}{
		{name: "valid RFC3339", raw: "2026-04-07T21:30:00Z"},
		{name: "invalid timestamp", raw: "yesterday", want: "yesterday"},
		{name: "empty string", raw: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatLocalTime(tt.raw)
			if tt.name == "valid RFC3339" {
				// Local-time formatted; can't assert exact value across
				// time zones, but it must contain the month name and a
				// 24h-style time.
				assert.Contains(t, got, "Apr")
				assert.Regexp(t, `\d{2}:\d{2}`, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
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

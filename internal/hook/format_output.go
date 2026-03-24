package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
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
	case "iam":
		return emitSimple(w, truncate(result, 200))
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
	if v := jsonString(result, "personality"); v != "" {
		lines = append(lines, "Personality: "+v)
	}
	if v := jsonString(result, "writing_style"); v != "" {
		lines = append(lines, "Writing: "+v)
	}
	if talents := jsonStringArray(result, "talents"); len(talents) > 0 {
		lines = append(lines, "Talents: "+strings.Join(talents, ", "))
	}

	return emit(w, strings.Join(lines, "\n"), result)
}

func formatIdentityList(w io.Writer, result string) error {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		return emitSimple(w, truncate(result, 200))
	}

	if len(entries) == 0 {
		return emit(w, "0 identities", "(none)")
	}

	// Count active entries.
	activeCount := 0
	for _, e := range entries {
		if active, _ := e["active"].(bool); active {
			activeCount++
		}
	}
	noun := "identities"
	if len(entries) == 1 {
		noun = "identity"
	}
	summary := fmt.Sprintf("%d %s, %d active", len(entries), noun, activeCount)

	// Build columnar table.
	headers := []string{"HANDLE", "NAME", "KIND", "PERSONALITY", "ACTIVE"}
	rows := make([][]string, len(entries))
	for i, e := range entries {
		handle, _ := e["handle"].(string)
		name, _ := e["name"].(string)
		kind, _ := e["kind"].(string)
		personality, _ := e["personality"].(string)
		active, _ := e["active"].(bool)
		marker := "-"
		if active {
			marker = "*"
		}
		if personality == "" {
			personality = "-"
		}
		rows[i] = []string{handle, name, kind, personality, marker}
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
			if len(cell) > widths[i] {
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
		lastCol := len(row) - 1
		for i, cell := range row {
			if i > 0 {
				buf.WriteString("  ")
			}
			if i == lastCol {
				buf.WriteString(cell)
			} else {
				buf.WriteString(fmt.Sprintf("%-*s", widths[i], cell))
			}
		}
	}

	return buf.String()
}

// --- Attribute tool formatters ---

func formatAttribute(w io.Writer, tool, method, result string) error {
	switch method {
	case "list":
		slugs := jsonNestedStringArray(result, "attributes", "slug")
		if len(slugs) == 0 {
			return emit(w, "(none)", result)
		}
		return emit(w, strings.Join(slugs, ", "), result)
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
		return emit(w, "Roster loaded", result)
	default:
		return emitSimple(w, truncate(result, 200))
	}
}

// --- Ext tool formatters ---

func formatExt(w io.Writer, method, result string) error {
	switch method {
	case "get", "list":
		return emit(w, "Extensions", result)
	default:
		return emitSimple(w, truncate(result, 200))
	}
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

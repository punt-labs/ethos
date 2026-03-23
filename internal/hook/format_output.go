package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// formatResult is the JSON output of the format-output hook.
type formatResult struct {
	HookSpecificOutput struct {
		HookEventName      string `json:"hookEventName"`
		UpdatedMCPToolOutput string `json:"updatedMCPToolOutput"`
		AdditionalContext  string `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

// HandleFormatOutput reads a PostToolUse hook payload from stdin and
// emits two-channel display output: a compact summary in
// updatedMCPToolOutput and full data in additionalContext.
func HandleFormatOutput(r io.Reader) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: format-output: %v\n", err)
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
		return emitSimple("error: " + errMsg)
	}

	// Extract method for consolidated tools.
	method := extractMethod(input)

	// Dispatch to tool-specific formatter.
	switch toolName {
	case "identity":
		return formatIdentity(method, result)
	case "talent", "personality", "writing_style":
		return formatAttribute(toolName, method, result)
	case "session":
		return formatSession(method, result)
	case "ext":
		return formatExt(method, result)
	default:
		return emitSimple(truncate(result, 200))
	}
}

// --- Identity tool formatters ---

func formatIdentity(method, result string) error {
	switch method {
	case "whoami", "get":
		return formatIdentityDetail(result)
	case "list":
		return formatIdentityList(result)
	case "create":
		name := jsonString(result, "name")
		if name == "" {
			name = "identity"
		}
		return emit("Created "+name, result)
	case "iam":
		return emitSimple(truncate(result, 200))
	default:
		return emitSimple(truncate(result, 200))
	}
}

func formatIdentityDetail(result string) error {
	name := jsonString(result, "name")
	if name == "" {
		return emitSimple(truncate(result, 200))
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

	return emit(strings.Join(lines, "\n"), result)
}

func formatIdentityList(result string) error {
	var entries []map[string]any
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		return emitSimple(truncate(result, 200))
	}

	var names []string
	for _, e := range entries {
		handle, _ := e["handle"].(string)
		name, _ := e["name"].(string)
		active, _ := e["active"].(bool)
		prefix := ""
		if active {
			prefix = "* "
		}
		names = append(names, fmt.Sprintf("%s%s (%s)", prefix, handle, name))
	}
	if len(names) == 0 {
		return emit("(none)", result)
	}
	return emit(strings.Join(names, ", "), result)
}

// --- Attribute tool formatters ---

func formatAttribute(tool, method, result string) error {
	switch method {
	case "list":
		slugs := jsonNestedStringArray(result, "attributes", "slug")
		if len(slugs) == 0 {
			return emit("(none)", result)
		}
		return emit(strings.Join(slugs, ", "), result)
	case "show":
		content := jsonString(result, "content")
		if content == "" {
			content = truncate(result, 200)
		}
		return emit(content, result)
	case "create":
		slug := jsonString(result, "slug")
		if slug == "" {
			slug = tool
		}
		return emit("Created "+slug, result)
	case "delete", "set", "add", "remove":
		return emitSimple(truncate(result, 200))
	default:
		return emitSimple(truncate(result, 200))
	}
}

// --- Session tool formatters ---

func formatSession(method, result string) error {
	switch method {
	case "roster":
		return emit("Roster loaded", result)
	default:
		return emitSimple(truncate(result, 200))
	}
}

// --- Ext tool formatters ---

func formatExt(method, result string) error {
	switch method {
	case "get", "list":
		return emit("Extensions", result)
	default:
		return emitSimple(truncate(result, 200))
	}
}

// --- Output helpers ---

func emit(summary, ctx string) error {
	r := formatResult{}
	r.HookSpecificOutput.HookEventName = "PostToolUse"
	r.HookSpecificOutput.UpdatedMCPToolOutput = summary
	r.HookSpecificOutput.AdditionalContext = ctx
	return json.NewEncoder(os.Stdout).Encode(r)
}

func emitSimple(summary string) error {
	r := formatResult{}
	r.HookSpecificOutput.HookEventName = "PostToolUse"
	r.HookSpecificOutput.UpdatedMCPToolOutput = summary
	return json.NewEncoder(os.Stdout).Encode(r)
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

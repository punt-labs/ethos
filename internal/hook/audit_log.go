package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// auditEntry is a single JSONL line in the session audit log.
type auditEntry struct {
	Ts               string `json:"ts"`
	Session          string `json:"session"`
	Tool             string `json:"tool"`
	ToolInputPreview string `json:"tool_input_preview"`
}

// HandleAuditLog appends a one-line JSONL entry to the session audit log.
// Called from the PostToolUse hook for every tool invocation. Never returns
// an error — audit logging is advisory, not a gate.
func HandleAuditLog(r io.Reader, sessionsDir string) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: audit-log: reading input: %v\n", err)
		return nil
	}

	sessionID, _ := input["session_id"].(string)
	if sessionID == "" {
		return nil
	}

	toolName, _ := input["tool_name"].(string)

	preview := toolInputPreview(input)

	entry := auditEntry{
		Ts:               time.Now().UTC().Format(time.RFC3339),
		Session:          sessionID,
		Tool:             toolName,
		ToolInputPreview: preview,
	}

	line, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: audit-log: marshaling entry: %v\n", err)
		return nil
	}
	line = append(line, '\n')

	path := filepath.Join(sessionsDir, filepath.Base(sessionID)+".audit.jsonl")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: audit-log: opening %s: %v\n", path, err)
		return nil
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: audit-log: writing %s: %v\n", path, err)
	}

	return nil
}

// toolInputPreview returns the first 200 characters of the JSON-serialized
// tool input. Returns empty string if tool_input is absent.
func toolInputPreview(input map[string]any) string {
	ti, ok := input["tool_input"]
	if !ok {
		return ""
	}
	data, err := json.Marshal(ti)
	if err != nil {
		return ""
	}
	s := string(data)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

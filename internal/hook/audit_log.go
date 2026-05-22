// Package hook implements the Claude Code lifecycle hook handlers.
//
// audit_log.go is the public entry point — HandleAuditLog — for the
// PostToolUse session audit log. It orchestrates the read of the
// hook payload, the build of an auditEntry, and the JSONL append.
// The struct, the canonical-JSON helpers, and the atomic-write
// contract each live in their own file under DES-052 extract_into
// discipline:
//
//   - audit_entry.go   — auditEntry struct, toolInputPreview,
//                        extractToolInput, hashToolInput
//   - audit_writer.go  — writeAuditEntry (open/write/fsync/close)
//   - audit_reader.go  — readAuditEntries (partial-line tolerant)
//
// KnownFields asymmetry (DES-054 phase 1): the contract YAML decoder
// in internal/mission/store.go uses KnownFields(true) to refuse
// silent feature loss across versions. The audit JSONL decoder is
// permissive — older readers (`ethos audit show`, vox, custom
// post-mortem tools) must keep parsing new logs even when fields are
// added. The asymmetry is intentional: contracts are a trust
// boundary; audit logs are operator-facing telemetry. See
// auditEntry's doc in audit_entry.go for the full rationale.
package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// HandleAuditLog appends one JSONL line to the session audit log.
// Called from the PostToolUse hook for every tool invocation. Never
// returns an error — audit logging is advisory, not a gate. Every
// failure path writes a warning to stderr and returns nil so a
// broken audit pipeline cannot block the tool call.
//
// The on-disk layout is the legacy single-tree shape:
// <sessionsDir>/<session-id>.audit.jsonl. The two-tree
// repo-vs-global storage layout introduced by DES-054 phase 1 is
// applied at the mission Store layer; the session audit hook still
// writes through this single path until phase 2 wires the
// repo-aware dispatcher.
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

	entry := auditEntry{
		Ts:               time.Now().UTC().Format(time.RFC3339),
		Session:          sessionID,
		Tool:             toolName,
		ToolInput:        extractToolInput(input),
		ToolInputHash:    hashToolInput(input),
		ToolInputPreview: toolInputPreview(input),
	}
	// Optional enrichment fields. Each is `omitempty` on the struct
	// so the absent value drops out of the JSONL line, preserving
	// wire shape compatibility with v3.11.0 readers.
	if v, ok := input["parent_session_id"].(string); ok {
		entry.ParentSession = v
	}
	if v, ok := input["agent_id"].(string); ok {
		entry.AgentID = v
	}
	if v, ok := input["agent_type"].(string); ok {
		entry.AgentType = v
	}
	if v, ok := input["delegation_id"].(string); ok {
		entry.DelegationID = v
	}
	if v, ok := input["parent_delegation"].(string); ok {
		entry.ParentDelegation = v
	}
	if v, ok := input["contract_id"].(string); ok {
		entry.ContractID = v
	}

	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: audit-log: creating %s: %v\n", sessionsDir, err)
		return nil
	}

	path := filepath.Join(sessionsDir, filepath.Base(sessionID)+".audit.jsonl")
	if err := writeAuditEntry(path, entry); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: audit-log: %v\n", err)
	}
	return nil
}

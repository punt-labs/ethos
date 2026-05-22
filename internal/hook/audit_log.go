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
//   - audit_paths.go   — repo-tree session directory resolution +
//                        legacy fallback for the read path
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
	"time"
)

// HandleAuditLog appends one JSONL line to the session audit log.
// Called from the PostToolUse hook for every tool invocation. Never
// returns an error — audit logging is advisory, not a gate. Every
// failure path writes a warning to stderr and returns nil so a
// broken audit pipeline cannot block the tool call.
//
// DES-054 phase 1 storage layout:
//
//   - When repoRoot is set, the write lands in
//     <repoRoot>/.ethos/sessions/<YYYY-MM-DD>-<session-id>/audit.jsonl.
//     The per-session directory is created on first write; subsequent
//     writes in the same session reuse the existing date directory
//     even if the wall clock has rolled over a day boundary.
//   - When repoRoot is empty, the write falls back to the legacy
//     <globalSessionsDir>/<session-id>.audit.jsonl shape so the hook
//     keeps working in non-repo contexts (e.g. cron tasks, ad-hoc
//     CLI sessions outside a git tree).
//
// The function never silently drops a write: an mkdir or open
// failure writes a warning to stderr but allows the tool call to
// proceed. A v3.11.0 reader (legacy fallback path only) continues to
// see logs from sessions whose wall-clock date matches today's UTC
// date — see resolveSessionDir's fallback behaviour and the
// readAuditEntriesForSession reader in audit_paths.go for the full
// migration contract.
func HandleAuditLog(r io.Reader, repoRoot, globalSessionsDir string) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: audit-log: reading input: %v\n", err)
		return nil
	}

	sessionID, _ := input["session_id"].(string)
	if sessionID == "" {
		return nil
	}

	now := time.Now().UTC()
	entry := buildAuditEntry(input, sessionID, now)

	path, err := resolveAuditWritePath(repoRoot, globalSessionsDir, sessionID, now)
	if err != nil {
		// Path resolution failed — no file is reachable, so the
		// sentinel cannot land either. Surface the original error
		// and the preview to stderr so an operator running -v can
		// still reconstruct the lost tool input from terminal scroll
		// or the systemd journal.
		fmt.Fprintf(os.Stderr,
			"ethos: audit-log: %v; lost session=%s tool=%s preview=%s\n",
			err, sessionID, entry.Tool, entry.ToolInputPreview)
		return nil
	}
	if writeErr := writeAuditEntry(path, entry); writeErr != nil {
		// The full entry did not persist. Always emit the entry's
		// reason and preview to stderr so the lost tool input is
		// recoverable even when the sentinel itself cannot land.
		fmt.Fprintf(os.Stderr,
			"ethos: audit-log: %v; lost session=%s tool=%s preview=%s\n",
			writeErr, sessionID, entry.Tool, entry.ToolInputPreview)
		// Attempt the in-band sentinel so `ethos audit show` reveals
		// the loss without an operator having to scrape stderr. A
		// fsync, ENOSPC, or partial-write failure that defeated the
		// full entry does not necessarily defeat a 100-byte sentinel;
		// when the file system has truly broken (the 0o000 directory
		// case) the sentinel write returns its own error and stderr
		// stays the only signal.
		if sentErr := emitAuditSentinel(path, sessionID, entry.Ts, writeErr.Error()); sentErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: audit-log: sentinel: %v\n", sentErr)
		}
	}
	return nil
}

// buildAuditEntry assembles the enriched auditEntry from the hook
// payload. Split from HandleAuditLog so the construction can be
// exercised under test without staging a writable directory and so
// the orchestrator stays focused on the I/O concerns.
func buildAuditEntry(input map[string]any, sessionID string, now time.Time) auditEntry {
	toolName, _ := input["tool_name"].(string)
	entry := auditEntry{
		Ts:               now.Format(time.RFC3339),
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
	return entry
}

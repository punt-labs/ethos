package hook

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AuditView is the exported, render-ready projection of an audit log
// entry. The on-disk auditEntry struct stays package-private; AuditView
// is the surface other packages — notably the CLI — consume.
//
// Field names + tags mirror the on-disk JSONL shape so re-marshaling
// produces a schema-equivalent line — same keys, same values. Go's
// json encoder does not preserve original key order or whitespace,
// so the result is not byte-for-byte identical (Copilot on PR #328).
type AuditView struct {
	Ts               string         `json:"ts"`
	Session          string         `json:"session"`
	ParentSession    string         `json:"parent_session,omitempty"`
	AgentID          string         `json:"agent_id,omitempty"`
	AgentType        string         `json:"agent_type,omitempty"`
	DelegationID     string         `json:"delegation_id,omitempty"`
	ParentDelegation string         `json:"parent_delegation,omitempty"`
	ContractID       string         `json:"contract_id,omitempty"`
	Tool             string         `json:"tool"`
	ToolInput        map[string]any `json:"tool_input,omitempty"`
	ToolInputHash    string         `json:"tool_input_hash,omitempty"`
	ToolInputPreview string         `json:"tool_input_preview,omitempty"`
}

// Summary returns a single-line human description of the entry. Prefers
// tool_input.file_path (the common case for Read/Edit/Write); falls
// back to tool_input_preview when the input has no file_path field.
// Used by the text render path in `ethos audit show --format text`.
func (v AuditView) Summary() string {
	if v.ToolInput != nil {
		if fp, ok := v.ToolInput["file_path"].(string); ok && fp != "" {
			return fp
		}
	}
	return v.ToolInputPreview
}

// toView projects the internal auditEntry into the exported shape.
// AuditView and auditEntry have identical field shapes; the type
// conversion is a no-op at runtime but keeps the package-private
// auditEntry from leaking out of the CLI render path.
func toView(e auditEntry) AuditView {
	return AuditView(e)
}

// QueryAuditByDelegation returns every audit entry whose DelegationID
// equals delegationID. It walks <repoRoot>/.punt-labs/ethos/sessions/<date>-<id>/
// audit.jsonl first, then falls back to the legacy
// <globalSessionsDir>/<id>.audit.jsonl shape for sessions that have no
// repo-tree counterpart (DES-052 reader fallback).
//
// Order: repo-tree sessions are visited in directory-listing order
// (lexicographic on the <date>-<id> name, which puts older sessions
// first). Entries within a session keep their on-disk order. Legacy
// sessions are appended after the repo-tree set in the same order.
//
// An empty delegationID returns (nil, nil) — every entry would match
// the wildcard, which is never the operator's intent.
//
// Missing directories are not an error: a fresh install has neither
// tree and the function returns an empty slice with nil error.
func QueryAuditByDelegation(repoRoot, globalSessionsDir, delegationID string) ([]AuditView, error) {
	if delegationID == "" {
		return nil, nil
	}

	var out []AuditView
	seenSessions := make(map[string]struct{})

	if repoRoot != "" {
		repoEntries, sessions, err := queryRepoTreeAudit(repoRoot, delegationID)
		if err != nil {
			return nil, fmt.Errorf("querying repo audit %s: %w", repoRoot, err)
		}
		for _, e := range repoEntries {
			out = append(out, toView(e))
		}
		for _, s := range sessions {
			seenSessions[s] = struct{}{}
		}
	}

	legacyEntries, err := queryLegacyAudit(globalSessionsDir, delegationID, seenSessions)
	if err != nil {
		return nil, fmt.Errorf("querying legacy audit %s: %w", globalSessionsDir, err)
	}
	for _, e := range legacyEntries {
		out = append(out, toView(e))
	}

	return out, nil
}

// queryRepoTreeAudit reads every repo session's union stream (sealed
// chunks + live tail, DES-058) and returns the entries matching the
// delegation, plus the set of session ids inspected. A missing tree is
// not an error.
func queryRepoTreeAudit(repoRoot, delegationID string) ([]auditEntry, []string, error) {
	sessions, err := listRepoSessions(repoRoot)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	var out []auditEntry
	for _, sessionID := range sessions {
		entries, err := readSessionAudit(repoRoot, sessionID, now)
		if err != nil {
			return nil, nil, fmt.Errorf("reading session %s: %w", sessionID, err)
		}
		out = append(out, filterByDelegation(entries, delegationID)...)
	}
	return out, sessions, nil
}

// queryLegacyAudit walks <globalSessionsDir>/<id>.audit.jsonl files
// for matching delegation entries. The `seen` argument is no longer
// used to skip sessions — during a migration window, a session can
// have a repo-tree dir AND legacy entries that have not yet been
// merged, so skipping legacy by session-id presence would hide
// matching entries (Bugbot MED on PR #328). Callers that want
// dedup should run `ethos audit migrate` first, which is the
// authoritative consolidation tool. A missing directory is not an
// error.
func queryLegacyAudit(globalSessionsDir, delegationID string, _ map[string]struct{}) ([]auditEntry, error) {
	if globalSessionsDir == "" {
		return nil, nil
	}
	files, err := os.ReadDir(globalSessionsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", globalSessionsDir, err)
	}

	var out []auditEntry
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		if !strings.HasSuffix(name, ".audit.jsonl") {
			continue
		}
		sessionID := strings.TrimSuffix(name, ".audit.jsonl")
		if sessionID == "" {
			continue
		}
		path := filepath.Join(globalSessionsDir, name)
		entries, err := readAuditEntries(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		out = append(out, filterByDelegation(entries, delegationID)...)
	}
	return out, nil
}

// sessionIDFromDir strips a leading "YYYY-MM-DD-" prefix from a
// repo-tree session directory name. Names that do not match the
// expected shape return the empty string.
func sessionIDFromDir(name string) string {
	// "YYYY-MM-DD-" is 11 characters; the id is everything after.
	const prefixLen = len("2006-01-02-")
	if len(name) <= prefixLen {
		return ""
	}
	prefix := name[:prefixLen]
	if prefix[4] != '-' || prefix[7] != '-' || prefix[10] != '-' {
		return ""
	}
	return name[prefixLen:]
}

// filterByDelegation returns the subset of entries whose DelegationID
// matches the target. Order is preserved.
func filterByDelegation(entries []auditEntry, delegationID string) []auditEntry {
	var out []auditEntry
	for _, e := range entries {
		if e.DelegationID == delegationID {
			out = append(out, e)
		}
	}
	return out
}

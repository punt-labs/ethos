package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/mission"
)

// auditEntry is a single JSONL line in the session audit log.
//
// DES-054 phase 1 enrichment: parent_session, agent_id, agent_type,
// delegation_id, parent_delegation, contract_id, and the full
// tool_input (not just the 200-char preview) are all carried so the
// audit trail can reconstruct the parent/child chain of every Agent
// spawn. tool_input_hash is the sha256 of the canonical-JSON encoding
// of tool_input — the hash gives an O(1) collision check for
// extract_into races (DES-052 Stat–Write residual) without re-reading
// the full input. tool_input_preview is retained as a 200-char human
// snippet for grep convenience.
//
// redacted marks a line whose tool_input lost content to the per-tool
// policy in audit_content.go — an outbound email reduced to its
// subject, or an address swept out of a prompt. It matches the marker
// the hand-redacted public-website lines carry, so a reader can tell
// "this call had no body" from "this body was removed".
//
// All new fields are `omitempty` so a v3.11.0 audit JSONL line
// (carrying only ts/session/tool/tool_input_preview) decodes cleanly
// under v3.12.0 — the new fields stay zero-valued and a permissive
// reader does not reject the line.
//
// KnownFields asymmetry: contract YAML is decoded with
// KnownFields(true) (see internal/mission/store.go DecodeContractStrict)
// to refuse silent feature loss when a v3.11.0 binary reads a v3.12.0
// contract. The audit JSONL stays permissive because operator tools
// like vox and `ethos audit show` must keep reading older logs
// indefinitely; rejecting an unknown field there would break the
// post-mortem path. See audit_log.go for the asymmetry note.
type auditEntry struct {
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
	Redacted         bool           `json:"redacted,omitempty"`
}

// previewLimit bounds the size of tool_input_preview. 200 chars is
// enough for grep-on-the-jsonl to pick out a Bash command or a Read
// path; the full input still lives on the entry under tool_input.
const previewLimit = 200

// toolInputPreview returns the first 200 characters of the
// canonical-JSON encoding of tool_input, with a "..." marker when the
// input was longer. Empty string when tool_input is absent or fails
// to encode — audit logging is advisory and a marshal failure must
// not block the write.
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
	if len(s) > previewLimit {
		return s[:previewLimit] + "..."
	}
	return s
}

// extractToolInput returns the tool_input value as a typed map so it
// can be persisted in full. Inputs that are not a JSON object (rare:
// some hooks pass a scalar) are returned as nil — the entry's
// ToolInput field stays empty and the JSON `omitempty` tag drops it,
// matching v3.11.0 wire shape for the trivial case.
func extractToolInput(input map[string]any) map[string]any {
	ti, ok := input["tool_input"]
	if !ok {
		return nil
	}
	m, ok := ti.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

// hashToolInput returns the hex sha256 of the canonical-JSON encoding
// of tool_input. encoding/json sorts map keys alphabetically, so two
// callers producing the same logical input produce the same hash
// regardless of map iteration order. Returns empty string when the
// input is absent or fails to encode.
//
// The hash is the post-hoc collision detector for DES-052's
// Stat–Write race: two delegations writing the same extract_into
// target produce two audit lines with the same tool_input_hash, and
// an operator running `ethos audit show --hash <hex>` finds the
// collision without parsing the prompts.
func hashToolInput(input map[string]any) string {
	ti, ok := input["tool_input"]
	if !ok {
		return ""
	}
	data, err := json.Marshal(ti)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// redactAbsolutePaths returns a copy of input with every string value
// rewritten so that machine-specific absolute path prefixes become
// portable tokens. $HOME/X becomes ~/X and repoRoot/X becomes
// <repo>/X. The repoRoot prefix is checked first so a repo nested
// inside HOME (the common case) gets the more specific token.
//
// The walker recurses into nested maps and slices. Bash command
// strings, file_path fields, and any other string value in tool_input
// are rewritten uniformly — the recursion handles them in place.
//
// Applied to new audit writes only; existing audit.jsonl lines on
// disk retain their original absolute paths. Operators who want
// clean history can rewrite the log via git filter-repo.
//
// Empty homeDir or repoRoot disables that substitution. The function
// never mutates the input map.
//
// The substitution itself lives on mission.PathRedactor. The Tier B
// delegation writer redacts prompt.md and record.yaml with the same
// type (internal/mission/delegation.go), so the audit lines and the
// delegation records cannot drift apart — one implementation, two
// call sites (ethos-ersr, ethos-n4np).
func redactAbsolutePaths(input map[string]any, homeDir, repoRoot string) map[string]any {
	return mission.PathRedactor{Home: homeDir, Repo: repoRoot}.Map(input)
}

// redactToolInput produces the tool_input that lands on disk: absolute
// paths rewritten to portable tokens, then the per-tool content policy
// from audit_content.go. The bool reports whether content was removed,
// which the caller stamps on the entry as the redacted marker.
//
// A usable home prefix is what makes path redaction possible; without
// one there is no way to tell the operator's home path from any other
// absolute path. mission.NewPathRedactor is the single arbiter of that
// — the delegation writer resolves its prefix through the same call,
// so the two write paths cannot disagree about what is redactable.
//
// That failure drops the input rather than storing it raw. The line
// keeps its timestamp, tool name, and delegation linkage — everything
// the audit trail is reconstructed from — and loses only the payload.
// Audit logging must never block a tool call (HandleAuditLog returns
// nil on every failure path), so failing closed here means dropping
// content, not refusing the call.
func redactToolInput(input map[string]any, toolName, repoRoot string) (map[string]any, bool) {
	r, err := mission.NewPathRedactor(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: audit-log: %v; storing tool=%s without its input\n", err, toolName)
		return nil, true
	}
	return redactSensitiveContent(toolName, r.Map(extractToolInput(input)))
}

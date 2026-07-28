package hook

import (
	"maps"
	"regexp"
	"slices"
	"strings"
)

// audit_content.go is the per-tool content redaction policy for the
// PostToolUse audit write path (ethos-ggtu).
//
// Path redaction (audit_entry.go) rewrites $HOME and repoRoot and
// nothing else, so message bodies and addresses passed straight
// through into the sealed, git-tracked record: a full outbound email
// body plus its recipient, and a recipient address inside a CronCreate
// prompt, both landed in a public repo.
//
// Two passes run here, both name-based and both fail-closed in the
// sense that matching more than necessary is the safe direction:
//
//  1. Sensitive tools reduce to a keep-list. Everything the list does
//     not name is replaced, so a field added to the tool later — a cc,
//     a reply-to, an attachment — is redacted the day it appears
//     rather than the day someone remembers to extend a deny-list.
//  2. Prompt-bearing fields get a PII sweep. Free-form prose authored
//     by or for a model is where an address turns up incidentally;
//     structured fields (a file_path, a glob) are left alone so a
//     non-sensitive tool's line stays byte-identical.
//
// Both passes run BEFORE tool_input_hash is computed, so the hash is
// over the stored form. That is the DES-052 cross-machine
// collision-detection invariant: two machines making the same logical
// call must produce the same hash, and a hash over a form that never
// reaches disk cannot be checked against anything.
//
// The tokens match the hand-redacted lines from the public-website
// PR #259 cleanup so one grep finds both the hand-redacted history and
// everything written since.
const (
	redactedValueToken = "[redacted]"
	redactedEmailToken = "[redacted-email]"
)

// sensitiveTools maps a tool's bare name to the tool_input keys that
// survive redaction. Every other key's value is replaced with
// redactedValueToken.
//
// The key is the bare name: an MCP tool arrives as
// mcp__plugin_beadle_email__send_email, and matching the trailing
// segment covers every namespace that exposes the same tool. A second
// email plugin therefore inherits the policy without an edit here.
//
// Keeping the subject is deliberate. It is what makes the audit line
// useful — an operator reconstructing a session needs to know an email
// went out and roughly what about — and it passes through the PII
// sweep below, so an address in the subject is still removed.
var sensitiveTools = map[string][]string{
	"send_email": {"subject"},
}

// promptBearingKeys names the tool_input keys carrying free-form prose
// rather than structured arguments. The PII sweep runs over strings
// reachable under these keys and nowhere else, so a Read's file_path
// or a Glob's pattern is untouched and its audit line is unchanged.
//
// Add a key here when a tool starts carrying model-authored text under
// a new name. Widening the set can only redact more; it cannot leak.
var promptBearingKeys = map[string]bool{
	"body":         true,
	"content":      true,
	"description":  true,
	"instructions": true,
	"message":      true,
	"notes":        true,
	"prompt":       true,
	"reason":       true,
	"subject":      true,
	"summary":      true,
	"text":         true,
}

// emailPattern matches an address with a lettered top-level domain.
// Requiring the TLD keeps the common false positives out: a Go module
// version (gopkg.in/yaml.v3@v3.0.1), a digest (image@sha256:...), and
// a bare user@host all fail the final \.[A-Za-z]{2,} anchor.
//
// Deliberately not RFC 5322. An address-shaped run of characters is
// what leaks; a parser that accepts every legal address and nothing
// else would be larger, slower, and no better at this job.
var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// redactSensitiveContent applies the policy to one tool call's
// already-path-redacted tool_input. It reports whether anything
// changed so the caller can stamp the redacted marker on the entry.
//
// Returns input unchanged — same map, no copy — when the tool is not
// sensitive and carries no address, so a non-sensitive line is
// byte-identical to what the pre-policy writer produced.
func redactSensitiveContent(toolName string, input map[string]any) (map[string]any, bool) {
	if input == nil {
		return nil, false
	}
	if keep, ok := sensitiveTools[bareToolName(toolName)]; ok {
		return reduceToKeepList(input, keep)
	}
	out, changed := sweepPII(input, false)
	m, _ := out.(map[string]any)
	return m, changed
}

// bareToolName returns the trailing segment of an MCP tool name.
// mcp__plugin_beadle_email__send_email becomes send_email; a built-in
// like Bash is already bare and passes through.
func bareToolName(toolName string) string {
	if i := strings.LastIndex(toolName, "__"); i >= 0 {
		return toolName[i+len("__"):]
	}
	return toolName
}

// reduceToKeepList returns a copy of input holding only the named keys
// at their swept values; every other key is present with its value
// replaced by redactedValueToken. The keys stay so the audit line
// still shows the shape of the call — which fields were supplied —
// without their contents.
//
// Non-kept values are replaced wholesale regardless of type. A nested
// map or slice can hide prose at any depth, and one rule that reduces
// everything is easier to verify than a type table that has to be
// right about each case.
//
// The bool reports whether anything was actually removed, so a call
// that supplied only kept fields is not mismarked as having lost
// content it never had.
func reduceToKeepList(input map[string]any, keep []string) (map[string]any, bool) {
	kept := make(map[string]bool, len(keep))
	for _, k := range keep {
		kept[k] = true
	}
	out := make(map[string]any, len(input))
	changed := false
	for k, v := range input {
		if !kept[k] {
			out[k] = redactedValueToken
			changed = true
			continue
		}
		swept, c := sweepPII(v, true)
		out[k] = swept
		changed = changed || c
	}
	return out, changed
}

// sweepPII walks v and replaces every address inside a prompt-bearing
// string with redactedEmailToken. inPrompt says whether the current
// position is already under a prompt-bearing key — once inside, the
// sweep applies at every depth below, so an address nested in a
// structured argument of a prompt is caught too.
//
// A container is cloned only once the first match inside it is found,
// so a line with nothing to redact is returned as the value that came
// in — no copy, and it marshals to exactly the bytes it did before
// this policy existed.
func sweepPII(v any, inPrompt bool) (any, bool) {
	switch x := v.(type) {
	case string:
		if !inPrompt {
			return x, false
		}
		s := emailPattern.ReplaceAllString(x, redactedEmailToken)
		return s, s != x
	case map[string]any:
		out, copied := x, false
		for k, vv := range x {
			swept, c := sweepPII(vv, inPrompt || promptBearingKeys[k])
			if !c {
				continue
			}
			if !copied {
				out, copied = maps.Clone(x), true
			}
			out[k] = swept
		}
		return out, copied
	case []any:
		out, copied := x, false
		for i, vv := range x {
			swept, c := sweepPII(vv, inPrompt)
			if !c {
				continue
			}
			if !copied {
				out, copied = slices.Clone(x), true
			}
			out[i] = swept
		}
		return out, copied
	default:
		return v, false
	}
}

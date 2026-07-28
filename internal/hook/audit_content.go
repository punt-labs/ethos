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
//
// The keep-list is fail-closed within an enrolled tool, but the
// enrolment itself is a list, so every tool on the surface that
// composes a message or names a person has to be on it. Enrolling only
// send_email left reply_message writing full bodies into the
// git-tracked log, which is the same defect one name over. create_draft
// is not on beadle's surface today; it is the conventional name for a
// third compose path, and enrolling a tool that does not exist costs
// nothing while enrolling it late costs a leak.
//
// The contact tools are enrolled with an empty keep-list. An address
// book is a list of real people: add_contact carries a legal name, an
// address, and nicknames; remove_contact carries the name. None of that
// belongs in a git-tracked file, and "add_contact was called" is the
// whole of the audit value — the keys stay, so the line still shows it.
//
// The rest of the beadle surface reads or files mail (list, read,
// search, move, mark, download) and composes nothing.
var sensitiveTools = map[string][]string{
	"send_email":     {"subject"},
	"reply_message":  {"subject"},
	"create_draft":   {"subject"},
	"add_contact":    {},
	"remove_contact": {},
}

// recipientKeys names structured tool_input keys that address a
// message rather than describe it. They are swept on every tool,
// enrolled or not, so a mail tool nobody has enrolled yet still cannot
// put a recipient into the log.
//
// A value here is reduced whole once it contains an address, not
// patched at the match. A recipient field holds "Display Name <addr>",
// and the display name identifies the person as surely as the address
// does; leaving it behind would redact the machine-readable half of a
// leak and commit the readable half.
//
// The trigger is still the address, because these key names are not
// unique to mail: SendMessage carries recipient="bwk-audit-seal-r2" and
// biff write carries to="claude:tty16", both agent names an operator
// needs when reconstructing a session. No address in the value means no
// leak to close, and the line keeps its audit value.
var recipientKeys = map[string]bool{
	"bcc":        true,
	"cc":         true,
	"email":      true,
	"from":       true,
	"recipient":  true,
	"recipients": true,
	"reply_to":   true,
	"to":         true,
}

// promptBearingKeys names the tool_input keys carrying free-form prose
// rather than structured arguments. The PII sweep runs over strings
// reachable under these keys and nowhere else, so a Read's file_path
// or a Glob's pattern is untouched and its audit line is unchanged.
//
// Add a key here when a tool starts carrying model-authored text under
// a new name. Widening the set can only redact more; it cannot leak.
//
// query earns its place from find_contact, which looks a person up by
// "name, email, or alias" — an operator asking for a contact by address
// wrote that address into the log under a key no other pass covered.
var promptBearingKeys = map[string]bool{
	"body":         true,
	"content":      true,
	"description":  true,
	"instructions": true,
	"message":      true,
	"notes":        true,
	"prompt":       true,
	"query":        true,
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
	out, changed := sweepPII(input, sweepOff)
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
		swept, c := sweepPII(v, sweepPrompt)
		out[k] = swept
		changed = changed || c
	}
	return out, changed
}

// sweepMode says how a string at the current position is treated.
// A key selects the mode for everything beneath it, so an address
// nested in a structured argument of a prompt, or one element of a to:
// list, is reached at the same mode as the key that opened it.
type sweepMode int

const (
	// sweepOff is a structured argument: a file_path, a glob, a cron
	// expression. Left verbatim so a non-sensitive line keeps its bytes.
	sweepOff sweepMode = iota
	// sweepPrompt is prose. Addresses inside it are replaced at the
	// match; the surrounding sentence is what makes the line useful.
	sweepPrompt
	// sweepAddress is a recipient field. A string holding an address is
	// replaced whole — see recipientKeys.
	sweepAddress
)

// modeFor returns the mode for a value reached under key k at the
// current mode. A recipient key always wins, including inside a
// prompt, because the widest reduction is the safe one. Otherwise a
// mode already in force propagates down: prose stays prose however
// deeply it is nested.
func modeFor(mode sweepMode, k string) sweepMode {
	switch {
	case recipientKeys[k]:
		return sweepAddress
	case mode != sweepOff:
		return mode
	case promptBearingKeys[k]:
		return sweepPrompt
	default:
		return sweepOff
	}
}

// sweepPII walks v and removes addresses according to mode.
//
// A container is cloned only once the first match inside it is found,
// so a line with nothing to redact is returned as the value that came
// in — no copy, and it marshals to exactly the bytes it did before
// this policy existed.
func sweepPII(v any, mode sweepMode) (any, bool) {
	switch x := v.(type) {
	case string:
		return sweepString(x, mode)
	case map[string]any:
		out, copied := x, false
		for k, vv := range x {
			swept, c := sweepPII(vv, modeFor(mode, k))
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
			swept, c := sweepPII(vv, mode)
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

// sweepString applies one mode to one string and reports whether it
// changed.
func sweepString(s string, mode sweepMode) (string, bool) {
	switch mode {
	case sweepPrompt:
		r := emailPattern.ReplaceAllString(s, redactedEmailToken)
		return r, r != s
	case sweepAddress:
		if emailPattern.MatchString(s) {
			return redactedValueToken, true
		}
		return s, false
	default:
		return s, false
	}
}

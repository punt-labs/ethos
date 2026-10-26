package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// guardAuditLine is the check at the sink of every audit appender. It takes
// the bytes of one finished line and returns a line that names no home, always
// in its canonical form: the decoded line re-marshalled, never the input bytes.
// Each string or key that leaks is replaced by a token; if the re-encoded line
// still leaks, its payload fields go; if it still does, or the line is not one
// JSON object, has keys that differ only in case, or would read back with
// another ts, it becomes a loss marker keeping only its ts and session. The audit
// writer cannot block a tool call, so it never refuses a line. A rewritten
// line is marked redacted, its tool_input_hash is recomputed over the stored
// tool_input (DES-054). The notice it returns names the rule, never the value,
// and the writer prints it only after the line is stored.
//
// Without a resolvable home directory every absolute path counts as a leak
// (mission.LeakRule).
func guardAuditLineNote(line []byte) ([]byte, string) {
	out, rule, marker := guardAuditLineFull(line)
	return out, auditGuardNotice(rule, marker)
}

// auditGuardNotice is the line the guard reports for a rewritten line, or ""
// when it left the line as it was. A writer prints it only after the line is
// stored.
func auditGuardNotice(rule string, marker bool) string {
	switch {
	case marker:
		return fmt.Sprintf("ethos: audit: a line was replaced by a loss marker (it %s)", rule)
	case rule != "":
		return fmt.Sprintf("ethos: audit: a line was rewritten before it was stored: it %s", rule)
	}
	return ""
}

// printNotices writes each non-empty notice to stderr, one line each.
func printNotices(notes []string) {
	for _, n := range notes {
		if n != "" {
			fmt.Fprintln(os.Stderr, n)
		}
	}
}

// guardAuditLineWhy is guardAuditLineNote returning the rule that forced a
// rewrite instead of a notice; the rule is "" when the line was clean.
//
// The bytes returned are always the re-marshalled decoded line, never the
// input bytes: whitespace, duplicate keys, escapes and key order of the input
// do not reach the file.
func guardAuditLineWhy(line []byte) ([]byte, string) {
	out, rule, _ := guardAuditLineFull(line)
	return out, rule
}

// guardAuditLineFull is guardAuditLineWhy also reporting whether the whole
// line became a loss marker.
func guardAuditLineFull(line []byte) ([]byte, string, bool) {
	out, rule, marker := guardAuditLineOnce(line)
	marker2 := func(kind, r string) ([]byte, string, bool) {
		return lossMarker(lineTS(line), sessionOf(line), kind), r, true
	}
	if marker {
		return out, rule, true
	}
	// The header a reader decodes from the stored line must be the one it
	// decoded from the input: seal selects a line by its ts and names the
	// chunk by it, and the rest of the header links the line to its session,
	// delegation and mission.
	got, gotOK := headerOf(out)
	if !gotOK {
		// A reader could not decode this line. Coerce it as a rewrite does (a
		// non-map tool_input becomes {}); if that is still undecodable, the
		// line becomes a loss marker.
		const why = "has a header a reader cannot decode"
		coerced, ok, variant := coerceHeader(out)
		if variant {
			return marker2("case-variant of a field ethos writes", caseVariantRule)
		}
		if !ok || lineTS(coerced) != lineTS(line) {
			return marker2("header a reader cannot decode", orRule(rule, why))
		}
		return coerced, orRule(rule, why), false
	}
	in, inOK := headerOf(line)
	if !inOK {
		if lineTS(line) != lineTS(out) {
			return marker2("ts that would change in re-encoding", orRule(rule, "would change its ts in re-encoding"))
		}
		return out, rule, false
	}
	if f := headerDiff(in, got, rule != ""); f != "" {
		return marker2("header field that would change in re-encoding", orRule(rule, "would change its "+f+" in re-encoding"))
	}
	return out, rule, false
}

// coerceHeader replaces a non-map tool_input with {}, marks the line redacted
// and recomputes its tool_input_hash, and reports whether the result decodes
// into auditEntry; the third result reports a case-variant of a field it
// would write, which makes the line a loss marker instead.
func coerceHeader(line []byte) ([]byte, bool, bool) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false, false
	}
	if caseVariantOfRewritten(m) {
		return nil, false, true
	}
	if v, ok := m["tool_input"]; ok {
		if _, isMap := v.(map[string]any); !isMap && v != nil {
			m["tool_input"] = map[string]any{}
		}
	}
	if _, ok := m["tool_input_hash"]; ok {
		m["tool_input_hash"] = hashToolInput(map[string]any{"tool_input": m["tool_input"]})
	}
	m["redacted"] = true
	out, err := json.Marshal(m)
	if err != nil || lineRule(out) != "" {
		return nil, false, false
	}
	if _, ok := headerOf(out); !ok {
		return nil, false, false
	}
	return out, true, false
}

// quoteUnlessLeak is k, passed through the prose redaction and quoted with %q,
// so neither a home nor a control character in it can reach a notice or a
// stand-in when k names a home.
func quoteUnlessLeak(k string) string {
	if mission.LeakRule(k) != "" {
		return "(its name names a home)"
	}
	return fmt.Sprintf("%q", redact.Prose(k))
}

// joinRules accumulates rules: a line that breaks two names both.
func joinRules(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; it " + b
}

// caseVariantRule is the rule for a line holding another spelling of a field
// the guard writes in its own spelling.
const caseVariantRule = "holds a case-variant of a field ethos rewrites"

// rewrittenFields are the top-level fields a rewrite sets in their own
// spelling. encoding/json would fold another case of one onto the same
// field, so a line holding one would read back differently after a rewrite,
// or a second guard would see a collision the first created.
var rewrittenFields = []string{"redacted", "tool_input", "tool_input_hash"}

// caseVariantOfRewritten reports whether a top-level key of m equals one of
// rewrittenFields case-insensitively but not exactly.
func caseVariantOfRewritten(m map[string]any) bool {
	for k := range m {
		for _, f := range rewrittenFields {
			if k != f && strings.EqualFold(k, f) {
				return true
			}
		}
	}
	return false
}

func orRule(rule, alt string) string {
	if rule != "" {
		return rule
	}
	return alt
}

// headerOf decodes the struct-decoded header of an audit line.
func headerOf(line []byte) (auditEntry, bool) {
	var e auditEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return auditEntry{}, false
	}
	return e, true
}

// headerDiff names the first header field that out does not hold as in did,
// or "". A value replaced by the redaction token is a redaction, not a change,
// and a rewritten line's tool_input_hash is recomputed on purpose.
func headerDiff(in, out auditEntry, rewritten bool) string {
	fields := []struct {
		name    string
		in, out string
	}{
		{"ts", in.Ts, out.Ts},
		{"session", in.Session, out.Session},
		{"parent_session", in.ParentSession, out.ParentSession},
		{"agent_id", in.AgentID, out.AgentID},
		{"agent_type", in.AgentType, out.AgentType},
		{"delegation_id", in.DelegationID, out.DelegationID},
		{"parent_delegation", in.ParentDelegation, out.ParentDelegation},
		{"contract_id", in.ContractID, out.ContractID},
		{"tool", in.Tool, out.Tool},
		{"tool_input_hash", in.ToolInputHash, out.ToolInputHash},
	}
	for _, f := range fields {
		if f.in == f.out || f.out == redactedPath {
			continue
		}
		if f.name == "tool_input_hash" && rewritten {
			continue
		}
		return f.name
	}
	if in.PathFormat != out.PathFormat {
		return "path_format"
	}
	return ""
}

// lineTS is the ts a reader decodes from a line (encoding/json matches the
// key without regard to case, the last match winning).
func lineTS(line []byte) string {
	var h struct {
		TS string `json:"ts"`
	}
	_ = json.Unmarshal(line, &h)
	return h.TS
}

func sessionOf(line []byte) string {
	var h struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(line, &h)
	return h.Session
}

// lossMarker is the line that stands for one that cannot be stored: it keeps
// the ts and session a reader took from the original, and says in kind, a
// phrase that names no value, why it could not be stored.
func lossMarker(ts, session, kind string) []byte {
	m := map[string]any{"audit_error": kind, "redacted": true}
	if ts != "" && mission.LeakRule(ts) == "" && !strings.ContainsRune(ts, '\uFFFD') {
		m["ts"] = ts
	}
	if session != "" && mission.LeakRule(session) == "" && !strings.ContainsRune(session, '\uFFFD') {
		m["session"] = session
	}
	out, _ := json.Marshal(m)
	return out
}

// caseCollision reports whether two top-level keys of an audit line fold to
// the same name the way encoding/json folds a key onto a struct field
// (Unicode simple folding, so ſession meets session and K meets k). Only the
// top level is decoded into a struct; tool_input is a map, where keys that
// differ in case (HTTP headers) are distinct and legitimate.
func caseCollision(m map[string]any) bool {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if strings.EqualFold(keys[i], keys[j]) {
				return true
			}
		}
	}
	return false
}

func guardAuditLineOnce(line []byte) ([]byte, string, bool) {
	// Checked before decoding: a decoder would turn invalid bytes or an
	// unpaired surrogate escape into U+FFFD and the line would be stored with
	// text it never held.
	if r := mission.InvalidText(line); r != "" {
		return lossMarker(lineTS(line), sessionOf(line), "invalid text"), r, true
	}
	rule := lineRule(line)
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		if rule == "" {
			rule = "is not a JSON object"
		}
		return lossMarker(lineTS(line), sessionOf(line), "not a JSON object"), rule, true
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if rule == "" {
			rule = "is not a single JSON object"
		}
		return lossMarker(lineTS(line), sessionOf(line), "not a single JSON object"), rule, true
	}
	// Keys that differ only in case let re-marshalling change which one a
	// reader takes; such a line is not stored.
	if caseCollision(m) {
		return lossMarker(lineTS(line), sessionOf(line), "keys that differ only in case"), "holds keys that differ only in case", true
	}
	if _, k, dup := redact.DuplicateJSONKey(line); dup {
		// The last value is the one a reader takes, and the one m holds;
		// the line is rewritten so the duplicate is gone and the notice fires.
		rule = joinRules(rule, "holds duplicate key "+quoteUnlessLeak(k))
	}
	if rule == "" {
		if out, err := json.Marshal(m); err == nil && lineRule(out) == "" {
			return out, "", false
		}
		rule = "cannot be re-encoded cleanly"
	}
	if caseVariantOfRewritten(m) {
		return lossMarker(lineTS(line), sessionOf(line), "case-variant of a field ethos writes"), caseVariantRule, true
	}
	cleaned, _ := scrubAuditValue(m, true).(map[string]any)
	cleaned["redacted"] = true
	finish := func(cleaned map[string]any) []byte {
		// The reader decodes tool_input as a map; anything else would make it
		// drop the line.
		if v, ok := cleaned["tool_input"]; ok {
			if _, isMap := v.(map[string]any); !isMap && v != nil {
				cleaned["tool_input"] = map[string]any{}
			}
		}
		if _, ok := cleaned["tool_input_hash"]; ok {
			cleaned["tool_input_hash"] = hashToolInput(map[string]any{"tool_input": cleaned["tool_input"]})
		}
		out, err := json.Marshal(cleaned)
		if err != nil || lineRule(out) != "" {
			return nil
		}
		return out
	}
	if out := finish(cleaned); out != nil {
		return out, rule, false
	}
	for _, k := range []string{"tool_input", "tool_input_preview", "audit_error"} {
		if _, ok := cleaned[k]; !ok {
			continue
		}
		cleaned[k] = redactedPath
		if k == "tool_input" {
			// The reader decodes tool_input as a map; a string would make it
			// drop the line.
			cleaned[k] = map[string]any{}
		}
	}
	if out := finish(cleaned); out != nil {
		return out, rule, false
	}
	return lossMarker(lineTS(line), sessionOf(line), "path that could not be removed"), rule, true
}

// lineRule names the rule the raw bytes or any decoded string or key of a
// JSON line break, or returns "".
func lineRule(line []byte) string {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		// Not JSON: the bytes are all there is to judge.
		return mission.LeakRule(string(line))
	}
	// The decoded strings and keys are the authority for a JSON line. The raw
	// bytes are not judged: they hold the escapes of the encoding (a quote
	// after /home/ is \" there), which are not part of any value.
	return valueRule(v)
}

// leakCtx says where a value sits in an audit line, for the one place the leak
// check differs: the path fields of tool_input.
type leakCtx int

const (
	ctxLine  leakCtx = iota // the line's own fields
	ctxInput                // tool_input, whose path keys are path fields
	ctxOther                // anywhere else
	ctxPath                 // a path field's value, or a list of them
)

// childCtx is the context of the value under key k of a map in context c. The
// path fields are the ones the audit writer applies the path pass to
// (redactPathKeys). Like every string they meet redact.LeakRule, except that a
// value naming a file of the checkout (redact.InsideRule) or of this machine's
// home, a ~/ value (redact.HomeRelativeRule), skips the mid-path /home/<name>
// and /Users/<name> rule (app/home/page.tsx is a file): see
// mission.PathRedactor.AuditLeakRule.
func childCtx(c leakCtx, k string) leakCtx {
	switch c {
	case ctxLine:
		if k == "tool_input" {
			return ctxInput
		}
	case ctxInput:
		switch k {
		case "file_path", "notebook_path", "path", "files", "paths":
			return ctxPath
		}
	case ctxOther, ctxPath:
	}
	return ctxOther
}

// previewExempt reports whether the entry k=e of the line map x is the
// tool_input_preview the writer derives from the line's own tool_input, and
// that tool_input passes the check: the preview is a copy of text already
// judged, and quotes the same path-field values, so it is not judged again as
// free text.
func previewExempt(x map[string]any, k string, e any) bool {
	s, ok := e.(string)
	if !ok || k != "tool_input_preview" {
		return false
	}
	env := map[string]any{}
	ti, hasInput := x["tool_input"]
	if hasInput {
		env["tool_input"] = ti
	}
	return s == toolInputPreview(env) && valueRuleIn(ti, ctxInput) == ""
}

func valueRule(v any) string { return valueRuleIn(v, ctxLine) }

func valueRuleIn(v any, c leakCtx) string {
	switch x := v.(type) {
	case string:
		return mission.AuditLeakRule(x, c == ctxPath)
	case []any:
		for _, e := range x {
			ec := ctxOther
			if c == ctxPath {
				ec = ctxPath
			}
			if r := valueRuleIn(e, ec); r != "" {
				return r
			}
		}
	case map[string]any:
		// Sorted, so the rule text is the same on every run.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if r := mission.LeakRule(k); r != "" {
				return r
			}
			if c == ctxLine && previewExempt(x, k, x[k]) {
				continue
			}
			if r := valueRuleIn(x[k], childCtx(c, k)); r != "" {
				return r
			}
		}
	}
	return ""
}

// scrubAuditValue replaces every string that leaks by redactedPath and every
// key that leaks by redactedKey, numbering collisions in sorted order.
func scrubAuditValue(v any, top bool) any {
	ctx := ctxOther
	if top {
		ctx = ctxLine
	}
	return scrubAuditIn(v, ctx)
}

func scrubAuditIn(v any, c leakCtx) any {
	switch x := v.(type) {
	case string:
		if mission.AuditLeakRule(x, c == ctxPath) != "" {
			return redactedPath
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			ec := ctxOther
			if c == ctxPath {
				ec = ctxPath
			}
			out[i] = scrubAuditIn(e, ec)
		}
		return out
	case map[string]any:
		// Keys that name no home keep their names; a renamed key takes a
		// suffix if its new name is taken, never the other way round. At the
		// top level a name is taken under EqualFold, as encoding/json folds
		// the keys of the struct-decoded header.
		out, _ := redact.RenameKeys(x, func(k string) string {
			if mission.LeakRule(k) != "" {
				return redactedKey
			}
			return k
		}, c == ctxLine, func(k string, e any) any {
			if c == ctxLine && previewExempt(x, k, e) {
				return e
			}
			return scrubAuditIn(e, childCtx(c, k))
		})
		return out
	}
	return v
}

// credentialPassLine is the part of the audit content pass that needs no home:
// credentials and addresses, in tool_input (by the writer's per-tool policy, with
// the input hash and preview rebuilt when it changes) and in every other field of
// the line, header or unknown, whatever its key. ts and the derived hash and
// preview are left alone. A line it cannot decode, or that it leaves unchanged,
// is returned as it came.
func credentialPassLine(line []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return line, false
	}
	for k := range m {
		// A decoder reads Tool_Input as the tool_input field, so its hash and
		// preview cannot be rebuilt from here: the line becomes a loss marker, as
		// the guard treats a case-variant of a field it rewrites.
		switch strings.ToLower(k) {
		case "tool_input", "tool_input_hash", "tool_input_preview":
			if k != strings.ToLower(k) {
				return lossMarker(lineTS(line), sessionOf(line), "case-variant of a field ethos writes"), true
			}
		}
	}
	changed := false
	if ti, ok := m["tool_input"].(map[string]any); ok {
		tool, _ := m["tool"].(string)
		if red, c := redactSensitiveContent(tool, ti); c {
			m["tool_input"] = red
			env := map[string]any{"tool_input": red}
			if _, has := m["tool_input_hash"]; has {
				m["tool_input_hash"] = hashToolInput(env)
			}
			if _, has := m["tool_input_preview"]; has {
				m["tool_input_preview"] = toolInputPreview(env)
			}
			m["redacted"] = true
			changed = true
		}
	}
	rest := map[string]any{}
	for k, v := range m {
		switch k {
		case "ts", "tool_input", "tool_input_hash", "tool_input_preview":
		default:
			rest[k] = v
		}
	}
	swept, c1 := redact.SecretsValue(rest)
	swept, c2 := sweepPII(swept, sweepPrompt)
	if c1 || c2 {
		out, _ := swept.(map[string]any)
		for k := range rest {
			delete(m, k)
		}
		for k, v := range out {
			m[k] = v
		}
		m["redacted"] = true
		changed = true
	}
	if !changed {
		return line, false
	}
	b, err := json.Marshal(m)
	if err != nil {
		return line, false
	}
	return b, true
}

// sessionLineGuard is the sink check for a session audit line that did not come
// from this binary's writer: a live tail an older ethos appended, or one a
// quarantine re-seals. The guard itself is home-only, so the line first gets the
// audit content pass migrate gives a legacy line: credentials and addresses
// (credentialPassLine, which needs no home and always runs), then the paths
// (redactLegacyLine, when a path redactor can be built; without a home the guard
// treats every absolute path as a leak). A line the passes leave alone goes to
// the guard as it is.
func sessionLineGuard(repoRoot string) lineGuard {
	r, err := mission.NewPathRedactor(repoRoot)
	return func(line []byte) ([]byte, string, bool) {
		pre, note := line, ""
		if isJSONObject(line) {
			if red, changed := credentialPassLine(pre); changed {
				pre, note = red, "held a credential or an address"
			}
			if err == nil {
				if red, changed, e := redactLegacyLine(pre, repoRoot, r); e == nil && changed {
					pre, note = red, "held a credential, an address or a path the content pass rewrites"
				}
			}
		}
		out, rule, marker := guardAuditLineFull(pre)
		if marker {
			return out, rule, marker
		}
		return out, joinRules(note, rule), false
	}
}

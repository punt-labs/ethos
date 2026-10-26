package mission

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/redact"
)

// leakChecker is the one question every tracked-tree writer asks of what it
// is about to write: does it name a home directory. It carries this process's
// home, or knows that it cannot name one.
type leakChecker struct {
	home  string
	known bool
}

// newLeakChecker resolves the home directory of this process. When it cannot,
// the checker treats every absolute path as a leak: with no home to compare
// against, a path cannot be cleared.
func newLeakChecker() leakChecker {
	home, err := os.UserHomeDir()
	if err != nil || !usablePrefix(home) {
		return leakChecker{}
	}
	return leakChecker{home: filepath.Clean(home), known: true}
}

// rule names the rule s breaks, in words that do not quote s, or returns "".
func (c leakChecker) rule(s string) string { return c.ruleFor(s, false) }

// ruleFor is rule for a value that sits in a path-tagged field when path is
// true. A repo-relative value there (redact.RepoRelative, as at ddefb51) is
// judged by redact.OwnHomeRule: this machine's home and its dash-encoded slug
// anywhere, and nothing else, so a write set may name app/home/page.tsx,
// nav--home-page.scss, @types/users/index.d.ts or -/home/page.tsx. There is no
// foreign-slug rule, and no rule about a flag or glob byte beside /home/ (bead
// ethos-ka73). Every other value meets all the rules. The audit log's sinks
// split off here: they use AuditLeakRule and plainPathField.
func (c leakChecker) ruleFor(s string, path bool) string {
	var r string
	if path && redact.RepoRelative(s) {
		r = redact.OwnHomeRule(s, c.home)
	} else {
		r = redact.LeakRule(s, c.home)
	}
	if r != "" {
		return r
	}
	if !c.known && redact.AbsolutePath(s) {
		return "holds an absolute path while the home directory is unknown"
	}
	return ""
}

func (c leakChecker) plainPathField(s string) bool {
	return PathRedactor{Home: c.home}.plainPathField(s)
}

// LeakRule names the rule s breaks against this process's home directory, or
// returns "" when s names no home. Without a resolvable home directory every
// absolute path breaks a rule.
func LeakRule(s string) string { return newLeakChecker().rule(s) }

// LeakRuleFor is LeakRule for a value in a path-tagged field when path is
// true: a repo-relative value there (redact.RepoRelative) is judged by redact.OwnHomeRule, this
// machine's home and its dash-encoded slug and no foreign-slug rule (bead
// ethos-ka73); the audit log uses AuditLeakRule instead.
func LeakRuleFor(s string, path bool) string { return newLeakChecker().ruleFor(s, path) }

// AuditLeakRule is PathRedactor.AuditLeakRule against this process's home
// directory, for the audit guard.
func AuditLeakRule(s string, path bool) string {
	c := newLeakChecker()
	if !path {
		return c.rule(s)
	}
	if r := redact.AuditPathRule(s, c.home, c.plainPathField(s)); r != "" {
		return r
	}
	if !c.known && redact.AbsolutePath(s) {
		return "holds an absolute path while the home directory is unknown"
	}
	return ""
}

// guardWrite is the check at the sink of every mission-tree writer. It takes
// the bytes bound for dest and returns the bytes to write in their place, or
// refuses. A structured file (.yaml, .yml, .json, .jsonl) is canonicalised:
// it is decoded into its record type (a generic value when it has none), every
// decoded key and value is checked, and the value is encoded again with the
// project's encoder. Only those bytes are written, so comments, anchors,
// aliases, tags, directives, byte-order marks, odd line breaks and any other
// syntax never reach the disk: every byte comes from a checked value. A file
// of any other kind is checked as text and written as it is. The error names
// the file's base name and the place, never the value.
func guardWrite(dest string, data []byte) ([]byte, error) {
	base := filepath.Base(dest)
	c := newLeakChecker()
	var out []byte
	var where, r string
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".yaml", ".yml", ".json", ".jsonl":
		// Checked before decoding: a decoder replaces invalid bytes, and
		// encoding/json an unpaired surrogate escape, with U+FFFD, so the
		// value checked would not be the value the file held. yaml.v3 refuses
		// every surrogate escape in a double-quoted scalar itself, valid pairs
		// included, and elsewhere in YAML \ud800 is plain text.
		if !utf8.Valid(data) {
			return nil, fmt.Errorf("refusing to write %s: its content is not valid UTF-8; remove or rephrase it", base)
		}
		if (ext == ".json" || ext == ".jsonl") && loneSurrogateEscape(data) {
			return nil, fmt.Errorf("refusing to write %s: its content holds an unpaired surrogate escape; remove or rephrase it", base)
		}
	}
	switch ext {
	case ".yaml", ".yml":
		out, where, r = canonYAML(c, data, artifactType(base))
	case ".json":
		out, where, r = canonJSON(c, data, false, nil)
	case ".jsonl":
		typ := reflect.TypeOf(Event{})
		if base == "missions.jsonl" {
			typ = reflect.TypeOf(TraceSummary{})
		}
		out, where, r = canonJSON(c, data, true, typ)
	default:
		out = data
		if r = c.rule(string(data)); r != "" {
			where = "its content"
		}
	}
	if r != "" {
		return nil, fmt.Errorf("refusing to write %s: %s %s; remove or rephrase it", base, where, r)
	}
	return out, nil
}

// emptyFileRecord finds an all-zero element in the list that makes up a whole
// file: resultsFile.Results, reflectionsFile.Reflections, or a bare legacy list
// (v is a pointer to the record the file decodes into). It returns the
// element's path, such as results[1], and its index.
//
// Scope: only those lists. Whether a field is valid inside a record
// (delegations, files_changed, evidence, preconditions, trigger, evaluator) is
// that record's Validate to decide: Contract.Validate accepts delegations:
// [{}], and a contract already on disk with one must stay updatable, closable
// and exportable.
func emptyFileRecord(v reflect.Value) (string, int, bool) {
	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	name := ""
	if v.Kind() == reflect.Struct && v.NumField() == 1 && v.Field(0).Kind() == reflect.Slice {
		name = strings.Split(v.Type().Field(0).Tag.Get("yaml"), ",")[0]
		v = v.Field(0)
	}
	if v.Kind() != reflect.Slice {
		return "", 0, false
	}
	for i := 0; i < v.Len(); i++ {
		e := v.Index(i)
		for e.Kind() == reflect.Pointer && !e.IsNil() {
			e = e.Elem()
		}
		if e.Kind() == reflect.Struct && e.IsZero() {
			return fmt.Sprintf("%s[%d]", name, i), i, true
		}
	}
	return "", 0, false
}

// emptyRecord is the rule for an all-zero list element, naming it unless the
// path names a home.
func (c leakChecker) emptyRecord(p string) string {
	if c.rule(p) != "" {
		return "holds an empty record (its name names a home)"
	}
	return fmt.Sprintf("field %q is an empty record", p)
}

// elementLine is the line of element i of the file's list in doc, or the
// document's own line.
func elementLine(doc *yaml.Node, i int) int {
	n := doc
	for n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	if n.Kind == yaml.MappingNode && len(n.Content) >= 2 {
		n = n.Content[1]
	}
	if n.Kind == yaml.SequenceNode && i < len(n.Content) {
		return n.Content[i].Line
	}
	return n.Line
}

// loneSurrogateEscape reports whether data holds a \uXXXX escape naming a
// surrogate that is not half of a high-low pair. An escaped backslash (\\) is
// not the start of an escape.
func loneSurrogateEscape(data []byte) bool {
	hex4 := func(i int) (rune, bool) {
		if i+6 > len(data) || data[i] != '\\' || data[i+1] != 'u' {
			return 0, false
		}
		var v rune
		for _, c := range data[i+2 : i+6] {
			switch {
			case c >= '0' && c <= '9':
				v = v<<4 | rune(c-'0')
			case c >= 'a' && c <= 'f':
				v = v<<4 | rune(c-'a'+10)
			case c >= 'A' && c <= 'F':
				v = v<<4 | rune(c-'A'+10)
			default:
				return 0, false
			}
		}
		return v, true
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		v, ok := hex4(i)
		if !ok {
			i++ // an escaped character, possibly a backslash
			continue
		}
		switch {
		case v >= 0xd800 && v <= 0xdbff:
			lo, ok := hex4(i + 6)
			if !ok || lo < 0xdc00 || lo > 0xdfff {
				return true
			}
			i += 11
		case v >= 0xdc00 && v <= 0xdfff:
			return true
		default:
			i += 5
		}
	}
	return false
}

// InvalidText names the rule data breaks if it is not valid UTF-8 or holds an
// unpaired surrogate escape, or returns "". A decoder would turn either into
// U+FFFD, so the value that was checked would not be the value written.
func InvalidText(data []byte) string {
	if !utf8.Valid(data) {
		return "is not valid UTF-8"
	}
	if loneSurrogateEscape(data) {
		return "holds an unpaired surrogate escape"
	}
	return ""
}

// CheckBytes runs the sink guard on data bound for path without writing it,
// returning the canonical bytes the sink would write, or the refusal.
func CheckBytes(path string, data []byte) ([]byte, error) { return guardWrite(path, data) }

// undecodable is the rule a YAML or JSON file breaks when it cannot be parsed
// for the decoded check: a file that cannot be judged is not written.
const undecodable = "cannot be decoded, so it cannot be checked"

// canonYAML decodes the one document in data into typ (or a generic value),
// encodes it with yaml.Marshal, and checks the result: every decoded key and
// value by the typed rules, as the audit guard judges decoded strings (the
// encoding's own escapes are not part of a value). A stream of more than one document is refused; ethos never
// writes one. An empty document encodes to no bytes.
func canonYAML(c leakChecker, data []byte, typ reflect.Type) ([]byte, string, string) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			if typ != nil {
				return nil, "its YAML content", "is an empty record"
			}
			return []byte{}, "", ""
		}
		return nil, "its YAML content", undecodable
	}
	if typ != nil && len(doc.Content) == 0 {
		return nil, "its YAML content", "is an empty record"
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, "its YAML content", "holds more than one document, which ethos never writes"
	}
	// The document as parsed, aliases unexpanded: the comparison with the
	// canonical encoding reports positions from it.
	var orig yaml.Node
	if err := yaml.Unmarshal(data, &orig); err != nil {
		return nil, "its YAML content", undecodable
	}
	rt := recordType(typ, &doc)
	// A field name given by an alias is how a record could name one field and
	// mean another; ethos never writes one, so a typed record refuses it,
	// naming the field it resolves to.
	if rt != nil {
		if k, line, cx, ok := aliasKey(&orig); ok {
			if cx {
				return nil, fmt.Sprintf("yaml line %d", line), "uses a complex key as a field name"
			}
			return nil, fmt.Sprintf("yaml line %d", line), c.aliasKeyRule(k)
		}
	}
	if rt != nil && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.ScalarNode && doc.Content[0].Tag == "!!null" {
		return nil, "its YAML content", "is an empty record"
	}
	budget := maxYAMLNodes
	if err := expandAliases(&doc, &budget); err != nil {
		return nil, "its YAML content", undecodable
	}
	// A field the record type does not know would be dropped by the
	// decode; it is refused instead, by name.
	if k, ok := unknownYAMLKey(&doc, rt); ok {
		return nil, "its YAML content", c.unknownField(k)
	}
	var v any
	if rt != nil {
		p := reflect.New(rt)
		strict := yaml.NewDecoder(bytes.NewReader(data))
		strict.KnownFields(true)
		if err := strict.Decode(p.Interface()); err != nil {
			return nil, "its YAML content", undecodable
		}
		if p.Elem().IsZero() {
			return nil, "its YAML content", "is an empty record"
		}
		if path, i, bad := emptyFileRecord(p); bad {
			return nil, fmt.Sprintf("yaml line %d", elementLine(&doc, i)), c.emptyRecord(path)
		}
		v = p.Interface()
	} else if err := doc.Decode(&v); err != nil {
		return nil, "its YAML content", undecodable
	}
	out, err := safeYAMLMarshal(v)
	if err != nil {
		return nil, "its YAML content", "cannot be encoded, so it cannot be checked"
	}
	// yaml.v3 does not round-trip every string (some line breaks inside a
	// block scalar are lost); an encoding that does not decode to the value
	// it came from is not a faithful copy and is refused.
	if !yamlRoundTrips(out, rt) {
		return nil, "its YAML content", "cannot be encoded faithfully, so it cannot be checked"
	}
	var n yaml.Node
	if err := yaml.Unmarshal(out, &n); err != nil {
		return nil, "its YAML content", undecodable
	}
	// The encoding holds the same keys and values as the input: a decode
	// that coerced or dropped something is refused, not written.
	if p, line := diffYAML(&orig, &n, "", 0); p != "" {
		return nil, fmt.Sprintf("yaml line %d", line), c.changedField(p)
	}
	if where, r := guardNode(c, &n, rt, false); r != "" {
		return nil, where, r
	}
	return out, "", ""
}

// yamlRoundTrips reports whether out decodes, into rt or a generic value as
// the original was decoded, to a value that encodes to out again: the encoding is a
// fixed point, so what is on disk is exactly the encoding of what it holds.
func yamlRoundTrips(out []byte, rt reflect.Type) bool {
	var back any
	if rt != nil {
		p := reflect.New(rt)
		if err := yaml.Unmarshal(out, p.Interface()); err != nil {
			return false
		}
		back = p.Interface()
	} else if err := yaml.Unmarshal(out, &back); err != nil {
		return false
	}
	again, err := safeYAMLMarshal(back)
	return err == nil && bytes.Equal(again, out)
}

// safeYAMLMarshal is yaml.Marshal, with a panic inside the encoder (which
// yaml.v3 raises for some input it cannot emit) returned as an error.
func safeYAMLMarshal(v any) (out []byte, err error) {
	defer func() {
		if p := recover(); p != nil {
			out, err = nil, fmt.Errorf("encoding yaml: %v", p)
		}
	}()
	return yaml.Marshal(v)
}

// canonJSON decodes data (one document, or one per line with perLine) into
// typ when it has one, else a generic value, encodes each with json.Marshal,
// and checks the result as canonYAML does. Blank lines are dropped; a trailing
// newline is kept.
func canonJSON(c leakChecker, data []byte, perLine bool, typ reflect.Type) ([]byte, string, string) {
	docs := [][]byte{data}
	if perLine {
		docs = bytes.Split(data, []byte("\n"))
	}
	var out [][]byte
	for i, d := range docs {
		where := "its JSON content"
		if perLine {
			where = fmt.Sprintf("line %d", i+1)
			if len(bytes.TrimSpace(d)) == 0 {
				continue
			}
		}
		enc, r := canonJSONDoc(c, d, typ)
		if r != "" {
			return nil, where, r
		}
		out = append(out, enc)
	}
	joined := bytes.Join(out, []byte("\n"))
	if perLine && len(out) > 0 && bytes.HasSuffix(data, []byte("\n")) {
		joined = append(joined, '\n')
	}
	return joined, "", ""
}

func canonJSONDoc(c leakChecker, d []byte, typ reflect.Type) ([]byte, string) {
	// A reader keeps the last of two equal keys, so a record holding a
	// duplicate is not the record it looks like: refused, at its path.
	if p, k, dup := redact.DuplicateJSONKey(d); dup {
		return nil, c.duplicateKeyRule(p, k)
	}
	in, err := decodeJSONGeneric(d)
	if err != nil {
		return nil, undecodable
	}
	if typ != nil && in == nil {
		return nil, "is an empty record"
	}
	// Field names are matched exactly: encoding/json would fold a key in
	// another case (TS) onto a field (ts), so such a key is unknown here.
	if k, ok := unknownJSONKey(in, typ); ok {
		return nil, c.unknownField(k)
	}
	var v any = in
	if typ != nil {
		p := reflect.New(typ)
		dec := json.NewDecoder(bytes.NewReader(d))
		dec.DisallowUnknownFields()
		dec.UseNumber()
		if err := dec.Decode(p.Interface()); err != nil {
			return nil, undecodable
		}
		if p.Elem().IsZero() {
			return nil, "is an empty record"
		}
		v = p.Interface()
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, "cannot be encoded, so it cannot be checked"
	}
	// An empty details map is dropped by the encoding (omitempty), so a record
	// can be non-zero as read and empty as stored; the stored form is what a
	// second write would refuse, so it is refused now.
	if typ != nil {
		q := reflect.New(typ)
		if json.Unmarshal(out, q.Interface()) == nil && q.Elem().IsZero() {
			return nil, "is an empty record"
		}
	}
	g, err := decodeJSONGeneric(out)
	if err != nil {
		return nil, undecodable
	}
	if p := diffJSON(in, g, ""); p != "" {
		return nil, c.changedField(p)
	}
	_, r := guardValue(c, g, typ, false)
	if r != "" {
		return nil, r
	}
	return out, ""
}

// decodeJSONGeneric decodes exactly one JSON value, numbers kept as written.
func decodeJSONGeneric(d []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(d))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing content")
	}
	return v, nil
}

// duplicateKeyRule is the rule for a JSON object holding a key twice. The path
// and key are named with %q unless one names a home.
func (c leakChecker) duplicateKeyRule(path, key string) string {
	if c.rule(path) != "" || c.rule(key) != "" {
		return "holds a duplicate key (its name names a home)"
	}
	return fmt.Sprintf("holds duplicate key %q at %q", redact.Prose(key), redact.Prose(path))
}

// unknownField is the rule for a key the record type does not know. The key
// is named unless it is itself a leak.
func (c leakChecker) unknownField(k string) string {
	if c.rule(k) != "" {
		return "holds a field this ethos does not know (its name names a home)"
	}
	return fmt.Sprintf("holds a field this ethos does not know: %q", k)
}

// unknownYAMLKey returns the first mapping key under a struct type that is not
// one of its fields (bead counts for the ticket of Inputs). Map types take
// any key; an untyped document has no unknown keys.
func unknownYAMLKey(n *yaml.Node, t reflect.Type) (string, bool) {
	t = derefType(t)
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if k, ok := unknownYAMLKey(c, t); ok {
				return k, true
			}
		}
	case yaml.MappingNode:
		if t == nil {
			return "", false
		}
		fields := yamlFields(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, val := n.Content[i], n.Content[i+1]
			var vt reflect.Type
			switch {
			case t.Kind() == reflect.Map:
				vt = t.Elem()
			case t.Kind() == reflect.Struct:
				sf, ok := fields[k.Value]
				if !ok || k.Kind != yaml.ScalarNode {
					return k.Value, true
				}
				vt = sf.Type
			}
			if key, ok := unknownYAMLKey(val, vt); ok {
				return key, true
			}
		}
	case yaml.SequenceNode:
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			for _, c := range n.Content {
				if k, ok := unknownYAMLKey(c, t.Elem()); ok {
					return k, true
				}
			}
		}
	}
	return "", false
}

// unknownJSONKey is unknownYAMLKey for a decoded JSON value, matching field
// names exactly.
func unknownJSONKey(v any, t reflect.Type) (string, bool) {
	t = derefType(t)
	if t == nil {
		return "", false
	}
	switch x := v.(type) {
	case map[string]any:
		fields := structFields(t, "json")
		if t == reflect.TypeOf(Inputs{}) {
			fields["bead"] = fields["ticket"]
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			var vt reflect.Type
			switch t.Kind() {
			case reflect.Map:
				vt = t.Elem()
			case reflect.Struct:
				sf, ok := fields[k]
				if !ok {
					return k, true
				}
				vt = sf.Type
			}
			if key, ok := unknownJSONKey(x[k], vt); ok {
				return key, true
			}
		}
	case []any:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			for _, e := range x {
				if k, ok := unknownJSONKey(e, t.Elem()); ok {
					return k, true
				}
			}
		}
	}
	return "", false
}

// legacyKeys are input spellings the canonical encoding writes under another
// name: the deprecated inputs.bead is the ticket.
var legacyKeys = map[string]string{"bead": "ticket"}

// diffYAML returns the path (and the input's line) of the first key or value
// of in that out does not hold the same way, or "": every key of in is in out
// with the same value (a legacy spelling under its canonical name), and a key
// only one side has holds a zero value there. Scalars are compared by their
// text, so style and quoting do not matter.
func diffYAML(in, out *yaml.Node, path string, line int) (string, int) {
	for in.Kind == yaml.DocumentNode && len(in.Content) == 1 {
		in = in.Content[0]
	}
	for out.Kind == yaml.DocumentNode && len(out.Content) == 1 {
		out = out.Content[0]
	}
	// A value reached through an alias is the anchor's node, positioned at
	// the anchor. Everything under it is reported at the alias's line, the
	// nearest place in the document.
	for in.Kind == yaml.AliasNode && in.Alias != nil {
		if line == 0 {
			line = in.Line
		}
		in = in.Alias
	}
	out = derefAlias(out)
	at := func(n *yaml.Node) int {
		if line != 0 {
			return line
		}
		return n.Line
	}
	atKey := func(keyLine int) int {
		if line != 0 {
			return line
		}
		return keyLine
	}
	here := path
	if here == "" {
		here = "(the document)"
	}
	if zeroYAML(in) && zeroYAML(out) {
		return "", 0
	}
	if in.Kind != out.Kind {
		return here, at(in)
	}
	switch in.Kind {
	case yaml.ScalarNode:
		if in.Value == out.Value {
			return "", 0
		}
		// A !!binary scalar decodes to the bytes its base64 holds.
		if in.Tag == "!!binary" {
			raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(in.Value), ""))
			if err == nil && string(raw) == out.Value {
				return "", 0
			}
		}
		return here, at(in)
	case yaml.SequenceNode:
		if len(in.Content) != len(out.Content) {
			return here, at(in)
		}
		for i := range in.Content {
			if p, l := diffYAML(in.Content[i], out.Content[i], fmt.Sprintf("%s[%d]", path, i), line); p != "" {
				return p, l
			}
		}
		return "", 0
	case yaml.MappingNode:
		// Keys are walked in document order and a difference is reported at
		// the key's line (or, under an alias, the alias's).
		im := yamlMap(in)
		om := yamlMap(out)
		for i := 0; i+1 < len(in.Content); i += 2 {
			kn, iv := in.Content[i], in.Content[i+1]
			// A key may itself be an alias; it means the anchor's value, and
			// is reported at the alias's line.
			keyLine := kn.Line
			kn = derefAlias(in.Content[i])
			k := kn.Value
			sub := joinPath(path, k)
			ov, ok := om[k]
			if !ok {
				if alt, has := legacyKeys[k]; has {
					if _, both := im[alt]; !both {
						ov, ok = om[alt]
					}
				}
			}
			if !ok {
				if !zeroYAML(iv) {
					return sub, atKey(keyLine)
				}
				continue
			}
			if p, ln := diffYAML(iv, ov, sub, line); p != "" {
				if p == sub || ln == 0 {
					ln = atKey(keyLine)
				}
				return p, ln
			}
		}
		for i := 0; i+1 < len(out.Content); i += 2 {
			k, ov := out.Content[i].Value, out.Content[i+1]
			if _, ok := im[k]; ok || legacyFor(k, im) {
				continue
			}
			if !zeroYAML(ov) {
				return joinPath(path, k), at(in)
			}
		}
		return "", 0
	}
	return here, at(in)
}

func joinPath(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

// changedField is the rule for a field whose value the canonical encoding
// would change. The field path is named unless it is itself a leak.
func (c leakChecker) changedField(p string) string {
	if c.rule(p) != "" {
		return "would change a field in its canonical encoding (its name names a home)"
	}
	return fmt.Sprintf("would change field %q in its canonical encoding (a value the record type coerces)", p)
}

// legacyFor reports whether in holds a legacy spelling of key k.
func legacyFor(k string, im map[string]*yaml.Node) bool {
	for old, canon := range legacyKeys {
		if canon == k {
			if _, ok := im[old]; ok {
				return true
			}
		}
	}
	return false
}

func yamlMap(n *yaml.Node) map[string]*yaml.Node {
	m := make(map[string]*yaml.Node, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		m[derefAlias(n.Content[i]).Value] = n.Content[i+1]
	}
	return m
}

// aliasKey returns the resolved name and line of the first mapping key under n
// that is an alias, or (complex true) the line of the first key that is a
// sequence or mapping.
func aliasKey(n *yaml.Node) (name string, line int, complex, ok bool) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			switch k.Kind {
			case yaml.AliasNode:
				if t := derefAlias(k); t.Kind == yaml.ScalarNode {
					return t.Value, k.Line, false, true
				}
				return "", k.Line, true, true
			case yaml.SequenceNode, yaml.MappingNode:
				return "", k.Line, true, true
			}
			if name, l, c, ok := aliasKey(n.Content[i+1]); ok {
				return name, l, c, true
			}
		}
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			if name, l, cx, ok := aliasKey(c); ok {
				return name, l, cx, true
			}
		}
	}
	return "", 0, false, false
}

// aliasKeyRule is the rule for a typed record whose field name is an alias.
func (c leakChecker) aliasKeyRule(name string) string {
	if c.rule(name) != "" {
		return "uses an alias as a field name (the name names a home)"
	}
	return fmt.Sprintf("uses an alias as the name of field %q", name)
}

// derefAlias follows .Alias until n is not an alias node.
func derefAlias(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// zeroYAML reports whether n holds a zero value: null, an empty or zero
// scalar, or an empty or all-zero collection.
func zeroYAML(n *yaml.Node) bool {
	n = derefAlias(n)
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return true
		}
		switch n.Value {
		case "", "0", "false", "~", "null":
			return true
		}
		return false
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			if !zeroYAML(n.Content[i]) {
				return false
			}
		}
		return true
	case yaml.SequenceNode:
		return len(n.Content) == 0
	}
	return false
}

// diffJSON is diffYAML for decoded JSON values (numbers as json.Number).
func diffJSON(in, out any, path string) string {
	here := path
	if here == "" {
		here = "(the document)"
	}
	if _, isList := in.(map[string]any); !isList {
		if _, isSlice := in.([]any); !isSlice && zeroJSON(in) && zeroJSON(out) {
			return ""
		}
	}
	if m, isMap := in.(map[string]any); isMap && len(m) == 0 && zeroJSON(out) {
		return ""
	}
	if s, isSlice := in.([]any); isSlice && len(s) == 0 && zeroJSON(out) {
		return ""
	}
	switch x := in.(type) {
	case map[string]any:
		y, ok := out.(map[string]any)
		if !ok {
			return here
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			iv := x[k]
			sub := joinPath(path, k)
			ov, ok := y[k]
			if !ok {
				if alt, has := legacyKeys[k]; has {
					if _, both := x[alt]; !both {
						ov, ok = y[alt]
					}
				}
			}
			if !ok {
				if !zeroJSON(iv) {
					return sub
				}
				continue
			}
			if p := diffJSON(iv, ov, sub); p != "" {
				return p
			}
		}
		for k, ov := range y {
			if _, ok := x[k]; ok {
				continue
			}
			legacy := false
			for old, canon := range legacyKeys {
				if _, has := x[old]; has && canon == k {
					legacy = true
				}
			}
			if !legacy && !zeroJSON(ov) {
				return joinPath(path, k)
			}
		}
		return ""
	case []any:
		y, ok := out.([]any)
		if !ok || len(x) != len(y) {
			return here
		}
		for i := range x {
			// A null list element changes the list: the typed decode turns it
			// into the element's zero value. A null scalar on a non-pointer
			// field is different: it decodes to that field's zero value, as
			// an absent key does, and legacy records hold them ("ticket":
			// null), so it is accepted.
			if (x[i] == nil) != (y[i] == nil) {
				return fmt.Sprintf("%s[%d]", path, i)
			}
			if p := diffJSON(x[i], y[i], fmt.Sprintf("%s[%d]", path, i)); p != "" {
				return p
			}
		}
		return ""
	}
	if reflect.DeepEqual(in, out) {
		return ""
	}
	return here
}

func zeroJSON(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case json.Number:
		return x == "0"
	case []any:
		return len(x) == 0
	case map[string]any:
		for _, e := range x {
			if !zeroJSON(e) {
				return false
			}
		}
		return true
	}
	return false
}

// fieldMode is the path flag for a value under a struct field with the given
// redact tag, inherited from the parent.
func fieldMode(path bool, tag string) bool {
	switch tag {
	case "path":
		return true
	case "keep":
		return false
	}
	return path
}

func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// exemptPathValue reports whether s, a value in a path field when path is true,
// is a repo-relative path. ruleFor has already judged it: every repo-relative
// value was judged by OwnHomeRule alone, with no foreign-slug rule, so it is
// blanked before the full-rule check of the whole encoding, which would
// otherwise read a mid-path /home/ and a slug inside an ordinary name again.
func exemptPathValue(s string, path bool) bool { return path && redact.RepoRelative(s) }

// guardNode checks n and everything under it by the typed rules, and blanks
// each exempt path value so the full-rule check of the re-encoded tree skips it.
func guardNode(c leakChecker, n *yaml.Node, t reflect.Type, path bool) (string, string) {
	t = derefType(t)
	where := fmt.Sprintf("yaml line %d", n.Line)
	valuePath := path && n.Kind == yaml.ScalarNode
	if r := c.ruleFor(n.Value, valuePath); r != "" {
		return where, r
	}
	if n.Kind == yaml.ScalarNode && exemptPathValue(n.Value, valuePath) {
		n.Value = "_"
	}
	for _, s := range []string{n.Tag, n.Anchor, n.HeadComment, n.LineComment, n.FootComment} {
		if r := c.rule(s); r != "" {
			return where, r
		}
	}
	if n.Tag == "!!binary" {
		raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(n.Value), ""))
		if err != nil {
			return where, undecodable
		}
		if r := c.rule(string(raw)); r != "" {
			return where, r
		}
	}
	switch n.Kind {
	case yaml.MappingNode:
		fields := yamlFields(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, val := n.Content[i], n.Content[i+1]
			vt, vp := reflect.Type(nil), path
			if t != nil && t.Kind() == reflect.Map {
				vt = t.Elem()
			}
			if sf, ok := fields[k.Value]; ok && k.Kind == yaml.ScalarNode {
				vt, vp = sf.Type, fieldMode(path, sf.Tag.Get("redact"))
			}
			// A key is data, whatever its kind: checked in full.
			if w, r := guardNode(c, k, nil, false); r != "" {
				return w, r
			}
			if w, r := guardNode(c, val, vt, vp); r != "" {
				return w, r
			}
		}
	case yaml.SequenceNode:
		var et reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			et = t.Elem()
		}
		for _, e := range n.Content {
			if w, r := guardNode(c, e, et, path); r != "" {
				return w, r
			}
		}
	default:
		for _, e := range n.Content {
			if w, r := guardNode(c, e, t, path); r != "" {
				return w, r
			}
		}
	}
	return "", ""
}

// guardValue checks a decoded JSON value by the typed rules and returns it
// with each exempt path value blanked.
func guardValue(c leakChecker, v any, t reflect.Type, path bool) (any, string) {
	t = derefType(t)
	switch x := v.(type) {
	case string:
		if r := c.ruleFor(x, path); r != "" {
			return v, r
		}
		if exemptPathValue(x, path) {
			return "_", ""
		}
	case []any:
		var et reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			et = t.Elem()
		}
		for i, e := range x {
			nv, r := guardValue(c, e, et, path)
			if r != "" {
				return v, r
			}
			x[i] = nv
		}
	case map[string]any:
		fields := structFields(t, "json")
		for k, e := range x {
			if r := c.rule(k); r != "" {
				return v, r
			}
			var vt reflect.Type
			vp := path
			if t != nil && t.Kind() == reflect.Map {
				vt = t.Elem()
			}
			if sf, ok := fields[k]; ok {
				vt, vp = sf.Type, fieldMode(path, sf.Tag.Get("redact"))
			}
			nv, r := guardValue(c, e, vt, vp)
			if r != "" {
				return v, r
			}
			x[k] = nv
		}
	}
	return v, ""
}

// ioErr is a failed I/O operation on path, naming the file by its base name
// and the cause without the full path an *fs.PathError or *os.LinkError
// carries: the directory above the base name holds the operator's username.
func ioErr(op, path string, err error) error {
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		err = pe.Err
	case errors.As(err, &le):
		err = le.Err
	}
	return fmt.Errorf("%s %s: %w", op, filepath.Base(path), err)
}

// TrackedFile is a file bound for a tracked tree outside the mission store.
type TrackedFile struct {
	Path string
	Data []byte
	Perm os.FileMode
}

// NotDurableError is WriteTrackedFiles' report that every file was written
// and renamed into place but a directory could not be fsynced, so the files
// may not survive a crash.
type NotDurableError struct {
	Err error
}

func (e *NotDurableError) Error() string {
	return "all files were written; only the directory sync failed, so durability is not guaranteed: " + e.Err.Error()
}

func (e *NotDurableError) Unwrap() error { return e.Err }

// renameFile is os.Rename; a variable so a test can fail one rename and prove
// the rollback.
var renameFile = os.Rename

// tmpMarker is in the name of every staging file ethos creates in a working
// tree, so a stray one is recognisable whatever its base name. It mirrors
// layout.TmpMarker and layout.DotTempPattern on feat/path-class-table; replace
// these two with the layout helpers when that branch lands.
const tmpMarker = ".ethos-tmp"

// dotTempPattern is the os.CreateTemp pattern for a hidden staging sibling of
// the file called base: ".base.*.ethos-tmp".
func dotTempPattern(base string) string { return "." + base + ".*" + tmpMarker }

// WriteTrackedFiles writes files for a caller outside this package whose
// output lands in a tracked tree (the mission export). Every payload is
// guarded and canonicalised (guardWrite) before any file is touched, and
// every existing destination is read into memory. Each payload is written to a hidden, marked temp file beside
// its destination and fsynced; only then is each renamed over its
// destination, which is atomic, so a destination always holds a complete
// file. If a later swap fails, each swapped destination is put back from the
// saved bytes the same way (a new destination is removed), so a failed
// re-export never leaves a mismatched pair; if a restore itself fails, that
// destination keeps the new content, never nothing. Failures to restore or to
// remove a temp file are joined into the returned error.
//
// The renames are durable only once the directories are fsynced. A failed
// directory fsync is returned as a *NotDurableError (test with errors.As): the
// files are in place, but may not survive a crash.
//
// A file's mode is exactly Perm: it is set with chmod, so the umask does not
// narrow it.
func WriteTrackedFiles(files ...TrackedFile) error {
	files = append([]TrackedFile(nil), files...)
	for i, f := range files {
		data, err := guardWrite(f.Path, f.Data)
		if err != nil {
			return err
		}
		files[i].Data = data
	}
	type saved struct {
		data []byte
		perm os.FileMode
		ok   bool // the destination existed
	}
	old := make([]saved, len(files))
	for i, f := range files {
		info, err := os.Lstat(f.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return ioErr("checking", f.Path, err)
		case !info.Mode().IsRegular():
			return fmt.Errorf("%s exists and is not a regular file", filepath.Base(f.Path))
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return ioErr("reading", f.Path, err)
		}
		old[i] = saved{data, info.Mode().Perm(), true}
	}

	temps := make([]string, len(files))
	removeTemps := func() error {
		var errs []error
		for _, t := range temps {
			if t == "" {
				continue
			}
			if err := os.Remove(t); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, ioErr("removing temp file", t, err))
			}
		}
		return errors.Join(errs...)
	}
	for i, f := range files {
		tmp, err := writeTrackedTemp(TrackedFile{Path: f.Path, Data: f.Data, Perm: f.Perm})
		temps[i] = tmp
		if err != nil {
			return errors.Join(err, removeTemps())
		}
	}

	for i, f := range files {
		if err := renameFile(temps[i], f.Path); err != nil {
			errs := []error{ioErr("renaming the temp file to", f.Path, err), removeTemps()}
			for j := i - 1; j >= 0; j-- {
				errs = append(errs, restoreTracked(files[j].Path, old[j].data, old[j].perm, old[j].ok))
			}
			return errors.Join(errs...)
		}
		temps[i] = ""
	}

	var errs []error
	seen := map[string]bool{}
	for _, f := range files {
		dir := filepath.Dir(f.Path)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if err := syncDir(dir); err != nil {
			errs = append(errs, ioErr("syncing directory of", f.Path, err))
		}
	}
	if len(errs) > 0 {
		return &NotDurableError{Err: errors.Join(errs...)}
	}
	return nil
}

// restoreTracked puts dest back as it was before a swap: the saved bytes,
// written to a marked temp and renamed over dest, or no file if there was none.
// On failure dest keeps what it holds now.
func restoreTracked(dest string, data []byte, perm os.FileMode, existed bool) error {
	if !existed {
		if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return ioErr("removing", dest, err)
		}
		return nil
	}
	tmp, err := writeTrackedTemp(TrackedFile{Path: dest, Data: data, Perm: perm})
	if err == nil {
		err = renameFile(tmp, dest)
	}
	if err != nil {
		errs := []error{ioErr("restoring", dest, err)}
		if tmp != "" {
			if rmErr := os.Remove(tmp); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				errs = append(errs, ioErr("removing temp file", tmp, rmErr))
			}
		}
		return errors.Join(errs...)
	}
	return nil
}

// writeTrackedTemp writes f's data to a hidden, marked temp file beside
// f.Path, sets its mode, fsyncs and closes it, and returns the temp file's name
// (also on a failure after it was created, so the caller can remove it).
func writeTrackedTemp(f TrackedFile) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), dotTempPattern(filepath.Base(f.Path)))
	if err != nil {
		return "", ioErr("creating temp file for", f.Path, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(f.Data); err != nil {
		_ = tmp.Close()
		return name, ioErr("writing", f.Path, err)
	}
	if err := tmp.Chmod(f.Perm); err != nil {
		_ = tmp.Close()
		return name, ioErr("chmod", f.Path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return name, ioErr("syncing", f.Path, err)
	}
	if err := tmp.Close(); err != nil {
		return name, ioErr("closing", f.Path, err)
	}
	return name, nil
}

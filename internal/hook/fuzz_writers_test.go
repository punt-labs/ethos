package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// writerSeeds are raw audit lines: duplicate keys, invalid UTF-8, surrogate
// escapes, case-variant keys, string tool_input, leaks, and clean lines.
func writerSeeds() [][]byte {
	ts := audit.FormatLineTS(100)
	h := `"ts":"` + ts + `","session":"s"`
	return [][]byte{
		[]byte(`{` + h + `,"tool":"Read","tool_input":{"file_path":"~/x"}}`),
		[]byte(`{` + h + `,"tool":"Bash","tool_input":{"command":"cat /home/bob/.ssh/id"}}`),
		[]byte(`{` + h + `,"session":"t","tool":"Read"}`),
		[]byte(`{` + h + `,"tool_input":{"a":1,"a":2}}`),
		[]byte(`{` + h + `,"tool_input":{"x":[{"k":1,"k":2}]}}`),
		[]byte(`{` + h + `,"cwd":"a` + "\xff" + `b"}`),
		[]byte(`{` + h + `,"cwd":"\ud800"}`),
		[]byte(`{` + h + `,"TS":"x"}`),
		[]byte(`{` + h + `,"tool_input":"ls","tool_input_hash":"h"}`),
		[]byte(`{` + h + `,"Redacted":false,"tool_input":{"a":"b"}}`),
		[]byte(`{` + h + `,"path_format":"x"}`),
		[]byte(`{` + h + `,"tool_input":{"/home/zed/a":"1","~/a":"2","/home/bob/a":"3"}}`),
		[]byte(`{` + h + `,"[REDACTED-KEY]":1,"/home/bob/a":2,"tool":"x"}`),
		[]byte(`{` + h + `,"x":{"Content-Type":"a","content-type":"b"}}`),
		[]byte(`{` + h + `,"ſession":"b"}`),
		[]byte(`{` + h + `,"":1,"":2}`),
		[]byte(`{"ts":"1"}`), []byte(`null`), []byte(`[]`), []byte(``), []byte(`not json`),
		[]byte(`{` + h + `,"n":1152921504606846977,"f":1.50,"b":true,"z":null}`),
	}
}

// decodeGeneric decodes one JSON object, numbers kept as written; last
// duplicate wins, as a reader sees it.
func decodeGeneric(b []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	if _, err := dec.Token(); err == nil || err.Error() != "EOF" {
		return nil, false
	}
	return m, true
}

// checkLineProperties asserts the four writer properties for one raw line
// through guardAuditLineFull: (a) no leak, (b) no non-leaking value lost
// unless the line became a loss marker, (c) a difference from the input other
// than canonical encoding comes with a rule, (d) the output is a fixed point
// that reports nothing.
func checkLineProperties(t *testing.T, what string, in []byte, home string) {
	t.Helper()
	out, rule, marker := guardAuditLineFull(in)
	if leak := decodedLeak(out, home); leak != "" {
		t.Fatalf("%s: (a) the output names the home (home %s): %q in %q out %q", what, home, leak, in, out)
	}
	if !utf8validJSON(out) {
		t.Fatalf("%s: the output is not valid JSON text: in %q out %q", what, in, out)
	}
	if !marker {
		if inM, ok := decodeGeneric(in); ok {
			got := testhelpers.Leaves{}
			if outM, ok := decodeGeneric(out); ok {
				got = lineLeaves(outM)
			}
			if leaf, ok := testhelpers.LostLeaf(lineLeaves(inM), got); ok {
				t.Fatalf("%s: (b) value %q lost with nothing in its place: in %q out %q", what, leaf, in, out)
			}
			// (c) no unreported difference: with no rule the decoded output
			// equals the decoded input.
			if rule == "" {
				outM, ok := decodeGeneric(out)
				if !ok || !reflect.DeepEqual(inM, outM) {
					t.Fatalf("%s: (c) the line changed with no rule: in %q out %q", what, in, out)
				}
			}
		} else if rule == "" {
			t.Fatalf("%s: (c) a line that is not one JSON object was stored with no rule: in %q out %q", what, in, out)
		}
	}
	again, rule2, _ := guardAuditLineFull(out)
	if !bytes.Equal(again, out) || rule2 != "" {
		t.Fatalf("%s: (d) the output is not a fixed point: in %q out %q again %q rule %q", what, in, out, again, rule2)
	}
}

func utf8validJSON(b []byte) bool {
	return json.Valid(b) && strings.ToValidUTF8(string(b), "\x00") == string(b)
}

// FuzzWritersAudit drives every audit writer path with raw bytes: the appender
// guard, legacy migration, seal and quarantine. Seal and quarantine write each
// line through guardTail and guardLine, which are the appender's guard (the
// harness asserts it), and their other writes are a chunk name made of the
// first and last ts, which the guard preserves, a marker reason that goes
// through redactReason, and the retired bytes judged by corruptRule, asserted
// here. A seal or quarantine run end to end adds git and the filesystem and
// no judgement of its own, so it is covered by the tests in internal/audit
// and TestQuarantineChunk_WithholdsRetiredBytesThatLeak.
func FuzzWritersAudit(f *testing.F) {
	for i := range auditHomes {
		for _, s := range writerSeeds() {
			f.Add(s, uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, line []byte, h uint8) {
		home := auditHomes[int(h)%len(auditHomes)]
		t.Setenv("HOME", home)
		testhelpers.CaptureStderr(t, func() {
			checkLineProperties(t, "appender", line, home)

			// Seal and quarantine run the same guard over each tail line.
			sealed, _ := guardTail([][]byte{line}, "label")
			if len(sealed) != 1 {
				t.Fatalf("guardTail returned %d lines", len(sealed))
			}
			if !bytes.Equal(sealed[0], func() []byte { o, _, _ := guardAuditLineFull(line); return o }()) {
				t.Fatalf("seal and the appender disagree on %q", line)
			}

			// Legacy migration: (a) no leak, (b) no value lost with nothing in
			// its place, (c) no change without a notice, (d) a fixed point.
			// A line that became a loss marker is judged by (a) and (d) only.
			var lines []rawAuditLine
			var err error
			lines, err = redactLegacyLines([]rawAuditLine{{raw: line}}, t.TempDir())
			notice := ""
			if err == nil && len(lines) == 1 && lines[0].note != nil {
				notice = "noted"
				if slices.Contains(lines[0].note.kinds, kindMarker) {
					notice = "loss marker"
				}
			}
			if err == nil && len(lines) == 1 {
				out := lines[0].raw
				if decodedLeak(out, home) != "" {
					t.Fatalf("migrate: (a) leak: in %q out %q", line, out)
				}
				if inM, ok := decodeGeneric(line); ok && !strings.Contains(notice, "loss marker") {
					outM, outOK := decodeGeneric(out)
					if !outOK {
						t.Fatalf("migrate: the output is not one JSON object: in %q out %q", line, out)
					}
					if notice == "" && !reflect.DeepEqual(inM, outM) {
						t.Fatalf("migrate: (c) the line changed with no notice: in %q out %q", line, out)
					}
					if leaf, ok := testhelpers.LostLeaf(lineLeaves(inM), lineLeaves(outM)); ok {
						t.Fatalf("migrate: (b) value %q lost with nothing in its place: in %q out %q", leaf, line, out)
					}
				}
				again, err := redactLegacyLines([]rawAuditLine{{raw: out}}, t.TempDir())
				if err != nil || len(again) != 1 || !bytes.Equal(again[0].raw, out) {
					t.Fatalf("migrate: (d) not a fixed point: in %q out %q again %v %v", line, out, again, err)
				}
			}

			// Quarantine stages a retired chunk's bytes: when its check passes
			// them, no decoded string or raw byte of them names the home.
			if corruptRule(line) == "" && (auditHoldsHome(string(line), home, false) || decodedLeak(line, home) != "") {
				t.Fatalf("quarantine: corruptRule passed bytes that name the home %s: %q", home, line)
			}
		})
	})
}

// FuzzWritersToolInput drives the live audit path with a raw map as tool_input:
// redactToolInput, buildAuditEntry, the marshalled entry and guardAuditLine.
func FuzzWritersToolInput(f *testing.F) {
	for i := range auditHomes {
		for _, s := range writerSeeds() {
			f.Add(s, uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte, h uint8) {
		home := auditHomes[int(h)%len(auditHomes)]
		t.Setenv("HOME", home)
		m, ok := decodeGeneric(raw)
		if !ok {
			return
		}
		testhelpers.CaptureStderr(t, func() {
			e := buildAuditEntry(map[string]any{"tool_name": "Bash", "tool_input": m}, "s", t.TempDir(), time.Unix(0, 100))
			line, err := json.Marshal(e)
			if err != nil {
				return
			}
			checkLineProperties(t, "buildAuditEntry", line, home)
			// (a) on the entry itself, before the guard.
			if decodedLeak(line, home) != "" {
				// the guard must still fix it
				out, rule, _ := guardAuditLineFull(line)
				if rule == "" || decodedLeak(out, home) != "" {
					t.Fatalf("an entry leaks and the guard did not rewrite it: %q -> %q (%q)", line, out, rule)
				}
			}
			_ = fmt.Sprint
		})
	})
}

// decodedLeak returns the first decoded string or key of a JSON line that the
// oracle matches, or "" (also for text that is not JSON: its bytes are judged).
func decodedLeak(line []byte, home string) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		if auditHoldsHome(string(line), home, false) {
			return string(line)
		}
		return ""
	}
	var strs []auditStr
	auditStringsIn(v, auditCtxLine, &strs)
	for _, s := range strs {
		if auditHoldsHome(s.v, home, s.own) {
			return s.v
		}
	}
	return ""
}

// TestLostLeafMutant_AGuardThatDropsYWhileRewritingX: the oracle fails a guard
// that rewrites x and drops the y pair, though one global count of what was
// added would cover what was lost.
func TestLostLeafMutant_AGuardThatDropsYWhileRewritingX(t *testing.T) {
	in, _ := decodeGeneric([]byte(`{"ts":"t","session":"s","tool_input":{"x":"/home/zed/a","y":1},"tool_input_hash":"h","redacted":false}`))
	good, _ := decodeGeneric([]byte(`{"ts":"t","session":"s","tool_input":{"x":"~/a","y":1},"tool_input_hash":"h2","redacted":true}`))
	bad, _ := decodeGeneric([]byte(`{"ts":"t","session":"s","tool_input":{"x":"~/a"},"tool_input_hash":"h2","redacted":true}`))
	if leaf, lost := testhelpers.LostLeaf(lineLeaves(in), lineLeaves(good)); lost {
		t.Errorf("a faithful rewrite was reported lost: %q", leaf)
	}
	if _, lost := testhelpers.LostLeaf(lineLeaves(in), lineLeaves(bad)); !lost {
		t.Error("a guard that drops y while rewriting x was not caught")
	}
}

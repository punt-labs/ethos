package mission

import (
	"bytes"
	"encoding/json"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardWrite_RefusesUnknownFields: a field the record type does not know
// is a refusal naming the field, never a silent deletion, for every record
// type the sink writes.
func TestGuardWrite_RefusesUnknownFields(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	tests := []struct{ name, file, data, key string }{
		{"contract", "contract.yaml", "mission_id: m-1\nfuture_field: x\n", "future_field"},
		{"contract inputs", "contract.yaml", "mission_id: m-1\ninputs:\n    newer: x\n", "newer"},
		{"result", "m-1.result.yaml", "round: 1\nnewer: x\n", "newer"},
		{"results", "results.yaml", "results:\n    - round: 1\n      newer: x\n", "newer"},
		{"reflections", "reflections.yaml", "reflections:\n    - round: 1\n      newer: x\n", "newer"},
		{"delegation record", "record.yaml", "id: d-1\nnewer: x\n", "newer"},
		{"trace summary", "missions.jsonl", `{"id":"m-1","newer":"x"}` + "\n", "newer"},
		{"event", "log.jsonl", `{"ts":"t","event":"e","actor":"a","newer":1}` + "\n", "newer"},
		{"event key in another case", "log.jsonl", `{"TS":"t","event":"e","actor":"a"}` + "\n", "TS"},
		{"trace key in another case", "missions.jsonl", `{"ID":"m-1"}` + "\n", "ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := guardWrite(filepath.Join("/r", tt.file), []byte(tt.data))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "holds a field this ethos does not know: "+strconv.Quote(tt.key))
		})
	}
	_, err := guardWrite("/r/contract.yaml", []byte("mission_id: m-1\n/home/bob/x: v\n"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "bob", "a key that is a leak is not quoted")
}

// TestGuardWrite_RefusesAChangedValue: the canonical encoding must hold the
// same keys and values as the input; a value the record type coerces is
// refused rather than rewritten.
func TestGuardWrite_RefusesAChangedValue(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	_, err := guardWrite("/r/contract.yaml", []byte("mission_id: m-1\nbudget:\n    rounds: 1.50\n"))
	require.Error(t, err)
	// The legacy bead spelling of the ticket, a null, and absent zero fields
	// are the same record.
	_, err = guardWrite("/r/contract.yaml", []byte("mission_id: m-1\ninputs:\n    bead: t-1\nworker: null\n"))
	require.NoError(t, err)
}

// TestEvent_LargeDetailNumberSurvivesAnAppend: a number in Details keeps all
// its digits through the sink.
func TestEvent_LargeDetailNumberSurvivesAnAppend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n := int64(1<<60 + 1)
	line, err := json.Marshal(Event{TS: "t", Event: "e", Actor: "a", Details: map[string]any{"n": n}})
	require.NoError(t, err)
	out, err := guardWrite("/r/log.jsonl", append(line, '\n'))
	require.NoError(t, err)
	assert.Contains(t, string(out), `"n":1152921504606846977`)
}

// TestScrubFile_RefusesANewerSchemaField: scrub refuses a record holding a
// field this ethos does not know and leaves the file as it was.
func TestScrubFile_RefusesANewerSchemaField(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	rel := ".punt-labs/ethos/missions/m-2026-07-21-002/contract.yaml"
	dest := filepath.Join(repo, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o700))
	body := []byte("mission_id: m-2026-07-21-002\nfuture_field: keep me\n")
	require.NoError(t, os.WriteFile(dest, body, 0o600))
	r, err := NewPathRedactor(repo)
	require.NoError(t, err)

	_, err = scrubFile(r, repo, rel, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "future_field")
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(body, got), "the file is left as it was")
}

// TestMigrateMission_RefusesANewerSchemaField: a legacy record with a field
// this ethos does not know is not copied, and the legacy source stays.
func TestMigrateMission_RefusesANewerSchemaField(t *testing.T) {
	newLeakFixture(t)
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	id := "m-2026-05-22-007"
	legacy := legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml": []byte("mission_id: " + id + "\nstatus: open\nfuture_field: x\n"),
	})
	repoAuditWithMission(t, repoRoot, "sess-7", id)

	var out bytes.Buffer
	require.Error(t, MigrateMission(globalRoot, repoRoot, repoRoot, "", false, &out))
	_, err := os.Stat(filepath.Join(repoRoot, ".punt-labs", "ethos", "missions", id, "contract.yaml"))
	assert.True(t, os.IsNotExist(err), "nothing is copied")
	_, err = os.Stat(legacy)
	assert.NoError(t, err, "the legacy source is not deleted")
}

// TestGuardWrite_RefusesInvalidText: a structured file that is not valid UTF-8,
// or holds an unpaired surrogate escape, is refused before it is decoded, and
// scrub leaves such a file as it was.
func TestGuardWrite_RefusesInvalidText(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for name, tc := range map[string]struct{ file, data string }{
		"invalid utf-8 in a trace":  {"missions.jsonl", "{\"id\":\"a\xffb\"}\n"},
		"lone surrogate in details": {"log.jsonl", `{"ts":"t","event":"e","actor":"a","details":{"x":"\ud800"}}` + "\n"},
		"lone low surrogate":        {"log.jsonl", `{"ts":"t","event":"e","actor":"a","details":{"x":"\udc00z"}}` + "\n"},
		"invalid utf-8 in yaml":     {"contract.yaml", "mission_id: a\xffb\n"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := guardWrite("/r/"+tc.file, []byte(tc.data))
			require.Error(t, err)
		})
	}
	_, err := guardWrite("/r/log.jsonl", []byte(`{"ts":"t","event":"e","actor":"a","details":{"x":"\ud83d\ude00 \\ud800"}}`+"\n"))
	assert.NoError(t, err, "a paired surrogate and an escaped backslash are fine")

	repo := t.TempDir()
	rel := ".punt-labs/ethos/missions.jsonl"
	dest := filepath.Join(repo, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o700))
	body := []byte("{\"id\":\"a\xffb\"}\n")
	require.NoError(t, os.WriteFile(dest, body, 0o600))
	r, err := NewPathRedactor(repo)
	require.NoError(t, err)
	_, err = scrubFile(r, repo, rel, false)
	require.Error(t, err)
	got, _ := os.ReadFile(dest)
	assert.True(t, bytes.Equal(body, got), "the file is left as it was")
}

// TestGuardWrite_ChangedValueNamesTheField: the refusal names the first field
// that would change, and hides a field name that is itself a leak.
func TestGuardWrite_ChangedValueNamesTheField(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	_, err := guardWrite("/r/contract.yaml", []byte("mission_id: m-1\nbudget:\n    rounds: 1.50\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "budget.rounds")
	assert.Contains(t, err.Error(), "yaml line 3")
	// The JSON decoder refuses a type mismatch itself; the comparison names
	// the path when a value does change.
	in := map[string]any{"id": "m-1", "details": map[string]any{"n": json.Number("1.50")}}
	out := map[string]any{"id": "m-1", "details": map[string]any{"n": json.Number("1.5")}}
	assert.Equal(t, "details.n", diffJSON(in, out, ""))
	assert.Empty(t, diffJSON(in, in, ""))
}

// TestGuardWrite_RefusesAnEmptyRecord: a null line or document is not a
// record of the type the file holds.
func TestGuardWrite_RefusesAnEmptyRecord(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for file, data := range map[string]string{
		"missions.jsonl": "null\n",
		"log.jsonl":      `{"ts":"t","event":"e","actor":"a"}` + "\nnull\n",
		"contract.yaml":  "~\n",
		"record.yaml":    "null\n",
	} {
		_, err := guardWrite("/r/"+file, []byte(data))
		require.Error(t, err, file)
		assert.Contains(t, err.Error(), "empty record", file)
	}
}

// TestGuardWrite_RefusesEmptyTypedRecords: an empty, blank or comment-only
// document, or an object that decodes to the record's zero value, is not a
// record. An untyped file and an empty log are accepted.
func TestGuardWrite_RefusesEmptyTypedRecords(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, file := range []string{"contract.yaml", "m-1.result.yaml", "results.yaml", "reflections.yaml", "record.yaml"} {
		for name, data := range map[string]string{"empty": "", "blank": "  \n\n", "comment only": "# nothing\n", "empty object": "{}\n"} {
			_, err := guardWrite("/r/"+file, []byte(data))
			if assert.Error(t, err, "%s %s", file, name) {
				assert.Contains(t, err.Error(), "empty record", "%s %s", file, name)
			}
		}
	}
	for _, file := range []string{"missions.jsonl", "log.jsonl"} {
		_, err := guardWrite("/r/"+file, []byte("{}\n"))
		if assert.Error(t, err, file) {
			assert.Contains(t, err.Error(), "empty record", file)
		}
		_, err = guardWrite("/r/"+file, []byte(""))
		assert.NoError(t, err, "an empty %s is an empty log", file)
		_, err = guardWrite("/r/"+file, []byte("\n\n"))
		assert.NoError(t, err, "a blank %s is an empty log", file)
	}
	for _, data := range []string{"", "# nothing\n", "{}\n"} {
		_, err := guardWrite("/r/notes.yaml", []byte(data))
		assert.NoError(t, err, "untyped %q", data)
	}
}

// TestGuardWrite_YAMLBackslashUIsText: outside a JSON file a \ud800 is plain
// text unless it sits in a double-quoted YAML scalar, where yaml.v3 refuses
// every surrogate escape itself, valid pairs included.
func TestGuardWrite_YAMLBackslashUIsText(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	_, err := guardWrite("/r/contract.yaml", []byte("mission_id: m-1\ncontext: refuse \\ud800 lone surrogates\n"))
	assert.NoError(t, err)
	_, err = guardWrite("/r/contract.yaml", []byte("mission_id: m-1\ncontext: \"\\ud800\"\n"))
	assert.Error(t, err)
}

// TestDiffYAML_NamesTheFirstFieldInTheDocument: two changed fields on one line
// are reported in column order.
func TestDiffYAML_NamesTheFirstFieldInTheDocument(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for i := 0; i < 20; i++ {
		_, err := guardWrite("/r/contract.yaml", []byte("{mission_id: m-1, current_round: 2.50, budget: {rounds: 1.50}}\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), `field "current_round" `)
	}
}

// TestDiffYAML_AliasNamesTheKeyLine: a value reached through an alias carries
// the anchor's position, so the field is found and reported by its key.
func TestDiffYAML_AliasNamesTheKeyLine(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	data := []byte("mission: m-1\nround: &v 2.50\nconfidence: *v\n")
	for i := 0; i < 200; i++ {
		_, err := guardWrite("/r/m-1.result.yaml", data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "yaml line 2")
		assert.Contains(t, err.Error(), `field "round" `)
	}
}

// TestGuardWrite_EmptyRecordsInLists: a list element that decodes to the
// record's zero value is refused; an empty list is what the writers emit for
// an empty file and is accepted; a null list is an empty record.
func TestGuardWrite_EmptyRecordsInLists(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	refused := map[string]string{
		"results.yaml":     "results: [{}]\n",
		"reflections.yaml": "reflections:\n    - {}\n",
	}
	for file, data := range refused {
		_, err := guardWrite("/r/"+file, []byte(data))
		if assert.Error(t, err, file) {
			assert.Contains(t, err.Error(), "empty record", file)
		}
	}
	for _, data := range []string{"- {}\n", "- round: 1\n- {}\n"} {
		_, err := guardWrite("/r/results.yaml", []byte(data))
		assert.Error(t, err, "legacy list %q", data)
	}
	for file, data := range map[string]string{
		"results.yaml":     "results: []\n",
		"reflections.yaml": "reflections: []\n",
	} {
		_, err := guardWrite("/r/"+file, []byte(data))
		assert.NoError(t, err, file)
	}
	_, err := guardWrite("/r/results.yaml", []byte("[]\n"))
	assert.NoError(t, err, "a bare empty legacy list")
	_, err = guardWrite("/r/results.yaml", []byte("results:\n"))
	assert.Error(t, err, "a null list is an empty record")
}

// TestGuardWrite_EmptyRecordScope: the sink refuses an all-zero element only
// in the lists that make up a whole file; whether a field is valid inside a
// record is the record's Validate, so delegations: [{}] is not refused. The
// refusal names the element.
func TestGuardWrite_EmptyRecordScope(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	_, err := guardWrite("/r/results.yaml", []byte("results:\n    - round: 1\n    - {}\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `field "results[1]" is an empty record`)
	assert.Contains(t, err.Error(), "yaml line 3")
	_, err = guardWrite("/r/reflections.yaml", []byte("reflections: [{}]\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `field "reflections[0]" is an empty record`)
	_, err = guardWrite("/r/results.yaml", []byte("- {}\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `field "[0]" is an empty record`)

	_, err = guardWrite("/r/contract.yaml", []byte("mission_id: m-1\ndelegations: [{}]\n"))
	assert.NoError(t, err, "a zero element inside a record is for Validate")
}

// TestStore_ContractWithAnEmptyDelegationStaysUsable: Validate accepts
// delegations: [{}], so a contract holding one can still be updated and closed.
func TestStore_ContractWithAnEmptyDelegationStaysUsable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	s := NewStoreWithRoots(repo, t.TempDir())
	c := validContract()
	c.Delegations = []DelegationTemplate{{}}
	require.NoError(t, s.Create(&c))
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	loaded.Context = "updated"
	require.NoError(t, s.Update(loaded))
	require.NoError(t, s.AppendResult(c.MissionID, &Result{Mission: c.MissionID, Round: 1, Author: c.Worker, Verdict: VerdictPass,
		Evidence: []EvidenceCheck{{Name: "x", Status: EvidenceStatusPass}}}))
	_, err = s.Close(c.MissionID, StatusClosed)
	require.NoError(t, err)
}

// TestGuardWrite_JSONNullIsNotAnEmptyString: a null list element is refused
// with its field path, as on the YAML path.
func TestGuardWrite_JSONNullIsNotAnEmptyString(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, data := range []string{
		`{"id":"m-1","write_set":[null]}`,
		`{"id":"m-1","files_changed":[null]}`,
	} {
		_, err := guardWrite("/r/missions.jsonl", []byte(data+"\n"))
		require.Error(t, err, data)
		assert.Contains(t, err.Error(), "would change field", data)
	}
	assert.Empty(t, diffJSON([]any{""}, []any{""}, "x"))
	assert.NotEmpty(t, diffJSON([]any{nil}, []any{""}, "x"))
}

// TestDiffYAML_LinesAreInputKeyLines: a key present only in the output reports
// the line of the enclosing input node, and a difference under an alias the
// alias's line, never the anchor's.
func TestDiffYAML_LinesAreInputKeyLines(t *testing.T) {
	parse := func(s string) *yaml.Node {
		var n yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(s), &n))
		return &n
	}
	p, l := diffYAML(parse("a: 1\n"), parse("a: 1\nb: 2\n"), "", 0)
	assert.Equal(t, "b", p)
	assert.Equal(t, 1, l, "the input's own line, as the output's has none")

	in := parse("a: &x {n: 2.50}\nb: *x\n")
	out := parse("a: {n: 2.50}\nb: {n: 2.5}\n")
	p, l = diffYAML(in, out, "", 0)
	assert.Equal(t, "b.n", p)
	assert.Equal(t, 2, l, "the alias's line, not the anchor's (line 1)")
}

// TestGuardWrite_AliasesInKeysAndValues: an alias is followed wherever it
// stands, as a value, a key, or a merge, so a record holding one is judged by
// what it means.
func TestGuardWrite_AliasesInKeysAndValues(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	_, err := guardWrite("/r/contract.yaml", []byte("mission_id: m-1\ncontext: &z \"\"\ninputs: {ticket: *z}\n"))
	assert.NoError(t, err, "an aliased empty string is an empty string")
	_, err = guardWrite("/r/notes.yaml", []byte("a: &z {}\nb: {<<: *z}\n"))
	assert.NoError(t, err, "a merge of an empty mapping changes nothing")
	_, err = guardWrite("/r/notes.yaml", []byte("a: &k name\n*k : val\n"))
	assert.NoError(t, err, "an untyped file may use an alias as a key")
	_, err = guardWrite("/r/contract.yaml", []byte("mission_id: m-1\nworker: &k context\n*k : hello\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context", "the key is named by what it means")
	assert.NotContains(t, err.Error(), `field "k" `)
}

// TestDiffYAML_ScalarDifferenceIsAtTheKey: a scalar on its own line is
// reported at its key's line.
func TestDiffYAML_ScalarDifferenceIsAtTheKey(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	_, err := guardWrite("/r/m-1.result.yaml", []byte("mission: m-1\nround:\n  1.50\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "yaml line 2")
}

// TestGuardWrite_ComplexKeyInATypedRecord: a mapping key that is a sequence or
// mapping is refused as a complex key, with its line, not as an empty field.
func TestGuardWrite_ComplexKeyInATypedRecord(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	_, err := guardWrite("/r/contract.yaml", []byte("mission_id: m-1\nk: &k name\n? [*k]\n: x\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uses a complex key")
	assert.Contains(t, err.Error(), "yaml line 3")
}

// TestGuardWrite_RulesQuoteNamesWithQ: field names in rules are %q-quoted.
func TestGuardWrite_RulesQuoteNamesWithQ(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	_, err := guardWrite("/r/contract.yaml", []byte("mission_id: m-1\n\"fu\\nture\": x\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"fu\nture"`)
	assert.NotContains(t, err.Error(), "fu\nture")
}

// TestGuardWrite_DuplicateJSONKeys: a duplicate key at any depth of a .json or
// .jsonl record is refused with its path; YAML refuses a duplicate too.
func TestGuardWrite_DuplicateJSONKeys(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, tt := range []struct{ file, data, path string }{
		{"missions.jsonl", `{"id":"a","id":"b"}`, `"id"`},
		{"log.jsonl", `{"ts":"t","event":"e","actor":"a","details":{"x":{"k":1,"k":2}}}`, `"details.x.k"`},
		{"log.jsonl", `{"ts":"t","event":"e","actor":"a","details":{"l":[{"k":1,"k":2}]}}`, `"details.l[0].k"`},
	} {
		_, err := guardWrite("/r/"+tt.file, []byte(tt.data+"\n"))
		require.Error(t, err, tt.data)
		assert.Contains(t, err.Error(), "holds duplicate key", tt.data)
		assert.Contains(t, err.Error(), tt.path, tt.data)
	}
	_, err := guardWrite("/r/notes.yaml", []byte("a: 1\na: 2\n"))
	assert.Error(t, err, "a duplicate YAML key is refused")
	_, err = guardWrite("/r/contract.yaml", []byte("mission_id: a\nmission_id: b\n"))
	assert.Error(t, err)
}

// TestSafeField_QuotesWithQ: a field name with a newline cannot forge a line
// of the error.
func TestSafeField_QuotesWithQ(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	got := safeField("a\nforged: line")
	assert.Equal(t, `"a\nforged: line"`, got)
	assert.NotContains(t, got, "\n")
}

// TestRedactRecord_ReportsARenameOnStderr: a map key the passes rename prints
// a notice, so the change is never silent.
func TestRedactRecord_ReportsARenameOnStderr(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	var err error
	notice := testhelpers.CaptureStderr(t, func() {
		var say Announcement
		_, say, err = RedactRecordNotify("/work/repo", Event{TS: "t", Event: "e", Actor: "a",
			Details: map[string]any{"/home/zed/a": "1", "~/a": "2"}})
		say.Say()
	})
	require.NoError(t, err)
	assert.Contains(t, notice, "was renamed")
	assert.NotContains(t, notice, "zed")
}

// TestGuardWrite_WhitespaceComponentLimitation pins a known limit: a
// whitespace-only component followed by .. is not normalised, so the sink does
// not see a home behind it. Collapsing across whitespace would refuse ordinary
// shell text, which the second half checks is stored as written.
func TestGuardWrite_WhitespaceComponentLimitation(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, in := range []string{"cd /home/\t/../jfreeman/x", "/home/\n/../bob/x", "/home/ /../bob/x"} {
		_, err := guardWrite("/r/prompt.md", []byte(in))
		assert.NoError(t, err, in)
	}
	for _, in := range []string{"cd <repo>/docs && make; cd ../../..", "cat ~/notes.txt; cd ../../..; ls"} {
		out, err := guardWrite("/r/prompt.md", []byte(in))
		require.NoError(t, err, in)
		assert.Equal(t, in, string(out))
		out, err = guardWrite("/r/contract.yaml", []byte("mission_id: m-1\ncontext: "+yamlQuote(in)+"\n"))
		require.NoError(t, err, in)
		assert.Contains(t, string(out), in)
	}
}

func yamlQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// TestRedactRecord_RefusesInvalidUTF8: a key or string value that is not valid
// UTF-8 is refused, with the field named: json.Marshal would store U+FFFD.
func TestRedactRecord_RefusesInvalidUTF8(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for name, d := range map[string]map[string]any{
		"value":     {"x": "a\xffb"},
		"key":       {"a\xffb": "v"},
		"surrogate": {"x": "\xed\xa0\x80"},
		"nested":    {"x": []any{map[string]any{"k": "\xc0"}}},
	} {
		_, err := RedactRecord("/work/repo", Event{TS: "t", Event: "e", Actor: "a", Details: d})
		if assert.Error(t, err, name) {
			assert.Contains(t, err.Error(), "not valid UTF-8", name)
		}
	}
	_, err := RedactRecord("/work/repo", Event{TS: "t", Event: "e", Actor: "a", Details: map[string]any{"x": "héllo 😀"}})
	assert.NoError(t, err)
}

// TestRedactRecord_OneNoticePerChangedWrite: a record the passes rewrite
// prints one notice naming the kind of record and never a value; a record they
// leave alone prints none.
func TestRedactRecord_OneNoticePerChangedWrite(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	var err error
	changed := testhelpers.CaptureStderr(t, func() {
		var say Announcement
		_, say, err = RedactRecordNotify("/work/repo", Event{TS: "t", Event: "e", Actor: "a",
			Details: map[string]any{"k": "/home/bob/secret-place", "m": "jane@example.org"}})
		say.Say()
	})
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(changed, "\n"), changed)
	assert.Contains(t, changed, "mission.Event")
	assert.Contains(t, changed, "rewritten")
	assert.NotContains(t, changed, "bob")
	assert.NotContains(t, changed, "jane")

	clean := testhelpers.CaptureStderr(t, func() {
		var say Announcement
		_, say, err = RedactRecordNotify("/work/repo", Event{TS: "t", Event: "e", Actor: "a",
			Details: map[string]any{"k": "v"}})
		say.Say()
	})
	require.NoError(t, err)
	assert.Empty(t, clean)
}

// TestWriteDelegationSkeleton_NoticeForARewrittenPrompt: prompt.md is
// redacted as text, and a change to it is reported like any other.
func TestWriteDelegationSkeleton_NoticeForARewrittenPrompt(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	repo := t.TempDir()
	var err error
	notice := testhelpers.CaptureStderr(t, func() {
		_, err = WriteDelegationSkeleton(repo, "m-2026-01-01-001", "d-1", DelegationSkeleton{
			Prompt: []byte("mail jane@example.org about /home/bob/x"), Tier: "A", Session: "s", AgentType: "bwk",
		})
	})
	require.NoError(t, err)
	assert.Contains(t, notice, "prompt.md")
	assert.NotContains(t, notice, "jane")
	assert.NotContains(t, notice, "bob")
}

// TestCopyIfExists_NoticeWhenTheCopyIsRewritten: migration copies a legacy
// file through the redactor, and says once, naming the file and no value,
// when what it wrote differs from what it read. A file nothing touched is
// copied silently.
func TestCopyIfExists_NoticeWhenTheCopyIsRewritten(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	r, err := NewPathRedactor("/work/repo")
	require.NoError(t, err)
	dir := t.TempDir()
	for name, c := range map[string]struct {
		body    string
		noticed bool
	}{
		"log.jsonl":        {`{"ts":"t","event":"e","actor":"a","details":{"k":"/home/bob/secret-place"}}` + "\n", true},
		"results.yaml":     {"results: []\n", false},
		"reflections.yaml": {"reflections:\n  - round: 1\n    recommendation: continue\n    reason: mail jane@example.org\n", true},
	} {
		src, dst := filepath.Join(dir, "src-"+name), filepath.Join(dir, "dst-"+name)
		require.NoError(t, os.WriteFile(src, []byte(c.body), 0o600))
		notice := testhelpers.CaptureStderr(t, func() {
			require.NoError(t, copyIfExists(r, src, dst))
		})
		if !c.noticed {
			assert.Empty(t, notice, name)
			continue
		}
		assert.Equal(t, 1, strings.Count(notice, "\n"), notice)
		assert.Contains(t, notice, "src-"+name)
		assert.NotContains(t, notice, "bob")
		assert.NotContains(t, notice, "jane")
	}
}

// TestRedactRecord_RenameNoticeNamesTheSuffixOnlyWhenAdded and the fixed
// point: a second pass over a record whose key is the placeholder is silent
// and changes nothing.
func TestRedactRecord_RenameNoticeNamesTheSuffixOnlyWhenAdded(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	ev := func(d map[string]any) Event { return Event{TS: "t", Event: "e", Actor: "a", Details: d} }
	plain := testhelpers.CaptureStderr(t, func() {
		_, say, err := RedactRecordNotify("/work/repo", ev(map[string]any{"/home/zed/a": "1"}))
		require.NoError(t, err)
		say.Say()
	})
	assert.Contains(t, plain, "was renamed")
	assert.NotContains(t, plain, "#N")
	suffixed := testhelpers.CaptureStderr(t, func() {
		_, say, err := RedactRecordNotify("/work/repo", ev(map[string]any{"/home/zed/a": "1", "~/a": "2"}))
		require.NoError(t, err)
		say.Say()
	})
	assert.Contains(t, suffixed, "(name#N)")

	in := ev(map[string]any{"[redacted-secret]": "v", "[redacted-key]#2": "w"})
	var out Event
	again := testhelpers.CaptureStderr(t, func() {
		var err error
		var say Announcement
		out, say, err = RedactRecordNotify("/work/repo", in)
		require.NoError(t, err)
		say.Say()
	})
	assert.Empty(t, again)
	assert.Equal(t, in, out)
}

// TestRedactRecordNotify_PrintsOnlyWhenTheWriteSucceeded: building the record
// prints nothing; the writer says the announcement after its write.
func TestRedactRecordNotify_PrintsOnlyWhenTheWriteSucceeded(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	var say Announcement
	built := testhelpers.CaptureStderr(t, func() {
		var err error
		_, say, err = RedactRecordNotify("/work/repo", Event{TS: "t", Event: "e", Actor: "a",
			Details: map[string]any{"k": "/home/bob/x"}})
		require.NoError(t, err)
	})
	assert.Empty(t, built)
	said := testhelpers.CaptureStderr(t, say.Say)
	assert.Equal(t, 1, strings.Count(said, "\n"))
	assert.Empty(t, testhelpers.CaptureStderr(t, Announcement(nil).Say))

	// A refused record has no announcement, and says only the refusal.
	_, say, err := RedactRecordNotify("/work/repo", Event{TS: "t", Event: "e", Actor: "a",
		Details: map[string]any{"k": "/home/bob/x", "bad": "a\xffb"}})
	require.Error(t, err)
	assert.Nil(t, say)
}

// TestWriteDelegationSkeleton_RefusalsPrintNoNoticeAndWriteNothing: a prompt
// that is not valid UTF-8 is refused as a record is, and a refused skeleton
// leaves no prompt.md behind and no rewrite notice.
func TestWriteDelegationSkeleton_RefusalsPrintNoNoticeAndWriteNothing(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for name, p := range map[string]DelegationSkeleton{
		"prompt":  {Prompt: []byte("see /home/bob/x \xff"), Tier: "A", Session: "s", AgentType: "bwk"},
		"session": {Prompt: []byte("see /home/bob/x"), Tier: "A", Session: "s\xff", AgentType: "bwk"},
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			var err error
			notice := testhelpers.CaptureStderr(t, func() {
				_, err = WriteDelegationSkeleton(repo, "m-2026-01-01-001", "d-1", p)
			})
			require.Error(t, err)
			assert.NotContains(t, notice, "rewritten")
			_, statErr := os.Stat(filepath.Join(DelegationDir(repo, "m-2026-01-01-001", "d-1"), "prompt.md"))
			assert.True(t, os.IsNotExist(statErr), "a refused skeleton writes no prompt.md")
		})
	}
}

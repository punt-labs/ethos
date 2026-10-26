package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardAuditLine: a clean line is returned re-marshalled; a leaking value
// or key is replaced; the result is always a line that names no home.
func TestGuardAuditLine(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	clean := []byte(`{"ts":"1","tool":"Read","tool_input":{"file_path":"~/x"},"zz":1}`)
	var m map[string]any
	require.NoError(t, json.Unmarshal(clean, &m))
	want, err := json.Marshal(m)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(guardAuditLine(clean)), "a clean line is written as its re-marshalled decoded form")
	assert.Equal(t, `{"ts":"1","zz":1}`, string(guardAuditLine([]byte(`{ "zz" : 1 , "ts":"1" }`))),
		"never the input bytes")

	tests := []struct{ name, in string }{
		{"string tool_input", `{"ts":"1","session":"s","tool":"Bash","tool_input":"cat -I/home/alice/x"}`},
		{"unknown cwd", `{"ts":"1","session":"s","cwd":"/home/alice/work"}`},
		{"audit_error", `{"ts":"1","session":"s","audit_error":"open /home/alice/x: denied"}`},
		{"enrichment", `{"ts":"1","session":"s","agent_type":"/home/alice/a"}`},
		{"key", `{"ts":"1","session":"s","x":{"/home/alice/a":"v"}}`},
		{"escaped", `{"ts":"1","session":"s","cwd":"/home/alice"}`},
		{"not json", `torn /home/alice/x`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := guardAuditLine([]byte(tt.in))
			assert.NotContains(t, string(out), "alice")
			assert.NotContains(t, string(out), "zed")
			assert.True(t, json.Valid(out), string(out))
		})
	}
	out := guardAuditLine([]byte(`{"ts":"1","session":"s","cwd":"/home/alice/work","tool":"Read"}`))
	assert.JSONEq(t, `{"ts":"1","session":"s","cwd":"[redacted-path]","tool":"Read","redacted":true}`, string(out))
}

// TestGuardAuditLine_UnknownHomeDropsAbsolutePaths: no home to compare with,
// so every absolute path goes; the line is still written.
func TestGuardAuditLine_UnknownHomeDropsAbsolutePaths(t *testing.T) {
	t.Setenv("HOME", "")
	out := guardAuditLine([]byte(`{"ts":"1","session":"s","cwd":"/etc/hosts","tool":"Read"}`))
	assert.JSONEq(t, `{"ts":"1","session":"s","cwd":"[redacted-path]","tool":"Read","redacted":true}`, string(out))
}

// TestRedactToolInput_CollidingKeysKeepBothValues: two keys that rename to
// one name both survive, the same way every time.
func TestRedactToolInput_CollidingKeysKeepBothValues(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	in := map[string]any{"env": map[string]any{"/home/me/a": "1", "~/a": "2"}}
	for i := 0; i < 20; i++ {
		got, _ := redactToolInput(map[string]any{"tool_input": in}, "Bash", t.TempDir())
		assert.Equal(t, map[string]any{"~/a": "2", "~/a#2": "1"}, got["env"], "the clean key keeps its name")
	}
}

// TestEmitAuditSentinel_ReasonIsGuarded: the loss marker's reason goes through
// the line guard as well as the content pass.
func TestEmitAuditSentinel_ReasonIsGuarded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	live := filepath.Join(t.TempDir(), "live.jsonl")
	require.NoError(t, emitAuditSentinel(live, t.TempDir(), "", "s", time.Now(), "open -I/home/alice/x: denied"))
	data, err := os.ReadFile(live)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "alice")
	assert.Contains(t, string(data), "audit_error")
}

// TestRedactLegacyLines_UnknownFields: migration guards every
// line, including the fields redactLegacyLine does not look at.
func TestRedactLegacyLines_UnknownFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	lines := []rawAuditLine{
		{raw: []byte(`{"ts":"2026-05-22T10:00:00Z","session":"s","tool":"Bash","tool_input":{"command":"ls"},"cwd":"/home/alice/w","audit_error":"open -I/home/alice/x"}`)},
		{raw: []byte(`{"ts":"2026-05-22T10:00:01Z","session":"s","tool":"Read","tool_input":{"file_path":"x"}}`)},
	}
	out, err := redactLegacyLines(lines, repo)
	require.NoError(t, err)
	assert.NotContains(t, string(out[0].raw), "alice")
	var m1 map[string]any
	require.NoError(t, json.Unmarshal(lines[1].raw, &m1))
	want1, err := json.Marshal(m1)
	require.NoError(t, err)
	assert.Equal(t, string(want1), string(out[1].raw), "a clean line is written re-marshalled")
}

// TestEvaluatePreconditions_CountsReadsIgnoredForTheirFormat: a legacy Read
// holding a ~ is ignored, and the block says how many.
func TestEvaluatePreconditions_CountsReadsIgnoredForTheirFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	now := time.Now().UTC()
	dir, err := resolveRepoSessionDir(repo, "s", now)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, writeAuditEntry(filepath.Join(dir, "audit.jsonl"), auditEntry{
		Ts: now.Format(time.RFC3339), Session: "s", Tool: "Read",
		ToolInput: map[string]any{"file_path": "~/notes.md"},
	}))
	_, ignored, _, err := loadSessionReadsCounted(repo, "s")
	require.NoError(t, err)
	assert.Equal(t, 1, ignored)
}

// TestNamesOtherUserHome_SurfacesTheError: with no home to resolve, the
// judgment fails loudly rather than answering no.
func TestNamesOtherUserHome_SurfacesTheError(t *testing.T) {
	t.Setenv("HOME", "")
	_, err := namesOtherUserHome("/etc/x", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "home directory")
}

// TestGuardAuditLine_MarksRewrittenLines: a rewritten line says so, its hash
// is recomputed over the stored tool_input, and its tool_input stays a map.
func TestGuardAuditLine_MarksRewrittenLines(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	in := []byte(`{"ts":"1","session":"s","tool":"Read","tool_input":{"file_path":"-I/home/alice/x"},"tool_input_hash":"old"}`)
	out := guardAuditLine(in)
	var m map[string]any
	require.NoError(t, json.Unmarshal(out, &m))
	assert.Equal(t, true, m["redacted"])
	assert.Equal(t, hashToolInput(map[string]any{"tool_input": m["tool_input"]}), m["tool_input_hash"])
	assert.NotEqual(t, "old", m["tool_input_hash"])
	assert.NotContains(t, string(out), "alice")

	// The stage-2 fallback keeps tool_input a map, so the reader keeps the line.
	_, rule := guardAuditLineWhy([]byte(`{"ts":"1","tool_input":{"a":"b"}}`))
	assert.Empty(t, rule)
	cleaned := guardAuditLine([]byte(`{"ts":"1","session":"s","tool_input":"cat /home/alice/x"}`))
	_, err := decodeAuditLine(cleaned)
	require.NoError(t, err, string(cleaned))
}

// TestSealRepo_RewritesLeakingLiveLines: a live line an older binary appended
// without the guard is rewritten before it reaches the tracked chunk, keeping
// its ts so the chunk still verifies. This closes DES-080's upgrade exception.
func TestSealRepo_RewritesLeakingLiveLines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-old"
	live := liveAuditPath(repo, sid)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	body := `{"ts":"` + audit.FormatLineTS(100) + `","session":"` + sid + `","tool":"Read","cwd":"/home/alice/work"}` + "\n" +
		`{"ts":"` + audit.FormatLineTS(200) + `","session":"` + sid + `","tool":"Read"}` + "\n"
	require.NoError(t, os.WriteFile(live, []byte(body), 0o600))

	_, err := SealRepo(repo, time.Now().UTC(), SealOptions{})
	require.NoError(t, err)
	dir, err := audit.FindSealedSessionDir(repo, sid)
	require.NoError(t, err)
	chunk := filepath.Join(dir, audit.SessionChunkFile(100, 200))
	data, err := os.ReadFile(chunk)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "alice")
	lines, err := audit.ReadChunkVerified(chunk, 200)
	require.NoError(t, err)
	assert.Len(t, lines, 2)
}

// TestGuardAuditLine_CaseCollidingKeys: keys that differ only in case would
// let re-marshalling change which one a reader takes as ts, so the line
// becomes a loss marker that keeps the ts the input was read with.
func TestGuardAuditLine_CaseCollidingKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	in := []byte(`{"ts":"` + audit.FormatLineTS(100) + `","TS":"` + audit.FormatLineTS(200) + `","session":"s","tool":"Read"}`)
	out, rule := guardAuditLineWhy(in)
	assert.Contains(t, rule, "case")
	var before, after struct {
		TS string `json:"ts"`
	}
	require.NoError(t, json.Unmarshal(in, &before))
	require.NoError(t, json.Unmarshal(out, &after))
	assert.Equal(t, before.TS, after.TS, "the marker keeps the ts the input was read with")
	assert.Contains(t, string(out), "audit_error")
}

// TestSealRepo_CaseCollidingTsVerifies: the probe line goes through seal and
// the sealed chunk verifies against its name.
func TestSealRepo_CaseCollidingTsVerifies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-case"
	live := liveAuditPath(repo, sid)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	body := `{"ts":"` + audit.FormatLineTS(100) + `","session":"` + sid + `","tool":"Read"}` + "\n" +
		`{"ts":"` + audit.FormatLineTS(150) + `","TS":"` + audit.FormatLineTS(200) + `","session":"` + sid + `","tool":"Read"}` + "\n"
	require.NoError(t, os.WriteFile(live, []byte(body), 0o600))

	_, err := SealRepo(repo, time.Now().UTC(), SealOptions{})
	require.NoError(t, err)
	dir, err := audit.FindSealedSessionDir(repo, sid)
	require.NoError(t, err)
	_, err = audit.ReadChunkVerified(filepath.Join(dir, audit.SessionChunkFile(100, 200)), 200)
	require.NoError(t, err)
}

// TestGuardAuditLine_KeepsCaseVariantKeysInsideToolInput: tool_input is a map,
// so keys that differ only in case there (HTTP headers) are legitimate, and
// the line is kept intact, also through migration.
func TestGuardAuditLine_KeepsCaseVariantKeysInsideToolInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	in := []byte(`{"ts":"` + audit.FormatLineTS(100) + `","session":"s","tool":"WebFetch","tool_input":{"headers":{"Content-Type":"a","content-type":"b"}}}`)
	out, rule := guardAuditLineWhy(in)
	assert.Empty(t, rule)
	assert.Contains(t, string(out), `"Content-Type":"a"`)
	assert.Contains(t, string(out), `"content-type":"b"`)

	lines, err := redactLegacyLines([]rawAuditLine{{raw: in}}, t.TempDir())
	require.NoError(t, err)
	assert.Contains(t, string(lines[0].raw), `"Content-Type":"a"`)
	assert.Contains(t, string(lines[0].raw), `"content-type":"b"`)
	assert.NotContains(t, string(lines[0].raw), "audit_error")
}

// TestGuardAuditLine_UnicodeFoldedHeaderKeys: encoding/json folds ſ onto s,
// so ſession collides with session and the line becomes a loss marker that
// keeps the session a reader took from the input.
func TestGuardAuditLine_UnicodeFoldedHeaderKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	in := []byte(`{"ts":"` + audit.FormatLineTS(100) + `","session":"a","ſession":"b","tool":"Read"}`)
	out, rule := guardAuditLineWhy(in)
	assert.NotEmpty(t, rule)
	assert.Contains(t, string(out), "audit_error")
	var before, after auditEntry
	require.NoError(t, json.Unmarshal(in, &before))
	require.NoError(t, json.Unmarshal(out, &after))
	assert.Equal(t, before.Ts, after.Ts)
	assert.Equal(t, before.Session, after.Session)
}

// TestGuardAuditLine_NoticeSaysLossMarker: the stderr notice tells a whole-line
// replacement from a value swap.
func TestGuardAuditLine_NoticeSaysLossMarker(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	marker := testhelpers.CaptureStderr(t, func() {
		guardAuditLine([]byte(`{"ts":"t","TS":"u"}`))
	})
	assert.Contains(t, marker, "replaced by a loss marker (")
	swap := testhelpers.CaptureStderr(t, func() {
		guardAuditLine([]byte(`{"ts":"t","cwd":"/home/bob/x"}`))
	})
	assert.NotContains(t, swap, "loss marker")
	assert.Contains(t, swap, "rewritten")
}

// TestGuardAuditLine_UndecodableHeader: a line whose header does not decode
// into auditEntry is not stored as it is. A non-map tool_input is coerced to
// {} and the line marked redacted; a header still undecodable after that
// becomes a loss marker.
func TestGuardAuditLine_UndecodableHeader(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := audit.FormatLineTS(100)
	out, rule := guardAuditLineWhy([]byte(`{"ts":"` + ts + `","session":"s","tool":"Bash","tool_input":"ls"}`))
	assert.Contains(t, rule, "has a header a reader cannot decode")
	e, err := decodeAuditLine(out)
	require.NoError(t, err)
	assert.Equal(t, ts, e.Ts)
	assert.True(t, e.Redacted)
	assert.Empty(t, e.ToolInput)

	out, rule = guardAuditLineWhy([]byte(`{"ts":"` + ts + `","session":"s","tool":"Read","path_format":"x"}`))
	assert.Contains(t, rule, "has a header a reader cannot decode")
	assert.Contains(t, string(out), "audit_error")
	e, err = decodeAuditLine(out)
	require.NoError(t, err)
	assert.Equal(t, ts, e.Ts)
}

// TestGuardAuditLine_CaseVariantOfARewrittenField: a key that the guard would
// write in its own spelling, present in another case, makes the line a loss
// marker; and guarding any output a second time returns it unchanged.
func TestGuardAuditLine_CaseVariantOfARewrittenField(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	ts := audit.FormatLineTS(100)
	for _, in := range []string{
		`{"ts":"` + ts + `","session":"s","Redacted":false,"tool_input":"ls"}`,
		`{"ts":"` + ts + `","session":"s","Tool_Input_Hash":"x","cwd":"/home/bob/x"}`,
		`{"ts":"` + ts + `","session":"s","TOOL_INPUT":{"a":"/home/bob"}}`,
	} {
		out, rule := guardAuditLineWhy([]byte(in))
		assert.Contains(t, rule, "case-variant of a field ethos rewrites", in)
		assert.Contains(t, string(out), "audit_error", in)
	}
	for _, in := range []string{
		`{"ts":"` + ts + `","session":"s","Redacted":false,"tool_input":"ls"}`,
		`{"ts":"` + ts + `","session":"s","Tool_Input_Hash":"x","cwd":"/home/bob/x"}`,
		`{"ts":"` + ts + `","session":"s","tool":"Bash","tool_input":"ls"}`,
		`{"ts":"` + ts + `","session":"s","tool_input":{"a":"/home/bob/x"},"tool_input_hash":"h"}`,
		`{"ts":"` + ts + `","TS":"x"}`,
		`{"ts":"` + ts + `","session":"s","path_format":"x"}`,
		`{"ts":"` + ts + `","session":"s","tool":"Read","tool_input":{"Content-Type":"a","content-type":"b"}}`,
		`not json`,
	} {
		once := guardAuditLine([]byte(in))
		assert.Equal(t, string(once), string(guardAuditLine(once)), "idempotent for %s", in)
	}
}

// TestGuardAuditLine_FreeKeyNeverCreatesACollision: renaming a leaking key
// must not produce a key that differs from another only in case, or a second
// guard pass would find a collision the first created.
func TestGuardAuditLine_FreeKeyNeverCreatesACollision(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	in := []byte(`{"ts":"` + audit.FormatLineTS(100) + `","session":"s","[Redacted-Key]":1,"/home/bob":2}`)
	once := guardAuditLine(in)
	assert.Equal(t, string(once), string(guardAuditLine(once)), "idempotent")
	var m map[string]any
	require.NoError(t, json.Unmarshal(once, &m))
	assert.False(t, caseCollision(m), string(once))
}

// TestGuardAuditLine_CleanKeysKeepTheirNames: a renamed key never takes the
// name a clean key already has, whatever the order they sort in.
func TestGuardAuditLine_CleanKeysKeepTheirNames(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	in := []byte(`{"ts":"` + audit.FormatLineTS(100) + `","session":"s","[REDACTED-KEY]":1,"/home/bob/a":2,"tool":"x"}`)
	once := guardAuditLine(in)
	var m map[string]any
	require.NoError(t, json.Unmarshal(once, &m))
	assert.EqualValues(t, 1, m["[REDACTED-KEY]"], string(once))
	assert.EqualValues(t, 2, m["[redacted-key]#2"], "the leaking key is renamed and takes the suffix")
	assert.Equal(t, string(once), string(guardAuditLine(once)), "idempotent")
}

// TestGuardAuditLine_DuplicateKeys: an exact duplicate key keeps the last
// value, the one encoding/json takes, and the line is rewritten with a rule so
// the notice fires; the result is a fixed point.
func TestGuardAuditLine_DuplicateKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := audit.FormatLineTS(100)
	in := []byte(`{"ts":"` + ts + `","session":"a","session":"b","tool":"Read"}`)
	out, rule := guardAuditLineWhy(in)
	assert.Equal(t, `holds duplicate key "session"`, rule)
	var e auditEntry
	require.NoError(t, json.Unmarshal(out, &e))
	assert.Equal(t, "b", e.Session, "the last value, as a reader takes it")
	assert.Equal(t, string(out), string(guardAuditLine(out)))
	notice := testhelpers.CaptureStderr(t, func() { guardAuditLine(in) })
	assert.Contains(t, notice, `duplicate key "session"`)

	nested := []byte(`{"ts":"` + ts + `","session":"s","tool_input":{"a":1,"a":2}}`)
	_, rule = guardAuditLineWhy(nested)
	assert.Equal(t, `holds duplicate key "a"`, rule)
}

// TestRedactToolInput_RenamedKeysKeepEveryValue: keys that the passes rename
// never merge, nothing is lost, the result is the same on every run, and the
// rename is reported so the line is marked.
func TestRedactToolInput_RenamedKeysKeepEveryValue(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	in := map[string]any{"tool_input": map[string]any{"env": map[string]any{
		"/home/bob/x": "BOB", "/home/alice/x": "ALICE", "/Users/carol/x": "CAROL",
	}}}
	var first map[string]any
	for i := 0; i < 200; i++ {
		got, marked := redactToolInput(in, "Bash", t.TempDir())
		assert.True(t, marked, "a renamed key is reported")
		vals := map[any]bool{}
		for _, v := range got["env"].(map[string]any) {
			vals[v] = true
		}
		require.Equal(t, map[any]bool{"BOB": true, "ALICE": true, "CAROL": true}, vals)
		if first == nil {
			first = got
		}
		require.Equal(t, first, got)
	}

	got, marked := redactToolInput(map[string]any{"tool_input": map[string]any{"env": map[string]any{
		"/home/zed/x": "LEAKY", "~/x": "CLEAN",
	}}}, "Bash", t.TempDir())
	assert.True(t, marked)
	assert.Equal(t, "CLEAN", got["env"].(map[string]any)["~/x"], "a clean key keeps its value")
	assert.Equal(t, "LEAKY", got["env"].(map[string]any)["~/x#2"])
}

// TestGuardAuditLine_InvalidTextIsNeverReplaced: a line with invalid UTF-8 or
// an unpaired surrogate escape is rewritten with a rule or becomes a loss
// marker; no U+FFFD substitution reaches the file silently.
func TestGuardAuditLine_InvalidTextIsNeverReplaced(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := audit.FormatLineTS(100)
	for name, in := range map[string]string{
		"invalid utf-8":      `{"ts":"` + ts + `","session":"s","cwd":"a` + "\xff" + `b"}`,
		"lone surrogate":     `{"ts":"` + ts + `","session":"s","cwd":"\ud800"}`,
		"lone low surrogate": `{"ts":"` + ts + `","session":"s","cwd":"x\udc00y"}`,
	} {
		out, rule := guardAuditLineWhy([]byte(in))
		assert.NotEmpty(t, rule, name)
		assert.NotContains(t, string(out), "\uFFFD", name)
		assert.True(t, utf8.Valid(out), name)
		assert.True(t, json.Valid(out), name)
	}
	_, rule := guardAuditLineWhy([]byte(`{"ts":"` + ts + `","session":"s","cwd":"😀 ok"}`))
	assert.Empty(t, rule, "a paired surrogate is fine")
}

// TestGuardAuditLine_NoticeQuotesKeysWithQ: a key with control characters
// cannot forge a line of the notice, and rules accumulate.
func TestGuardAuditLine_NoticeQuotesKeysWithQ(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	ts := audit.FormatLineTS(100)
	evil := `{"ts":"` + ts + `","session":"s","\u001b[31mevil\nethos: forged":1,"\u001b[31mevil\nethos: forged":2}`
	notice := testhelpers.CaptureStderr(t, func() { guardAuditLine([]byte(evil)) })
	for _, l := range strings.Split(strings.TrimSuffix(notice, "\n"), "\n") {
		assert.True(t, strings.HasPrefix(l, "ethos: audit:"), "forged notice line %q", l)
	}
	assert.Contains(t, notice, `\x1b[31mevil\nethos: forged`)

	_, rule := guardAuditLineWhy([]byte(`{"ts":"` + ts + `","session":"s","a":1,"a":2,"cwd":"/home/bob/x"}`))
	assert.Contains(t, rule, `holds duplicate key "a"`)
	assert.Contains(t, rule, "/home/ or /Users/", "both rules are named")

	_, rule = guardAuditLineWhy([]byte(`{"ts":"` + ts + `","session":"s","":1,"":2}`))
	assert.Contains(t, rule, `holds duplicate key ""`)
}

// TestGuardAuditLine_DuplicateKeyShapes: a duplicate written with an escape,
// and one inside an object in an array.
func TestGuardAuditLine_DuplicateKeyShapes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := audit.FormatLineTS(100)
	_, rule := guardAuditLineWhy([]byte(`{"ts":"` + ts + `","session":"s","ab":1,"ab":2}`))
	assert.Contains(t, rule, `holds duplicate key "ab"`)
	_, rule = guardAuditLineWhy([]byte(`{"ts":"` + ts + `","session":"s","tool_input":{"x":[{"k":1,"k":2}]}}`))
	assert.Contains(t, rule, `holds duplicate key "k"`)
}

// TestRedactLegacyLines_DuplicateKeyInTheOriginalBytes: migration sees a
// duplicate key in the bytes as they were, keeps the last value, and reports.
func TestRedactLegacyLines_DuplicateKeyInTheOriginalBytes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw := []byte(`{"ts":"2026-05-22T10:00:00Z","session":"a","session":"b","tool":"Read","tool_input":{"file_path":"x"}}`)
	var out []rawAuditLine
	notice := testhelpers.CaptureStderr(t, func() {
		var err error
		out, err = redactLegacyLines([]rawAuditLine{{raw: raw}}, t.TempDir())
		require.NoError(t, err)
		sayMigrateNotes(out)
	})
	assert.Contains(t, notice, "duplicate key 1 (line 1)")
	e, err := decodeAuditLine(out[0].raw)
	require.NoError(t, err)
	assert.Equal(t, "b", e.Session)
	assert.True(t, e.Redacted)
}

// TestGuardAuditLine_NoticeRedactsTheKey: a key quoted in a notice has been
// through the prose redaction.
func TestGuardAuditLine_NoticeRedactsTheKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := audit.FormatLineTS(100)
	_, rule := guardAuditLineWhy([]byte(`{"ts":"` + ts + `","session":"s","a@b.org":1,"a@b.org":2}`))
	assert.NotContains(t, rule, "a@b.org")
	assert.Contains(t, rule, "duplicate key")
}

// TestGuardAuditLine_EscapedQuoteIsNotPartOfAValue: a string that holds a
// JSON document with a key /home/ is escaped on the line (/home/\"), which is
// not a leak; judging the raw bytes would wipe tool_input for nothing.
func TestGuardAuditLine_EscapedQuoteIsNotPartOfAValue(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	in := []byte(`{"ts":"` + audit.FormatLineTS(100) + `","session":"s","tool":"Bash","tool_input":{"a":{"/home/":"0"}},"tool_input_preview":"{\"a\":{\"/home/\":\"0\"}}"}`)
	out, rule := guardAuditLineWhy(in)
	assert.Empty(t, rule)
	assert.Contains(t, string(out), `"tool_input":{"a":{"/home/":"0"}}`)
}

// TestSinksAgreeOnTheSameStrings: the mission sink and the audit guard judge
// decoded strings and keys, so both treat a string the same way: text like
// grep "/home/" x, whose encoded form holds /home/\" , is accepted by both,
// and a real home is refused (or replaced) by both.
func TestSinksAgreeOnTheSameStrings(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	table := []struct {
		in   string
		leak bool
	}{
		{`grep "/home/" x`, false},
		{`echo '/home/'`, false},
		{"plain text", false},
		{"cd /home/bob/x", true},
		{`say "/home/bob"`, true},
		{"/home/zed/x", true},
		{"-home-bob-proj-", true},
		{"~/x", false},
		{"a\\b", false},
	}
	for _, tt := range table {
		rec, _ := json.Marshal(map[string]any{"ts": "t", "event": "e", "actor": "a", "details": map[string]any{"x": tt.in}})
		_, sinkErr := mission.CheckBytes("/r/log.jsonl", append(rec, '\n'))
		line, _ := json.Marshal(map[string]any{"ts": audit.FormatLineTS(100), "session": "s", "x": tt.in})
		_, rule := guardAuditLineWhy(line)
		assert.Equal(t, tt.leak, sinkErr != nil, "mission sink on %q", tt.in)
		assert.Equal(t, tt.leak, rule != "", "audit guard on %q (rule %q)", tt.in, rule)
	}
}

// TestRedactLegacyLines_DuplicateKeyKeepsOwnHomeAsTilde: a duplicate key does
// not make the line lose what the path pass would have kept; this machine's
// home becomes ~/x, not a removed value.
func TestRedactLegacyLines_DuplicateKeyKeepsOwnHomeAsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	raw := []byte(`{"ts":"2026-05-22T10:00:00Z","session":"a","session":"b","tool":"Read","tool_input":{"file_path":"` + home + `/x"}}`)
	out, err := redactLegacyLines([]rawAuditLine{{raw: raw}}, t.TempDir())
	require.NoError(t, err)
	assert.Contains(t, string(out[0].raw), `"file_path":"~/x"`)
	assert.NotContains(t, string(out[0].raw), home)
	e, err := decodeAuditLine(out[0].raw)
	require.NoError(t, err)
	assert.Equal(t, "b", e.Session)
	assert.True(t, e.Redacted)
}

// TestRedactLegacyLines_InvalidTextInTheOriginalBytes: a lone surrogate escape
// or invalid UTF-8 is judged on the bytes as they were, as the sink does.
func TestRedactLegacyLines_InvalidTextInTheOriginalBytes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for name, raw := range map[string][]byte{
		"surrogate": []byte(`{"ts":"2026-05-22T10:00:00Z","session":"a","tool":"Read","tool_input":{"file_path":"\ud800x"}}`),
		"utf8":      []byte("{\"ts\":\"2026-05-22T10:00:00Z\",\"session\":\"a\",\"tool\":\"Read\",\"tool_input\":{\"file_path\":\"\xffx\"}}"),
	} {
		t.Run(name, func(t *testing.T) {
			var out []rawAuditLine
			notice := testhelpers.CaptureStderr(t, func() {
				var err error
				out, err = redactLegacyLines([]rawAuditLine{{raw: raw}}, t.TempDir())
				require.NoError(t, err)
				sayMigrateNotes(out)
			})
			assert.Contains(t, notice, "loss marker")
			assert.Contains(t, string(out[0].raw), "audit_error")
			assert.NotContains(t, string(out[0].raw), "tool_input")
		})
	}
}

// TestGuardAuditLine_IsNotACredentialDetector: the sink judges homes and
// users only (DES-080). Credential-shaped text is the writers' business, and a
// line the guard has no home rule against is stored as it came, so a
// credential-looking string in a command or an address in a field is not a
// false positive that rewrites the line.
func TestGuardAuditLine_IsNotACredentialDetector(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	head := `{"ts":"` + audit.FormatLineTS(100) + `","session":"s","tool":"Bash",`
	for _, body := range []string{
		`"tool_input":{"command":"export MAX_TOKENS=4096; git log --author=jane@example.org"}}`,
		`"tool_input":{"command":"curl -H 'Authorization: Bearer abcdefghijklmnop'"}}`,
		`"tool_input":{"api_key":"hunter2xyz"}}`,
		`"note":"mail bob@example.org"}`,
		`"tool_input_preview":"token=abc"}`,
	} {
		in := []byte(head + body)
		out, rule := guardAuditLineWhy(in)
		assert.Empty(t, rule, body)
		var a, b map[string]any
		require.NoError(t, json.Unmarshal(in, &a))
		require.NoError(t, json.Unmarshal(out, &b))
		assert.Equal(t, a, b, body)
	}
}

// TestRedactToolInput_NoticeWhenContentIsRemoved: removing a credential from a
// tool_input is a change to what is stored, so it is reported, naming the tool
// and never the value; an input nothing touched is silent.
func TestRedactToolInput_NoticeWhenContentIsRemoved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var removed bool
	notice := testhelpers.CaptureStderr(t, func() {
		_, removed = redactToolInput(map[string]any{"tool_input": map[string]any{
			"command": "curl -H 'Authorization: Bearer abcdefghijklmnop'"}}, "Bash", "/work/repo")
	})
	assert.True(t, removed)
	assert.Equal(t, 1, strings.Count(notice, "\n"), notice)
	assert.Contains(t, notice, "Bash")
	assert.NotContains(t, notice, "abcdefghijklmnop")

	quiet := testhelpers.CaptureStderr(t, func() {
		_, removed = redactToolInput(map[string]any{"tool_input": map[string]any{"command": "ls"}}, "Bash", "/work/repo")
	})
	assert.False(t, removed)
	assert.Empty(t, quiet)
}

// TestRedactLegacyLines_OneNoticePerChangedLine: migrating a file prints one
// notice for the file, counting the lines the migration rewrote by kind with
// their numbers, and never a value; a line left alone is not counted.
func TestRedactLegacyLines_OneNoticePerChangedLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ts := "2026-05-22T10:00:00Z"
	lines := []rawAuditLine{
		{raw: []byte(`{"ts":"` + ts + `","session":"a","tool":"Read","tool_input":{"file_path":"` + home + `/x"}}`)},
		{raw: []byte(`{"ts":"` + ts + `","session":"a","tool":"Bash","tool_input":{"command":"curl -H 'Authorization: Bearer abcdefghijklmnop'"}}`)},
		{raw: []byte(`{"ts":"` + ts + `","session":"a","tool":"Bash","audit_error":"mail jane@example.org"}`)},
		{raw: []byte(`{"ts":"` + ts + `","session":"a","tool":"Read","tool_input":{"file_path":"x"}}`)},
	}
	notice := testhelpers.CaptureStderr(t, func() {
		out, err := redactLegacyLines(lines, t.TempDir())
		require.NoError(t, err)
		sayMigrateNotes(out)
	})
	assert.Equal(t, 1, strings.Count(notice, "\n"), notice)
	assert.Contains(t, notice, "audit migrate")
	assert.Contains(t, notice, "3 of 4 line(s)")
	assert.Contains(t, notice, "home path or content 3 (lines 1, 2, 3)")
	assert.NotContains(t, notice, "abcdefghijklmnop")
	assert.NotContains(t, notice, "jane")
	assert.NotContains(t, notice, home)
}

// TestAppender_OneNoticePerStoredLine: building the entry prints nothing; the
// writer prints one notice after the line is stored, and none when the write
// fails. A home path rewritten in place counts, as it does in RedactRecord
// and migrate, though it removes no content (redacted stays false).
func TestAppender_OneNoticePerStoredLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	payload := func(session string) []byte {
		b, err := json.Marshal(map[string]any{
			"session_id": session, "tool_name": "Read",
			"tool_input": map[string]any{"file_path": filepath.Join(home, "x")},
		})
		require.NoError(t, err)
		return b
	}
	var built auditEntry
	var rewritten bool
	quiet := testhelpers.CaptureStderr(t, func() {
		built, rewritten = buildAuditEntryNote(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(home, "x")}}, "s", "/work/repo", time.Now())
	})
	assert.Empty(t, quiet)
	assert.True(t, rewritten)
	assert.False(t, built.Redacted)

	dir := t.TempDir()
	notice := testhelpers.CaptureStderr(t, func() {
		require.NoError(t, HandleAuditLog(bytes.NewReader(payload("s1")), "", dir))
	})
	assert.Equal(t, 1, strings.Count(notice, "\n"), notice)
	assert.Contains(t, notice, "Read")
	assert.NotContains(t, notice, home)

	clean := testhelpers.CaptureStderr(t, func() {
		b, _ := json.Marshal(map[string]any{"session_id": "s2", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
		require.NoError(t, HandleAuditLog(bytes.NewReader(b), "", dir))
	})
	assert.Empty(t, clean)

	blocked := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.Mkdir(blocked, 0o000))
	defer func() { _ = os.Chmod(blocked, 0o700) }()
	failed := testhelpers.CaptureStderr(t, func() {
		require.NoError(t, HandleAuditLog(bytes.NewReader(payload("s3")), "", blocked))
	})
	assert.NotContains(t, failed, "was rewritten")
}

// TestLossMarker_NamesTheCause: the marker's audit_error says why the line
// could not be stored, in words that name no value, not always [redacted-path].
func TestLossMarker_NamesTheCause(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := audit.FormatLineTS(100)
	head := `{"ts":"` + ts + `","session":"s"`
	cases := map[string]struct{ line, kind string }{
		"invalid text":   {head + `,"cwd":"\ud800"}`, "invalid text"},
		"case collision": {head + `,"TS":"x"}`, "keys that differ only in case"},
		"not an object":  {`not json`, "not a JSON object"},
		"case variant":   {head + `,"Redacted":false,"x":1,"ſession":"b"}`, ""},
	}
	for name, c := range cases {
		out, _, marker := guardAuditLineFull([]byte(c.line))
		if !marker {
			continue // the case-variant line may be rewritten rather than replaced
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(out, &m), name)
		got, _ := m["audit_error"].(string)
		assert.NotEmpty(t, got, name)
		if c.kind != "" {
			assert.Contains(t, got, c.kind, name)
			assert.NotEqual(t, redactedPath, got, name)
		}
	}
}

// TestGuardTail_NamesALossMarkerAsALoss: seal says a line was replaced, not
// merely rewritten.
func TestGuardTail_NamesALossMarkerAsALoss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := audit.FormatLineTS(100)
	notice := testhelpers.CaptureStderr(t, func() {
		_, notes := guardTail([][]byte{[]byte(`{"ts":"` + ts + `","session":"s","cwd":"\ud800"}`)}, "label")
		printNotices(notes)
	})
	assert.Contains(t, notice, "replaced by a loss marker")
	assert.Equal(t, 1, strings.Count(notice, "\n"))
}

// TestAudit_KeepsABashCommandHoldingTheOwnHomeSlug: the audit hook stores the
// command with -~ for the dash home, and the guard leaves that line alone.
func TestAudit_KeepsABashCommandHoldingTheOwnHomeSlug(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	e := buildAuditEntry(map[string]any{"tool_name": "Bash",
		"tool_input": map[string]any{"command": "cd -home-zed && ls ~/.claude/projects/-home-zed/abc.jsonl"}},
		"s", "/work/repo", time.Now())
	assert.Equal(t, "cd -~ && ls ~/.claude/projects/-~/abc.jsonl", e.ToolInput["command"])
	line, err := json.Marshal(e)
	require.NoError(t, err)
	_, rule := guardAuditLineWhy(line)
	assert.Empty(t, rule)
}

// credentialLiveLine is a live audit line an older binary wrote: a credential in
// tool_input, with the hash and preview of that input.
func credentialLiveLine(ts int64, sid string) string {
	in := map[string]any{"api_key": "hunter2"}
	env := map[string]any{"tool_input": in}
	b, _ := json.Marshal(map[string]any{
		"ts": audit.FormatLineTS(ts), "session": sid, "tool": "Bash", "tool_input": in,
		"tool_input_hash": hashToolInput(env), "tool_input_preview": toolInputPreview(env),
	})
	return string(b) + "\n"
}

// assertRedactedWithMatchingHash checks a chunk line: no credential, and the
// hash and preview are those of the stored tool_input.
func assertRedactedWithMatchingHash(t *testing.T, line []byte) {
	t.Helper()
	assert.NotContains(t, string(line), "hunter2")
	var e struct {
		ToolInput map[string]any `json:"tool_input"`
		Hash      string         `json:"tool_input_hash"`
		Preview   string         `json:"tool_input_preview"`
	}
	require.NoError(t, json.Unmarshal(line, &e))
	env := map[string]any{"tool_input": e.ToolInput}
	assert.Equal(t, hashToolInput(env), e.Hash, "the hash is over the stored input")
	assert.Equal(t, toolInputPreview(env), e.Preview, "the preview is of the stored input")
}

// TestSealRepo_RedactsACredentialInAnOlderLiveLine: the guard is home-only, so a
// live tail written before the credential pass existed gets the audit content
// pass first, as migrate gives a legacy line; a clean line is unchanged byte for
// byte.
func TestSealRepo_RedactsACredentialInAnOlderLiveLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-cred"
	cleanLine, err := json.Marshal(map[string]any{
		"ts": audit.FormatLineTS(200), "session": sid, "tool": "Read",
		"tool_input": map[string]any{"file_path": "a.go"},
	})
	require.NoError(t, err)
	cleanLine, _, _ = guardAuditLineFull(cleanLine) // the form the writer stores
	live := liveAuditPath(repo, sid)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	body := credentialLiveLine(100, sid) + string(cleanLine) + "\n"
	require.NoError(t, os.WriteFile(live, []byte(body), 0o600))

	_, err = SealRepo(repo, time.Now().UTC(), SealOptions{})
	require.NoError(t, err)
	dir, err := audit.FindSealedSessionDir(repo, sid)
	require.NoError(t, err)
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	var chunk []byte
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			chunk, err = os.ReadFile(filepath.Join(dir, e.Name()))
			require.NoError(t, err)
		}
	}
	lines := strings.Split(strings.TrimSpace(string(chunk)), "\n")
	require.Len(t, lines, 2)
	assertRedactedWithMatchingHash(t, []byte(lines[0]))
	assert.Equal(t, string(cleanLine), lines[1], "a clean line is unchanged byte for byte")
}

// TestQuarantineChunk_RecoveryRedactsACredentialInALiveLine: the live lines
// quarantine re-seals get the same pass.
func TestQuarantineChunk_RecoveryRedactsACredentialInALiveLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-cred-q"
	sealedDir, err := resolveRepoSessionDir(repo, sid, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sealedDir, 0o700))
	chunk := audit.SessionChunkFile(100, 300)
	require.NoError(t, os.WriteFile(filepath.Join(sealedDir, chunk), []byte("\x00garbage\n"), 0o600))
	live := liveAuditPath(repo, sid)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte(credentialLiveLine(150, sid)), 0o600))

	_, err = QuarantineChunk(repo, filepath.Join(sealedDir, chunk), "bad")
	require.NoError(t, err)
	ents, err := os.ReadDir(sealedDir)
	require.NoError(t, err)
	found := 0
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			b, err := os.ReadFile(filepath.Join(sealedDir, e.Name()))
			require.NoError(t, err)
			for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				assertRedactedWithMatchingHash(t, []byte(l))
				found++
			}
		}
	}
	assert.Equal(t, 1, found, "the recovered line is in a sealed chunk")
}

// olderLiveLine is a live line an older binary wrote with extra top-level fields.
func olderLiveLine(ts int64, sid string, extra map[string]any) string {
	m := map[string]any{"ts": audit.FormatLineTS(ts), "session": sid, "tool": "Read", "tool_input": map[string]any{"file_path": "a.go"}}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b) + "\n"
}

// sealedLines seals the repo and returns every line of every sealed chunk of sid.
func sealedLines(t *testing.T, repo, sid string) []string {
	t.Helper()
	_, err := SealRepo(repo, time.Now().UTC(), SealOptions{})
	require.NoError(t, err)
	dir, err := audit.FindSealedSessionDir(repo, sid)
	require.NoError(t, err)
	return chunkLines(t, dir)
}

func chunkLines(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			require.NoError(t, err)
			out = append(out, strings.Split(strings.TrimSpace(string(b)), "\n")...)
		}
	}
	return out
}

// TestSessionLineGuard_NoHomeStillRedactsCredentials: the credential and
// address passes need no home, so they run when no path redactor can be built.
func TestSessionLineGuard_NoHomeStillRedactsCredentials(t *testing.T) {
	t.Setenv("HOME", "")
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-nohome"
	live := liveAuditPath(repo, sid)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte(credentialLiveLine(100, sid)), 0o600))
	_, err := mission.NewPathRedactor(repo)
	require.Error(t, err, "no usable home: no path redactor")
	for _, l := range sealedLines(t, repo, sid) {
		assert.NotContains(t, l, "hunter2")
	}
}

// TestSessionLineGuard_RedactsHeadersAndUnknownFields: an address in a header and
// a credential under an unknown field of an older live line are redacted when it
// is sealed and when quarantine re-seals it; a clean line is unchanged.
func TestSessionLineGuard_RedactsHeadersAndUnknownFields(t *testing.T) {
	token := "ghp_" + strings.Repeat("aBcD1234", 4) + "aBcD"
	extra := map[string]any{"agent_id": "jane@example.com", "x_token": token}
	check := func(t *testing.T, lines []string) {
		t.Helper()
		require.NotEmpty(t, lines)
		for _, l := range lines {
			assert.NotContains(t, l, "jane@example.com")
			assert.NotContains(t, l, token)
		}
	}

	t.Run("seal", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		repo := t.TempDir()
		initGitRepo(t, repo)
		sid := "sess-hdr"
		cleanLine, err := json.Marshal(map[string]any{"ts": audit.FormatLineTS(200), "session": sid, "tool": "Read", "tool_input": map[string]any{"file_path": "a.go"}})
		require.NoError(t, err)
		cleanLine, _, _ = guardAuditLineFull(cleanLine)
		live := liveAuditPath(repo, sid)
		require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
		require.NoError(t, os.WriteFile(live, []byte(olderLiveLine(100, sid, extra)+string(cleanLine)+"\n"), 0o600))
		lines := sealedLines(t, repo, sid)
		require.Len(t, lines, 2)
		check(t, lines[:1])
		assert.Equal(t, string(cleanLine), lines[1], "a clean line is unchanged byte for byte")
	})

	t.Run("quarantine", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		repo := t.TempDir()
		initGitRepo(t, repo)
		sid := "sess-hdr-q"
		sealedDir, err := resolveRepoSessionDir(repo, sid, time.Now().UTC())
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(sealedDir, 0o700))
		chunk := audit.SessionChunkFile(100, 300)
		require.NoError(t, os.WriteFile(filepath.Join(sealedDir, chunk), []byte("\x00garbage\n"), 0o600))
		live := liveAuditPath(repo, sid)
		require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
		require.NoError(t, os.WriteFile(live, []byte(olderLiveLine(150, sid, extra)), 0o600))
		_, err = QuarantineChunk(repo, filepath.Join(sealedDir, chunk), "bad")
		require.NoError(t, err)
		check(t, chunkLines(t, sealedDir))
	})
}

// TestSessionLineGuard_UnusableHomeRedactsAShortAssignment: with a relative HOME
// (no path redactor) an older live Bash line holding GITHUB_TOKEN=abc and no
// home path is sealed with the value gone.
func TestSessionLineGuard_UnusableHomeRedactsAShortAssignment(t *testing.T) {
	t.Setenv("HOME", "relative/home")
	repo := t.TempDir()
	initGitRepo(t, repo)
	_, err := mission.NewPathRedactor(repo)
	require.Error(t, err, "an unusable home: no path redactor")
	sid := "sess-short"
	live := liveAuditPath(repo, sid)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	b, err := json.Marshal(map[string]any{
		"ts": audit.FormatLineTS(100), "session": sid, "tool": "Bash",
		"tool_input": map[string]any{"command": "GITHUB_TOKEN=abc"},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(live, append(b, '\n'), 0o600))
	lines := sealedLines(t, repo, sid)
	require.NotEmpty(t, lines)
	for _, l := range lines {
		assert.NotContains(t, l, "TOKEN=abc")
	}
}

// TestCredentialPassLine_MarksTheLineRedacted: a line whose only change is in a
// header or an unknown field carries the redacted marker, as one whose
// tool_input changed does.
func TestCredentialPassLine_MarksTheLineRedacted(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"header":        {"agent_id": "jane@example.com"},
		"unknown field": {"x_token": "hunter2"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := t.TempDir()
			initGitRepo(t, repo)
			sid := "sess-mark"
			live := liveAuditPath(repo, sid)
			require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
			require.NoError(t, os.WriteFile(live, []byte(olderLiveLine(100, sid, extra)), 0o600))
			lines := sealedLines(t, repo, sid)
			require.Len(t, lines, 1)
			var m map[string]any
			require.NoError(t, json.Unmarshal([]byte(lines[0]), &m))
			assert.Equal(t, true, m["redacted"], lines[0])
			assert.NotContains(t, lines[0], "hunter2")
			assert.NotContains(t, lines[0], "jane@example.com")
		})
	}
}

// TestCredentialPassLine_CaseVariantInputIsALossMarker: a top-level key that is
// tool_input, tool_input_hash or tool_input_preview in another case is read as
// the field by a decoder, but its hash and preview cannot be rebuilt; the line
// becomes a loss marker, as the guard treats a case-variant of a field it
// rewrites, and holds no credential.
func TestCredentialPassLine_CaseVariantInputIsALossMarker(t *testing.T) {
	for _, key := range []string{"Tool_Input", "TOOL_INPUT_HASH", "Tool_Input_Preview"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := t.TempDir()
			initGitRepo(t, repo)
			sid := "sess-case"
			live := liveAuditPath(repo, sid)
			require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
			// The only spelling of the field is the variant: no exact tool_input.
			b, err := json.Marshal(map[string]any{
				"ts": audit.FormatLineTS(100), "session": sid, "tool": "Bash",
				key: map[string]any{"api_key": "hunter2"},
			})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(live, append(b, '\n'), 0o600))
			lines := sealedLines(t, repo, sid)
			require.Len(t, lines, 1)
			assert.NotContains(t, lines[0], "hunter2")
			assert.Contains(t, lines[0], "audit_error", "a loss marker")
		})
	}
}

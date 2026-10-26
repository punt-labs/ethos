package mission

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/punt-labs/ethos/v4/internal/redact"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardWrite: the sink refuses bytes that name a home, or writes only the
// canonical encoding of the checked value, which drops tags, comments and
// other syntax. The error names the file and the rule, never the value.
func TestGuardWrite(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	tests := []struct {
		name, file, data string
		want             bool
	}{
		{"clean yaml", "r.yaml", "context: ok\n", false},
		{"prose in a md file", "prompt.md", "see -I/home/alice/inc\n", true},
		{"url is refused too", "prompt.md", "see https://example.com/home/docs\n", true},
		{"app route is refused in prose", "prompt.md", "edit app/home/page.tsx\n", true},
		{"branch name is refused in prose", "prompt.md", "on fix-home-page-layout\n", true},
		{"app route in an untyped yaml value", "r.yaml", "context: app/home/page.tsx\n", true},
		{"scalar value", "r.yaml", "context: /home/alice/x\n", true},
		{"custom tag", "r.yaml", "context: !/home/alice/x v\n", true},
		{"sequence key", "r.yaml", "? [/home/alice/x]\n: v\n", true},
		{"mapping key", "r.yaml", "? {a: /home/alice/x}\n: v\n", true},
		{"comment", "r.yaml", "# /home/alice/x\ncontext: ok\n", true},
		{"escaped yaml", "r.yaml", "context: \"\\x2fhome\\x2falice\\x2fx\"\n", true},
		{"jsonl key", "m.jsonl", `{"/home/alice/x":"v"}` + "\n", true},
		{"jsonl escape", "m.jsonl", `{"a":"\u002fhome\u002falice\u002fx"}` + "\n", true},
		{"jsonl clean", "m.jsonl", `{"ts":"t","event":"e","actor":"a","details":{"a":"~/x"}}` + "\n", false},
		{"own home", "r.yaml", "context: /home/zed/x\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guardErr(filepath.Join("/some/dir", tt.file), []byte(tt.data))
			if !tt.want {
				assert.NoError(t, err)
				return
			}
			if err == nil {
				refusedOrClean(t, filepath.Join("/some/dir", tt.file), []byte(tt.data), "/home/zed")
				return
			}
			assert.Contains(t, err.Error(), tt.file)
			assert.NotContains(t, err.Error(), "alice")
			assert.NotContains(t, err.Error(), "zed")
			assert.NotContains(t, err.Error(), "/some/dir")
		})
	}
}

// refusedOrClean asserts that guardWrite refuses data bound for dest, or
// returns bytes that name no home outside exempt path fields, equal to the
// project encoder's output for the value they decode to.
func refusedOrClean(t *testing.T, dest string, data []byte, home string) {
	t.Helper()
	out, err := guardWrite(dest, data)
	if err != nil {
		return
	}
	assert.Equal(t, canonicalOf(t, dest, out), string(out), "written bytes are the encoder's output for %q", data)
	var strs []oStr
	switch ext := filepath.Ext(dest); ext {
	case ".yaml", ".yml":
		var n yaml.Node
		require.NoError(t, yaml.Unmarshal(out, &n))
		nodeStrings(&n, &strs, false)
	case ".json", ".jsonl":
		for _, line := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var v any
			require.NoError(t, json.Unmarshal([]byte(line), &v))
			stringsOf(v, &strs, false)
		}
	default:
		strs = append(strs, oStr{string(out), false})
	}
	for _, s := range strs {
		assert.False(t, s.holds(home), "written %q for input %q", s.v, data)
	}
}

// canonicalOf encodes what out decodes to, the way guardWrite does.
func canonicalOf(t *testing.T, dest string, out []byte) string {
	t.Helper()
	base := filepath.Base(dest)
	switch filepath.Ext(base) {
	case ".yaml", ".yml":
		if len(out) == 0 {
			return ""
		}
		var doc yaml.Node
		require.NoError(t, yaml.Unmarshal(out, &doc))
		var v any
		if rt := recordType(artifactType(base), &doc); rt != nil {
			p := reflect.New(rt)
			require.NoError(t, doc.Decode(p.Interface()))
			v = p.Interface()
		} else {
			require.NoError(t, doc.Decode(&v))
		}
		b, err := yaml.Marshal(v)
		require.NoError(t, err)
		return string(b)
	case ".jsonl":
		typ := reflect.TypeOf(Event{})
		if base == "missions.jsonl" {
			typ = reflect.TypeOf(TraceSummary{})
		}
		var lines []string
		for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
			p := reflect.New(typ)
			require.NoError(t, json.Unmarshal([]byte(line), p.Interface()))
			b, err := json.Marshal(p.Interface())
			require.NoError(t, err)
			lines = append(lines, string(b))
		}
		return strings.Join(lines, "\n") + "\n"
	}
	return string(out)
}

// TestGuardWrite_UnknownHomeRefusesAbsolutePaths: with no home to compare
// against, a writer cannot clear a path, so it refuses every absolute one.
func TestGuardWrite_UnknownHomeRefusesAbsolutePaths(t *testing.T) {
	t.Setenv("HOME", "")
	assert.Error(t, guardErr("r.yaml", []byte("context: /etc/hosts\n")))
	assert.NoError(t, guardErr("r.yaml", []byte("context: a/b and c\n")))
	assert.True(t, processLeaks("see /var/lib/jenkins/x"))
	assert.False(t, processLeaks("see internal/x"))
}

// TestWriteDelegationSkeleton_RefusesALeakingPrompt: prompt.md goes through
// the sink guard like every other tracked file.
func TestWriteDelegationSkeleton_RefusesALeakingPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	_, err := WriteDelegationSkeleton(repo, "m-2026-07-21-002", "d-2026-07-21-001", DelegationSkeleton{
		Tier: "B", AgentType: "bwk", Prompt: []byte("build in %2Fhome%2Falice%2Fproj"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt.md")
	assert.NotContains(t, err.Error(), "alice")
	_, statErr := os.Stat(filepath.Join(DelegationDir(repo, "m-2026-07-21-002", "d-2026-07-21-001"), "prompt.md"))
	assert.True(t, os.IsNotExist(statErr), "nothing was written")
}

// TestScrubFile_DefaultBranchIsGuarded: a file the scrub does not parse is
// still checked on the way out.
func TestScrubFile_DefaultBranchIsGuarded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	rel := ".punt-labs/ethos/missions/m-2026-07-21-002/notes.txt"
	dest := filepath.Join(repo, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o700))
	require.NoError(t, os.WriteFile(dest, []byte("a@example.com built in %2Fhome%2Falice%2Fproj\n"), 0o600))
	r, err := NewPathRedactor(repo)
	require.NoError(t, err)

	_, err = scrubFile(r, repo, rel, false)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "alice")
	got, readErr := os.ReadFile(dest)
	require.NoError(t, readErr)
	assert.Contains(t, string(got), "a@example.com", "the file is left as it was")
}

// TestRedactYAMLDoc_NonScalarKeysTagsAndKeptFields: the walker judges a key
// of any kind, a custom tag and a kept field.
func TestRedactYAMLDoc_NonScalarKeysTagsAndKeptFields(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	r := PathRedactor{Home: "/home/zed", Repo: "/work/repo"}
	for _, in := range []string{
		"x:\n  ? [/home/alice/x]\n  : v\n",
		"x:\n  ? {a: /home/alice/x}\n  : v\n",
		"context: !/home/alice/x v\n",
		"pipeline: /home/alice/x\n",
	} {
		out, _, err := redactYAMLDoc(r.Content, r.Path, reflect.TypeOf(Contract{}), []byte(in))
		if err != nil {
			assert.NotContains(t, err.Error(), "alice", in)
			continue
		}
		// Rewritten is as good as refused, but a kept field is never rewritten.
		assert.NotEqual(t, "pipeline: /home/alice/x\n", in, "a kept field must refuse")
		assert.NotContains(t, string(out), "alice", in)
	}
}

// TestPathRedactor_ValueKeepsCollidingKeys: keys that redact to one name each
// keep their value, in sorted order.
func TestPathRedactor_ValueKeepsCollidingKeys(t *testing.T) {
	r := PathRedactor{Home: "/home/zed", Repo: "/work/repo"}
	got := r.Map(map[string]any{"/home/zed/a": "1", "~/a": "2"})
	assert.Equal(t, map[string]any{"~/a": "2", "~/a#2": "1"}, got, "the clean key keeps its name")
}

// TestLeakRule_MessagesNameTheRuleNotTheValue pins the refusal wording.
func TestLeakRule_MessagesNameTheRuleNotTheValue(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	_, err := RedactRecord("/work/repo", Contract{Context: "see %2Fhome%2Falice%2Fproj"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field Context")
	assert.Contains(t, err.Error(), "/home/ or /Users/")
	assert.Contains(t, err.Error(), "remove or rephrase it")
	assert.NotContains(t, err.Error(), "alice")
}

// TestGuardWrite_Undecodable: a YAML or JSON file the guard cannot parse is
// refused, !!binary values and keys are decoded, every document of a stream
// is checked, and a .json file is judged whole.
func TestGuardWrite_DecodedForms(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	tests := []struct {
		name, file, data string
		want             bool
	}{
		{"binary value", "r.yaml", "context: !!binary " + b64("/home/alice/x") + "\n", true},
		{"binary key", "r.yaml", "!!binary " + b64("/home/alice/x") + ": v\n", true},
		{"binary clean", "r.yaml", "context: !!binary " + b64("hello") + "\n", false},
		{"bad binary", "r.yaml", "context: !!binary '***'\n", true},
		{"second document", "r.yaml", "a: ok\n---\nb: /home/alice/x\n", true},
		{"second document broken", "r.yaml", "a: ok\n---\nb: [\n", true},
		{"yaml not parsable", "r.yaml", "a: [\n", true},
		{"json not parsable", "d.json", "{", true},
		{"json whole document", "d.json", `{"a":"~/x"}`, false},
		{"json document leak", "d.json", `{"/home/alice/a":1}`, true},
		{"jsonl line not parsable", "m.jsonl", `{"a":1}` + "\n" + "{\n", true},
		{"empty yaml", "r.yaml", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := guardErr(tt.file, []byte(tt.data))
			if tt.want {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "alice")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestWriteResultsLocked_RemovesTheTempFileOnFailure: a failed rename leaves
// no .tmp behind.
func TestWriteResultsLocked_RemovesTheTempFileOnFailure(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))
	dest := mustResultsPath(t, s, c.MissionID)
	require.NoError(t, os.MkdirAll(dest, 0o700))
	err := s.writeResultsLocked(c.MissionID, []Result{{Mission: c.MissionID, Round: 1, Author: c.Worker, Verdict: VerdictPass}})
	require.Error(t, err)
	_, statErr := os.Stat(dest + ".tmp")
	assert.True(t, os.IsNotExist(statErr), "the temp file is removed")
}

// TestPrepareTraceSummary_RefusesALeakBeforeAnythingIsWritten: the line is
// built and guarded up front, so Close and Abandon can refuse with the field
// and rule.
func TestPrepareTraceSummary_RefusesALeakBeforeAnythingIsWritten(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &Store{repoRoot: t.TempDir()}
	c := &Contract{
		MissionID: "m-2026-01-01-001", Status: StatusClosed, Leader: "a", Worker: "b",
		Evaluator: Evaluator{Handle: "c"}, WriteSet: []string{"a.go"},
		SuccessCriteria: []string{"build in %2Fhome%2Falice%2Fproj"}, Budget: Budget{Rounds: 1},
	}
	_, _, err := s.prepareTraceSummary(c, &Result{Verdict: VerdictPass})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SuccessCriteria")
	assert.NotContains(t, err.Error(), "alice")
	_, statErr := os.Stat(filepath.Join(s.repoRoot, ".punt-labs", "ethos", "missions.jsonl"))
	assert.True(t, os.IsNotExist(statErr))
}

// TestWriteTrackedFiles: both payloads are guarded before either file is
// touched, and a failed rename leaves no temp file and no half pair.
func TestWriteTrackedFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.one.yaml"), filepath.Join(dir, "a.two.yaml")

	err := WriteTrackedFiles(
		TrackedFile{Path: a, Data: []byte("context: ok\n"), Perm: 0o644},
		TrackedFile{Path: b, Data: []byte("context: '%2Fhome%2Falice%2Fx'\n"), Perm: 0o644})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "alice")
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries, "a refusal leaves nothing behind")

	require.NoError(t, WriteTrackedFiles(
		TrackedFile{Path: a, Data: []byte("context: ok\n"), Perm: 0o644},
		TrackedFile{Path: b, Data: []byte("context: fine\n"), Perm: 0o644}))
	got, err := os.ReadFile(a)
	require.NoError(t, err)
	assert.Equal(t, "context: ok\n", string(got))

	// The second rename fails (its destination is a directory): the first
	// file, new in this call, is removed and no temp file is left.
	dir2 := t.TempDir()
	a2, b2 := filepath.Join(dir2, "x.yaml"), filepath.Join(dir2, "y.yaml")
	require.NoError(t, os.Mkdir(b2, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(b2, "keep"), []byte("x"), 0o600))
	err = WriteTrackedFiles(
		TrackedFile{Path: a2, Data: []byte("a: 1\n"), Perm: 0o644},
		TrackedFile{Path: b2, Data: []byte("b: 2\n"), Perm: 0o644})
	require.Error(t, err)
	entries, _ = os.ReadDir(dir2)
	require.Len(t, entries, 1, "only the pre-existing directory remains")
	assert.Equal(t, "y.yaml", entries[0].Name())
}

// TestWriteTrackedFiles_FailedReExportKeepsTheOldPair: a second rename that
// fails after the first file was swapped puts the first file back, so the
// pair on disk is the old pair, not a mismatch.
func TestWriteTrackedFiles_FailedReExportKeepsTheOldPair(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	a, b := filepath.Join(dir, "m.one.yaml"), filepath.Join(dir, "m.two.yaml")
	require.NoError(t, os.WriteFile(a, []byte("v: old-a\n"), 0o644))
	require.NoError(t, os.WriteFile(b, []byte("v: old-b\n"), 0o644))

	real := renameFile
	t.Cleanup(func() { renameFile = real })
	renameFile = func(oldpath, newpath string) error {
		if newpath == b && strings.HasSuffix(oldpath, tmpMarker) {
			return errors.New("injected rename failure")
		}
		return real(oldpath, newpath)
	}
	err := WriteTrackedFiles(
		TrackedFile{Path: a, Data: []byte("v: new-a\n"), Perm: 0o644},
		TrackedFile{Path: b, Data: []byte("v: new-b\n"), Perm: 0o644})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected rename failure")

	gotA, _ := os.ReadFile(a)
	gotB, _ := os.ReadFile(b)
	assert.Equal(t, "v: old-a\n", string(gotA))
	assert.Equal(t, "v: old-b\n", string(gotB))
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 2, "no temp file is left")

	renameFile = real
	require.NoError(t, WriteTrackedFiles(
		TrackedFile{Path: a, Data: []byte("v: new-a\n"), Perm: 0o644},
		TrackedFile{Path: b, Data: []byte("v: new-b\n"), Perm: 0o644}))
	gotA, _ = os.ReadFile(a)
	assert.Equal(t, "v: new-a\n", string(gotA))
	entries, _ = os.ReadDir(dir)
	assert.Len(t, entries, 2, "nothing but the pair after a good swap")
}

// TestRedactRecord_RepoRelativePathFieldsMayNameAHomeDir: a repo-relative
// value in a path field is exempt from the /home/<x> rule, so a write set can
// name app/home/page.tsx; the same text in prose, an absolute path value, and
// a value holding this machine's own home are still refused.
func TestRedactRecord_RepoRelativePathFieldsMayNameAHomeDir(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	out, err := RedactRecord("/work/repo", Contract{WriteSet: []string{"app/home/page.tsx", "src/Users/Index.cs"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"app/home/page.tsx", "src/Users/Index.cs"}, out.WriteSet)

	for name, c := range map[string]Contract{
		"prose":        {Context: "edit app/home/page.tsx"},
		"absolute":     {WriteSet: []string{"/srv/app/home/page.tsx"}},
		"climbing":     {WriteSet: []string{"../app/home/page.tsx"}},
		"encoded":      {WriteSet: []string{"%2Fhome%2Fbob%2Fx"}},
		"the own home": {WriteSet: []string{"app/home/zed/x"}},
	} {
		_, err := RedactRecord("/work/repo", c)
		assert.Error(t, err, name)
	}
}

// TestGuardWrite_TypedFilesExemptRepoRelativePathFields: in a contract the
// write_set is a path field; the same value anywhere else is refused.
func TestGuardWrite_TypedFilesExemptRepoRelativePathFields(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	assert.NoError(t, guardErr("contract.yaml", []byte("write_set:\n    - app/home/page.tsx\n")))
	assert.Error(t, guardErr("contract.yaml", []byte("context: app/home/page.tsx\n")))
	assert.Error(t, guardErr("contract.yaml", []byte("write_set:\n    - /srv/home/x\n")))
	assert.Error(t, guardErr("contract.yaml", []byte("write_set:\n    - app/home/zed/x\n")))
	assert.NoError(t, guardErr("missions.jsonl", []byte(`{"write_set":["app/home/page.tsx"]}`+"\n")))
	assert.Error(t, guardErr("missions.jsonl", []byte(`{"success_criteria":["app/home/page.tsx"]}`+"\n")))
}

// TestRepoRelativeExemption_StrictGrammar: only a plain relative path in a
// path field skips the /home/<x> and slug rules. Any other shape, a tool grant
// included, is judged in full: Read(app/home/x) is an accepted false refusal.
func TestRepoRelativeExemption_StrictGrammar(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, v := range []string{
		"Read(/.//home/bob/**)",
		"Read(%2Fhome%2Fbob/**)",
		"Read(/%68ome/bob)",
		"file:%2F%2F%2Fhome%2Fbob",
		"Read(%2Dhome-bob-proj)",
		" /home/bob/x",
		"x=/home/bob/x",
		"a:/home/bob/x",
		"////////////////////home/bob/x",
		"Read(app/home/x)",
	} {
		// The record writer refuses it, or the path pass rewrote the home
		// into a token: either way nothing that names a home is stored.
		for _, c := range []Contract{{Tools: []string{v}}, {WriteSet: []string{v}}} {
			out, err := RedactRecord("/work/repo", c)
			if err == nil {
				for _, fs := range redact.FieldStrings(out) {
					assert.False(t, holdsHome(fs.Value, "/home/zed", false), "RedactRecord stored %q for %q", fs.Value, v)
				}
			}
		}
		doc, mErr := yaml.Marshal(map[string]any{"tools": []string{v}, "write_set": []string{v}})
		require.NoError(t, mErr)
		assert.Error(t, guardErr("contract.yaml", doc), "guardWrite %q", v)
		line, jErr := json.Marshal(map[string]any{"write_set": []string{v}})
		require.NoError(t, jErr)
		assert.Error(t, guardErr("missions.jsonl", append(line, '\n')), "guardWrite jsonl %q", v)
	}
	_, err := RedactRecord("/work/repo", Contract{WriteSet: []string{"app/home/page.tsx", "x/*.go"}})
	assert.NoError(t, err)
}

// TestWriteTrackedFiles_FailedRestoreLeavesContent: when the swap of the second
// file fails and so does the restore of the first, the first destination holds
// the new content or the old, never nothing, and every stray file in the
// directory is a hidden, marked temp.
func TestWriteTrackedFiles_FailedRestoreLeavesContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	a, b := filepath.Join(dir, "m.one.yaml"), filepath.Join(dir, "m.two.yaml")
	require.NoError(t, os.WriteFile(a, []byte("v: old-a\n"), 0o644))
	require.NoError(t, os.WriteFile(b, []byte("v: old-b\n"), 0o644))

	real := renameFile
	t.Cleanup(func() { renameFile = real })
	swapped := false
	renameFile = func(oldpath, newpath string) error {
		if newpath == b || (newpath == a && swapped) {
			return errors.New("injected rename failure")
		}
		if newpath == a {
			swapped = true
		}
		return real(oldpath, newpath)
	}
	err := WriteTrackedFiles(
		TrackedFile{Path: a, Data: []byte("v: new-a\n"), Perm: 0o644},
		TrackedFile{Path: b, Data: []byte("v: new-b\n"), Perm: 0o644})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restoring")

	gotA, readErr := os.ReadFile(a)
	require.NoError(t, readErr, "the destination still exists")
	assert.Contains(t, []string{"v: old-a\n", "v: new-a\n"}, string(gotA))
	gotB, _ := os.ReadFile(b)
	assert.Equal(t, "v: old-b\n", string(gotB))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if n == "m.one.yaml" || n == "m.two.yaml" {
			continue
		}
		assert.True(t, strings.HasPrefix(n, ".") && strings.HasSuffix(n, tmpMarker), "stray file %q", n)
	}
}

// TestGuardWrite_YAMLAliasesAndDirectives: an alias is judged where it lands,
// so a path-field value aliased into prose meets the full rule; and a YAML
// directive, which ethos never writes, is refused.
func TestGuardWrite_YAMLAliasesAndDirectives(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	assert.NoError(t, guardErr("contract.yaml", []byte("write_set: &a [x/home/bob]\n")))
	err := guardErr("contract.yaml", []byte("write_set: &a [x/home/bob]\ncontext: *a\n"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "bob")

	// A directive never reaches the disk: the canonical encoding has none.
	out, err := guardWrite("contract.yaml", []byte("%TAG !e! tag:example.com,2000:/home/bob/\n---\ncontext: !e!x ok\n"))
	if err == nil {
		assert.NotContains(t, string(out), "%TAG")
		assert.NotContains(t, string(out), "bob")
	}
	for _, in := range []string{"%YAML 1.1\n---\ncontext: ok\n", "%YAML 1.2\n---\ncontext: ok\n"} {
		if out, err := guardWrite("contract.yaml", []byte(in)); err == nil {
			assert.NotContains(t, string(out), "%YAML")
		}
	}
	assert.NoError(t, guardErr("contract.yaml", []byte("context: \"%TAG not a directive\"\n")))
}

// guardErr is guardWrite's verdict alone.
func guardErr(dest string, data []byte) error {
	_, err := guardWrite(dest, data)
	return err
}

// utf16le encodes s as UTF-16LE with a byte-order mark.
func utf16le(s string) []byte {
	out := []byte{0xff, 0xfe}
	for _, r := range utf16.Encode([]rune(s)) {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

// TestGuardWrite_SyntaxProbes: every syntax trick the reviews used to carry a
// home past the checker is either refused or dropped by the canonical
// encoding, in a typed record and in an untyped file.
func TestGuardWrite_SyntaxProbes(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	tag := "%TAG !e! tag:example.com,2000:/home/bob/\n---\ncontext: !e!x ok\n"
	probes := map[string][]byte{
		"anchor slug":            []byte("goal: &-home-bob- hi\ncontext: ok\n"),
		"anchor path":            []byte("context: &/home/bob/x hi\n"),
		"alias line comment":     []byte("notes: &a hi\ncontext: *a # see /home/bob/x\n"),
		"alias head comment":     []byte("notes: &a hi\n# see /home/bob/x\ncontext: *a\n"),
		"alias foot comment":     []byte("notes: &a hi\ncontext: *a\n# see /home/bob/x\n"),
		"BOM before a directive": append([]byte("\ufeff"), tag...),
		"bare CR":                []byte("%TAG !e! tag:example.com,2000:/home/bob/\r---\rcontext: !e!x ok\r"),
		"NEL":                    []byte("context: ok\u0085# see /home/bob/x\n"),
		"LS":                     []byte("context: ok\u2028# see /home/bob/x\n"),
		"PS":                     []byte("context: ok\u2029# see /home/bob/x\n"),
		"text after ...":         []byte("context: ok\n... # see /home/bob/x\n"),
		"directive after ...":    append([]byte("context: ok\n...\n"), tag...),
		"UTF-16":                 utf16le(tag),
		"plain directive":        []byte(tag),
	}
	for name, data := range probes {
		for _, dest := range []string{"/r/contract.yaml", "/r/notes.yaml"} {
			t.Run(name+" "+filepath.Base(dest), func(t *testing.T) {
				refusedOrClean(t, dest, data, "/home/zed")
				if out, err := guardWrite(dest, data); err == nil {
					assert.NotContains(t, string(out), "bob")
					assert.NotContains(t, string(out), "%TAG")
				}
			})
		}
	}

	// A quoted scalar may continue on a line that begins with %; it is a
	// value, not a directive, and is written.
	out, err := guardWrite("/r/contract.yaml", []byte("context: \"a\n%b\"\n"))
	require.NoError(t, err)
	assert.Contains(t, string(out), "a %b")
}

// TestWriteTrackedFiles_DirSyncFailureIsAnError: the pair is in place but may
// not survive a crash, so the export reports it.
func TestWriteTrackedFiles_DirSyncFailureIsAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	real := syncDir
	t.Cleanup(func() { syncDir = real })
	syncDir = func(string) error { return errors.New("injected fsync failure") }
	dir := t.TempDir()
	a := filepath.Join(dir, "x.one.yaml")
	err := WriteTrackedFiles(TrackedFile{Path: a, Data: []byte("v: 1\n"), Perm: 0o644})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected fsync failure")
	var nd *NotDurableError
	require.True(t, errors.As(err, &nd), "a directory sync failure is a NotDurableError")
	assert.Contains(t, err.Error(), "all files were written; only the directory sync failed")
	got, readErr := os.ReadFile(a)
	require.NoError(t, readErr)
	assert.Equal(t, "v: 1\n", string(got))
}

// TestGuardWrite_RefusesARecordThatEncodesToNothing: a record whose only
// content is an empty map is empty as stored, so the first write refuses it
// and the guard stays a fixed point.
func TestGuardWrite_RefusesARecordThatEncodesToNothing(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	_, err := guardWrite("/r/log.jsonl", []byte(`{"details":{}}`+"\n"))
	assert.Error(t, err)
}

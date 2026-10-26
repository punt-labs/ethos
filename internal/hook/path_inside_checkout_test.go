package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

// checkoutUnderHome is a git repo under a temp HOME, with the global root.
func checkoutUnderHome(t *testing.T) (home, repo, global string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	repo = filepath.Join(home, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	initGitRepo(t, repo)
	return home, repo, filepath.Join(home, ".punt-labs", "ethos")
}

func implicitContract() *mission.Contract {
	return makeContractWithPreconditions([]mission.Precondition{{
		Form: mission.PreconditionFormImplicit, Message: "read before write",
	}}, mission.Inputs{}, nil)
}

// TestPathField_InsideTheCheckoutSkipsOnlyTheMidPathHomeRule: a path field
// naming a file of the checkout, repo-relative or absolute under it, skips only
// the mid-path /home/<name> rule (leakSlug and the rest still apply inside the
// checkout): nav--home-page.scss and @types/home/page.tsx are stored as written
// (or as <repo>/...), the seal guard agrees with the writer, and a Read of one
// satisfies a read-before-write precondition.
func TestPathField_InsideTheCheckoutSkipsOnlyTheMidPathHomeRule(t *testing.T) {
	_, repo, global := checkoutUnderHome(t)
	for _, rel := range []string{"src/styles/nav--home-page.scss", "@types/home/page.tsx"} {
		for _, form := range []string{"relative", "absolute"} {
			p := rel
			want := rel
			if form == "absolute" {
				p, want = filepath.Join(repo, rel), "<repo>/"+rel
			}
			sess := "sess-" + form + "-" + strings.NewReplacer("/", "-", "@", "at").Replace(rel)
			auditRead(t, repo, global, sess, p)

			e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, sess, repo, time.Now())
			assert.Equal(t, want, e.ToolInput["file_path"], "%s %s: stored as written", form, rel)
			line, err := json.Marshal(e)
			require.NoError(t, err)
			_, rule := guardAuditLineWhy(line)
			assert.Empty(t, rule, "%s %s: the seal guard agrees with the writer", form, rel)

			reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": filepath.Join(repo, rel)}, sess, repo, repo)
			require.NoError(t, err)
			assert.False(t, deny, "%s %s: the Read satisfies it: %s", form, rel, reason)
		}
	}
}

// TestPathField_ForeignProjectSlugUnderHomeIsRedacted: a path under a home but
// outside the checkout, with a project slug of another home, is redacted by the
// writer and the seal guard (leakSlug applies to every path field).
func TestPathField_ForeignProjectSlugUnderHomeIsRedacted(t *testing.T) {
	home, repo, _ := checkoutUnderHome(t)
	for _, tail := range []string{
		".claude/projects/C--Users-al-Coding-repo/abc.jsonl",
		".claude/projects/-var-home-bob-x/f.jsonl",
	} {
		abs := filepath.Join(home, tail)
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": abs}}, "s", repo, time.Now())
		assert.Equal(t, "[redacted-path]", e.ToolInput["file_path"], "writer: %s", tail)
		out, rule := guardAuditLineWhy(rawReadLine("~/" + tail))
		assert.NotEmpty(t, rule, "guard: %s", tail)
		assert.NotContains(t, string(out), "bob")
		assert.NotContains(t, string(out), "Users-al")
	}
}

// TestPathField_OwnSlugInsideTheCheckoutBecomesAPlaceholder: this machine's
// dash slug in a checkout path is written as -~, which a precondition expands.
func TestPathField_OwnSlugInsideTheCheckoutBecomesAPlaceholder(t *testing.T) {
	home, repo, global := checkoutUnderHome(t)
	slug := strings.ReplaceAll(home, string(filepath.Separator), "-")
	p := filepath.Join(repo, "cache", slug+"-proj", "x.txt")
	auditRead(t, repo, global, "sess-slug", p)
	e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", repo, time.Now())
	got, _ := e.ToolInput["file_path"].(string)
	assert.Contains(t, got, "-~")
	assert.NotContains(t, got, slug)
	reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": p}, "sess-slug", repo, repo)
	require.NoError(t, err)
	assert.False(t, deny, "the Read satisfies it: %s", reason)
}

// TestSealGuard_KeepsNumbersAndTheHash: a line the writer produced passes the
// seal guard without a rewrite, a large number keeps every digit, and the
// tool_input_hash is not changed.
func TestSealGuard_KeepsNumbersAndTheHash(t *testing.T) {
	line := []byte(`{"ts":"2026-01-01T00:00:00Z","session":"s","tool":"Bash","tool_input":{"offset":12345678901234567890,"f":1.50},"tool_input_hash":"abc"}`)
	out, rule := guardAuditLineWhy(line)
	assert.Empty(t, rule)
	assert.Contains(t, string(out), "12345678901234567890")
	assert.Contains(t, string(out), "1.50")
	assert.Contains(t, string(out), `"tool_input_hash":"abc"`)
}

// TestPreconditions_RedactedPlaceholderIsNotARead: a Read whose path the writer
// replaced by the placeholder is not recorded as a Read of a file named
// [redacted-path].
func TestPreconditions_RedactedPlaceholderIsNotARead(t *testing.T) {
	_, repo, global := checkoutUnderHome(t)
	auditRead(t, repo, global, "sess-ph", filepath.Join(repo, "C--Users-bob-x", "f"))
	for _, target := range []string{"[redacted-path]", filepath.Join(repo, "[redacted-path]"), "<repo>/[redacted-path]"} {
		_, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": target}, "sess-ph", repo, repo)
		require.NoError(t, err)
		assert.True(t, deny, "a Write of %q is denied", target)
	}
}

// TestPreconditions_LiteralPlaceholderNameIsNotTheExpandedOne: a file whose
// name holds a placeholder token (cache/-~-project/config.json) is stored
// unchanged and expands, when read back, to this machine's slug. A Read of it
// must not allow a Write of the file that name expands to, and a Write of a
// name holding a placeholder is denied, since a Read of it cannot be verified.
func TestPreconditions_LiteralPlaceholderNameIsNotTheExpandedOne(t *testing.T) {
	home, repo, global := checkoutUnderHome(t)
	slug := strings.ReplaceAll(home, string(filepath.Separator), "-")
	literal := "cache/-~-project/config.json"
	expanded := "cache/" + slug + "-project/config.json"
	write := func(sess, p string) (string, bool) {
		reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": p}, sess, repo, repo)
		require.NoError(t, err)
		return reason, deny
	}

	auditRead(t, repo, global, "sess-lit", literal)
	_, deny := write("sess-lit", expanded)
	assert.True(t, deny, "a Read of the literal placeholder name does not allow the expanded file")
	reason, deny := write("sess-lit", literal)
	assert.True(t, deny, "a Write of a name holding a placeholder is denied")
	assert.Contains(t, reason, "redaction placeholder")

	auditRead(t, repo, global, "sess-exp", expanded)
	reason, deny = write("sess-exp", expanded)
	assert.False(t, deny, "a Read of the file allows a Write of it: %s", reason)
}

// TestPreconditions_PlaceholderInTheMiddleOfAName: a token inside a name, not at
// a path boundary, round-trips through the path pass and Expand exactly, so a
// Read of the file allows a Write of it, for the implicit form and for an
// explicit require_read.
func TestPreconditions_PlaceholderInTheMiddleOfAName(t *testing.T) {
	_, repo, global := checkoutUnderHome(t)
	n := 0
	for _, name := range []string{"src/a-~b", "src/foo-~", "src/n-~1.c", "src/[redacted-x]/y"} {
		n++
		sess := "sess-mid-" + strconv.Itoa(n)
		auditRead(t, repo, global, sess, name)
		reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": name}, sess, repo, repo)
		require.NoError(t, err)
		assert.False(t, deny, "implicit %q: %s", name, reason)
		reason, deny, err = EvaluatePreconditions(redactedRequireRead(t, repo, name), "Write", map[string]any{"file_path": "x.go"}, sess, repo, repo)
		require.NoError(t, err)
		assert.False(t, deny, "explicit %q: %s", name, reason)
	}
}

// TestPreconditions_AmbiguousNameIsExplained: a name whose placeholder is at a
// path boundary (src/-~-proj/x, ~/x/-~-y) reads back as another file. A Write of
// it is denied, and when an explicit require_read names it, the deny reason
// counts the Reads that were ignored for that.
func TestPreconditions_AmbiguousNameIsExplained(t *testing.T) {
	_, repo, global := checkoutUnderHome(t)
	for i, name := range []string{"src/-~-proj/x", "~/x/-~-y"} {
		sess := "sess-amb-" + strconv.Itoa(i)
		auditRead(t, repo, global, sess, name)
		reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": name}, sess, repo, repo)
		require.NoError(t, err)
		assert.True(t, deny, "implicit %q", name)
		assert.Contains(t, reason, "redaction placeholder")

		reason, deny, err = EvaluatePreconditions(redactedRequireRead(t, repo, name), "Write", map[string]any{"file_path": "x.go"}, sess, repo, repo)
		require.NoError(t, err)
		assert.True(t, deny, "explicit %q", name)
		assert.Contains(t, reason, "(1 Read(s) were ignored: the file name contains a redaction placeholder)")
	}
}

// TestAmbiguousPath_FoldsCaseWhereThePathPassDoes: on a case-insensitive file
// system the path pass folds the case of a repo prefix, so a Read of
// /users/alice/work/a.go is stored as <repo>/a.go and read back as
// /Users/Alice/work/a.go: the same file, not an ambiguous name. Where the path
// pass is strict the lower-case spelling is another user's home.
func TestAmbiguousPath_FoldsCaseWhereThePathPassDoes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := mission.PathCaseFold
	t.Cleanup(func() { mission.PathCaseFold = old })

	mission.PathCaseFold = true
	amb, err := ambiguousPath("/users/alice/work/a.go", "/Users/Alice/work")
	require.NoError(t, err)
	assert.False(t, amb, "case folded: the same file")

	mission.PathCaseFold = false
	amb, err = ambiguousPath("/users/alice/work/a.go", "/Users/Alice/work")
	require.NoError(t, err)
	assert.True(t, amb, "strict: another user's home reads back as a different path")
}

// TestPreconditions_ReadsMatchWithThePathPassCaseRule: where the path pass folds
// case (macOS), a Read of a case-variant spelling of the checkout allows a Write
// of any spelling of the same file; where it does not, the comparison is exact.
func TestPreconditions_ReadsMatchWithThePathPassCaseRule(t *testing.T) {
	old := mission.PathCaseFold
	t.Cleanup(func() { mission.PathCaseFold = old })
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".punt-labs", "ethos")
	repo := filepath.Join(home, "RepoDir")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	initGitRepo(t, repo)
	canonical := filepath.Join(repo, "a.go")
	require.NoError(t, os.WriteFile(canonical, []byte("x"), 0o600))
	// repodir is the same directory as RepoDir, as on a case-insensitive volume.
	require.NoError(t, os.Symlink(repo, filepath.Join(home, "repodir")))
	variant := filepath.Join(home, "repodir", "a.go")
	require.NotEqual(t, canonical, variant)

	write := func(sess, p string) bool {
		_, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": p}, sess, repo, repo)
		require.NoError(t, err)
		return deny
	}

	mission.PathCaseFold = true
	auditRead(t, repo, global, "sess-fold", variant)
	assert.False(t, write("sess-fold", canonical), "the canonical spelling is the file that was read")
	assert.False(t, write("sess-fold", variant), "so is the spelling it was read by")

	mission.PathCaseFold = false
	auditRead(t, repo, global, "sess-strict", canonical)
	assert.False(t, write("sess-strict", canonical))
	assert.True(t, write("sess-strict", variant), "exact where the path pass is strict")
}

// TestPreconditions_StoredAmbiguousRequireReadIsNotMetByTheExpandedFile: a
// contract stored before admission refused such entries holds
// cache/-~-project/config.json. When that literal file exists in the checkout,
// a Read of cache/<slug>-project/config.json (what the entry expands to) does
// not satisfy it: the requirement is ambiguous and is denied with a reason. When
// no such literal file exists the entry can only mean the expanded file, and the
// Read satisfies it. An ordinary <repo>/docs/spec.md is satisfied by a Read of
// the real file.
func TestPreconditions_StoredAmbiguousRequireReadIsNotMetByTheExpandedFile(t *testing.T) {
	home, repo, global := checkoutUnderHome(t)
	slug := strings.ReplaceAll(home, string(filepath.Separator), "-")
	expanded := "cache/" + slug + "-project/config.json"
	write := map[string]any{"file_path": "x.go"}
	eval := func(c *mission.Contract, sess string) (string, bool) {
		reason, deny, err := EvaluatePreconditions(c, "Write", write, sess, repo, repo)
		require.NoError(t, err)
		return reason, deny
	}

	c := redactedRequireRead(t, repo, "cache/-~-project/config.json")
	auditRead(t, repo, global, "sess-a", expanded)
	_, deny := eval(c, "sess-a")
	assert.False(t, deny, "no literal file: the entry means the expanded file, which was read")

	require.NoError(t, os.MkdirAll(filepath.Join(repo, "cache", "-~-project"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "cache", "-~-project", "config.json"), []byte("{}"), 0o600))
	reason, deny := eval(c, "sess-a")
	assert.True(t, deny, "the literal file exists: the Read of the expanded file is not the file required")
	assert.Contains(t, reason, "redaction placeholder")

	require.NoError(t, os.MkdirAll(filepath.Join(repo, "docs"), 0o700))
	spec := filepath.Join(repo, "docs", "spec.md")
	require.NoError(t, os.WriteFile(spec, []byte("x"), 0o600))
	auditRead(t, repo, global, "sess-b", spec)
	ordinary := redactedRequireRead(t, repo, spec)
	require.Equal(t, "<repo>/docs/spec.md", ordinary.Preconditions[0].RequireRead[0])
	reason, deny = eval(ordinary, "sess-b")
	assert.False(t, deny, "an ordinary placeholder is met by a Read of the real file: %s", reason)
}

// TestReadsContain_FoldedMatchNeedsTheSameFile: PathCaseFold says the path pass
// folds case, but a volume can still be case-sensitive. A spelling that matches
// only by folding counts only when both paths are the same file on disk: a Read
// of Foo does not satisfy a Write of the distinct file foo.
func TestReadsContain_FoldedMatchNeedsTheSameFile(t *testing.T) {
	old := mission.PathCaseFold
	t.Cleanup(func() { mission.PathCaseFold = old })
	mission.PathCaseFold = true
	dir := t.TempDir()
	foo, Foo := filepath.Join(dir, "foo"), filepath.Join(dir, "Foo")
	require.NoError(t, os.WriteFile(Foo, []byte("upper"), 0o600))
	require.NoError(t, os.WriteFile(foo, []byte("lower"), 0o600))
	if os.SameFile(mustStat(t, foo), mustStat(t, Foo)) {
		t.Skip("this file system folds case: Foo and foo are one file")
	}
	reads := map[string]struct{}{Foo: {}}
	assert.True(t, readsContain(reads, Foo, dir), "exact")
	assert.False(t, readsContain(reads, foo, dir), "a different file that folds to the same name")
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(p)
	require.NoError(t, err)
	return fi
}

// TestRelUnder: the path of p under root, by the prefix rule, for a root that is
// a directory and for the file system root, whose cleaned form ends in a
// separator.
func TestRelUnder(t *testing.T) {
	for _, tt := range []struct {
		root, p, want string
		ok            bool
	}{
		{"/repo", "/repo/a.go", "a.go", true},
		{"/repo", "/repo", "", true},
		{"/repo", "/repox/a.go", "", false},
		{"/repo", "/other/a.go", "", false},
		{"/", "/a/b.go", "a/b.go", true},
		{"/", "/", "", true},
	} {
		got, ok := relUnder(tt.root, tt.p)
		assert.Equal(t, tt.ok, ok, "%s %s", tt.root, tt.p)
		assert.Equal(t, tt.want, got, "%s %s", tt.root, tt.p)
	}
}

// TestPreconditions_HomeRequirementIgnoresALiteralTildeDirectory: a leading ~ or
// ~/ in a stored require_read is the home placeholder. A checkout that holds a
// directory literally named ~ does not make the requirement ambiguous, and a
// Read of $HOME/.claude/CLAUDE.md satisfies it.
func TestPreconditions_HomeRequirementIgnoresALiteralTildeDirectory(t *testing.T) {
	home, repo, global := checkoutUnderHome(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "~", ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "~", ".claude", "CLAUDE.md"), []byte("x"), 0o600))
	c := redactedRequireRead(t, repo, "~/.claude/CLAUDE.md")
	auditRead(t, repo, global, "sess-tilde", filepath.Join(home, ".claude", "CLAUDE.md"))
	reason, deny, err := EvaluatePreconditions(c, "Write", map[string]any{"file_path": "x.go"}, "sess-tilde", repo, repo)
	require.NoError(t, err)
	assert.False(t, deny, "the home file was read: %s", reason)
}

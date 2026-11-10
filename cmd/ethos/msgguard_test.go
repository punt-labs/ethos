package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// bannedMessageTerms are phrases a user-facing string must not contain.
// Ethos repairs its own storage (DES-084): a message says what ethos did
// or found, never what the user must do, and never names a removed command.
var bannedMessageTerms = []string{
	"ethos repair", "audit quarantine", "audit migrate", "mission scrub",
	"mission migrate", "adr migrate", "session purge", "session write-current",
	"operator cleanup", "operator-directed", "manual migration", "manually",
	"by hand", "hand-", "reconcile",
	"ethos show", "ethos list", "ethos iam", "ethos create", "resolve-agent",
	"ext del", "session create", "session delete", "session delete-current",
	"ethos session roster", "`session roster`", "write-current", "--no-teams", "vendor --from",
	"run `ethos enable`", "run ethos enable",
	// A removed or remedy command named in a message, with or without "run".
	"ethos enable", "ethos seed", "ethos migrate",
	// Instructions to the user, whatever the command.
	"re-run", "rerun", "inspect before", "add it before", "file a bug", "check file",
	// Remedies git lookups once appended to their cause.
	"install git", "fix safe.directory", "point ethos_repo_root", "and repair it",
}

// bannedExtraPatterns are banned shapes a plain term cannot express.
var bannedExtraPatterns = []*regexp.Regexp{
	regexp.MustCompile(`git rm( -r)? --cached`),
}

// msgguardAllow marks a literal as a genuine user choice. A reason must
// follow on the same line; a bare marker is a hit.
var msgguardAllow = regexp.MustCompile(`msgguard: allow \S.+`)

func repoRootFromTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

// isWordByte reports whether b is a regexp word character.
func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// bannedPatterns match each term on word boundaries, so "by hand" does not
// fire on "by handle". A term that starts or ends in punctuation ("--no-teams",
// "hand-") has no word boundary there, so that edge is left open on the right
// and anchored to a non-word character on the left.
var bannedPatterns = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(bannedMessageTerms))
	for i, term := range bannedMessageTerms {
		left, right := `(?:^|\W)`, ``
		if isWordByte(term[0]) {
			left = `\b`
		}
		if isWordByte(term[len(term)-1]) {
			right = `\b`
		}
		out[i] = regexp.MustCompile(left + termPattern(term) + right)
	}
	return out
}()

// spaceClass is what a space in a term matches: one or more of whitespace,
// any Unicode space (non-breaking, ideographic), any dash (hyphen, en, em),
// underscore, quotes, backtick, parentheses, a backslash, format characters,
// or a colon, in any order and count ("ethos:repair", "ethos :repair",
// "ethos::repair", "ethos: repair"). The error-prefix form "ethos: <word>:" at
// the start of a literal is exempted by errorPrefix, not here. regexp's \s is
// ASCII-only, so the Unicode classes are spelled out.
const spaceClass = `(?:[\s\p{Z}\p{Pd}\p{Cf}_'"` + "`" + `()\\:])+`

// errorPrefix is the one colon form a message may carry: a literal that opens
// with "ethos: <word>:" is an error prefix (program: context: what failed), so
// the prefix is dropped before the terms are matched.
var errorPrefix = regexp.MustCompile(`^ethos: [a-z][a-z-]*:`)

// termPattern quotes term for a regexp, except that each space in the term
// matches spaceClass. "ethos repair" then matches "ethos  repair",
// "ethos-repair" and "ethos<ZWSP>repair"; a hyphen written in the term
// ("--no-teams", "hand-") stays literal, so "resolve agent" in prose does not
// match the term "resolve-agent".
func termPattern(term string) string {
	parts := strings.Split(term, " ")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return strings.Join(parts, spaceClass)
}

// foldedString returns the constant value of n when it is a string literal,
// a package-level string const, or a "+" chain of those, so a message split
// across literals or assembled from a named const is judged whole. consts may
// be nil.
func foldedString(n ast.Node, consts map[string]string) (string, bool) {
	switch x := n.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(x.Value)
		return v, err == nil
	case *ast.Ident:
		v, ok := consts[x.Name]
		return v, ok
	case *ast.ParenExpr:
		return foldedString(x.X, consts)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, lok := foldedString(x.X, consts)
		r, rok := foldedString(x.Y, consts)
		return l + r, lok && rok
	}
	return "", false
}

// packageConsts collects the string consts declared at package level in every
// non-test Go file of dir, so a const in one file resolves in its siblings.
func packageConsts(t *testing.T, dir string) map[string]string {
	t.Helper()
	consts := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files = append(files, f)
	}
	// Two passes let a const refer to one declared later or in another file.
	for pass := 0; pass < 2; pass++ {
		for _, f := range files {
			collectConsts(f, consts)
		}
	}
	return consts
}

// collectConsts adds each package-level const of f whose value folds to a
// string.
func collectConsts(f *ast.File, consts map[string]string) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs, ok := sp.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if v, ok := foldedString(vs.Values[i], consts); ok {
					consts[name.Name] = v
				}
			}
		}
	}
}

// normalize folds every dash to '-' so "re\u2010run" meets the term "re-run".
func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Pd, r) {
			return '-'
		}
		return r
	}, strings.ToLower(s))
}

// stripFormat removes every format character (soft hyphen, zero-width space,
// joiners, BOM) so one inside a word does not split it.
func stripFormat(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

func bannedIn(s string) string {
	norm := errorPrefix.ReplaceAllString(normalize(s), "")
	for _, low := range []string{norm, stripFormat(norm)} {
		for i, re := range bannedPatterns {
			if re.MatchString(low) {
				return bannedMessageTerms[i]
			}
		}
		for _, re := range bannedExtraPatterns {
			if re.MatchString(low) {
				return re.String()
			}
		}
	}
	return ""
}

// allowLines returns the lines of f that carry an allow comment with a reason.
// Only an ast.Comment counts: the marker text inside a string literal does not.
func allowLines(fset *token.FileSet, f *ast.File) map[int]bool {
	out := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if msgguardAllow.MatchString(c.Text) {
				out[fset.Position(c.Slash).Line] = true
			}
		}
	}
	return out
}

// allowed reports whether every literal and const name that makes up n sits on
// a line with an allow comment, so a marker on one line of a "+" chain does not
// excuse the other lines.
func allowed(fset *token.FileSet, n ast.Node, allow map[int]bool) bool {
	ok := true
	ast.Inspect(n, func(m ast.Node) bool {
		switch m.(type) {
		case *ast.BasicLit, *ast.Ident:
			if !allow[fset.Position(m.Pos()).Line] {
				ok = false
			}
		}
		return true
	})
	return ok
}

// goLiteralHits parses one Go file and reports each string literal that
// holds a banned term, unless an allow comment sits on its line.
func goLiteralHits(t *testing.T, path, rel string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return goSourceHits(t, src, rel, packageConsts(t, filepath.Dir(path)))
}

// goSourceHits judges src. A package-level const is folded where it is used,
// and every const value, local or exported, is judged where it is declared.
func goSourceHits(t *testing.T, src []byte, rel string, consts map[string]string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	allow := allowLines(fset, f)
	var hits []string
	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		val, ok := foldedString(n, consts)
		if !ok {
			return true
		}
		// A folded chain is judged whole, so do not descend into its parts.
		_, isChain := n.(*ast.BinaryExpr)
		term := bannedIn(val)
		if term == "" || allowed(fset, n, allow) {
			return !isChain
		}
		hit := rel + ":" + strconv.Itoa(fset.Position(n.Pos()).Line) + ": " + term
		if !seen[hit] {
			seen[hit] = true
			hits = append(hits, hit)
		}
		return !isChain
	})
	return hits
}

// shLineHits reports each non-comment line of a hook script that holds a
// banned term, unless the line carries the allow marker.
func shLineHits(t *testing.T, path, rel string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	for i, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || msgguardAllow.MatchString(line) {
			continue
		}
		if term := bannedIn(line); term != "" {
			hits = append(hits, rel+":"+strconv.Itoa(i+1)+": "+term)
		}
	}
	return hits
}

// TestNoMessageTellsTheUserToRepair walks every non-test Go file under cmd/
// and internal/ and every hook script under plugin/hooks/. A string literal
// naming a removed command or telling the user to clean up by hand fails.
// The one escape is a same-line "// msgguard: allow <reason>" comment for a
// genuine user choice.
func TestNoMessageTellsTheUserToRepair(t *testing.T) {
	root := repoRootFromTest(t)
	var hits []string
	walk := func(dir string, want func(name string) bool, scan func(*testing.T, string, string) []string) {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return fs.SkipDir
				}
				return nil
			}
			if !want(d.Name()) {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			hits = append(hits, scan(t, path, filepath.ToSlash(rel))...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	isGo := func(n string) bool { return strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") }
	isSh := func(n string) bool { return strings.HasSuffix(n, ".sh") }
	walk("cmd", isGo, goLiteralHits)
	walk("internal", isGo, goLiteralHits)
	walk(filepath.Join("plugin", "hooks"), isSh, shLineHits)
	for _, h := range hits {
		t.Errorf("message tells the user to repair or names a removed command: %s", h)
	}
}

// TestMessageGuardEvasions pins the spellings a plain substring match misses:
// runs of whitespace, a hyphen for the space, and a string assembled from
// adjacent literals. Assembly through format verbs (fmt.Sprintf("%s repair",
// "ethos")) is out of reach for a lexical guard; the doctor and hook tests pin
// those messages by their exact output instead.
func TestMessageGuardEvasions(t *testing.T) {
	for _, s := range []string{
		"ethos  repair",
		"ethos\trepair",
		"ethos\nrepair",
		"ethos-repair",
		"audit   quarantine",
		"session-purge",
		"run   `ethos enable`",
		"hooks stale; `ethos enable` refreshes them",
		"run \"ethos enable\" to refresh",
		"run (ethos enable)",
		"ethos\u2013repair",
		"ethos\u2014repair",
		"ethos\u200brepair",
		"ethos_repair",
		"ethos\u00a0repair",
		"rerun ethos enable",
		"re-run ethos enable",
		"to refresh hooks, re-run the command",
		"ethos seed",
		"inspect before refreshing",
		"add it before running",
		"if this persists, file a bug",
		"check file permissions",
	} {
		if bannedIn(s) == "" {
			t.Errorf("%q evades the guard", s)
		}
	}
	for _, s := range []string{"resolve agent: %w", "failed to write current session", "ethos serve"} {
		if got := bannedIn(s); got != "" {
			t.Errorf("%q must pass, matched %q", s, got)
		}
	}
	src := "package p\nvar a = \"ethos \" + \"repair\"\nvar b = \"audit \" +\n\t\"quarantine\"\nvar c = \"fine \" + \"text\"\n"
	f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		if v, ok := foldedString(n, nil); ok {
			if term := bannedIn(v); term != "" {
				found = append(found, v)
			}
		}
		return true
	})
	if len(found) != 2 {
		t.Errorf("concatenated literals: want 2 hits, got %v", found)
	}
}

func TestMessageGuardMatchesBannedTerms(t *testing.T) {
	for _, s := range bannedMessageTerms {
		if bannedIn("x "+strings.ToUpper(s)+" y") == "" {
			t.Errorf("%q is not detected", s)
		}
	}
	for _, s := range []string{"git rm --cached x", "git rm -r --cached x", "hand-edit the file", "Run `ethos enable` now", "run ethos enable"} {
		if bannedIn(s) == "" {
			t.Errorf("%q is not detected", s)
		}
	}
	for _, line := range []string{`"x" // msgguard: allow user choice`, `# msgguard: allow reason`} {
		if !msgguardAllow.MatchString(line) {
			t.Errorf("%q must be allowed", line)
		}
	}
	for _, line := range []string{`"x" // msgguard: allow`, `"x" // msgguard: allow `} {
		if msgguardAllow.MatchString(line) {
			t.Errorf("%q has no reason and must not be allowed", line)
		}
	}
	for _, ok := range []string{"ethos: seed: failed to write", "restart Claude Code to load new agents", "searching identities by handle", "handheld", "handle-based", "Manage session roster. Methods: roster, join", "show the session roster"} {
		if bannedIn(ok) != "" {
			t.Errorf("%q must pass", ok)
		}
	}
}

// TestMessageGuardUnicodeAndPunctuation pins the separators a term's space must
// absorb: a Unicode dash or a format character inside a hyphenated term,
// a backslash line continuation and a colon.
func TestMessageGuardUnicodeAndPunctuation(t *testing.T) {
	for _, s := range []string{
		"please re\u2010run the command",
		"please re\u2013run the command",
		"please re\u00adrun the command",
		"please re\u200drun the command",
		"please re\u2060run the command",
		"please re\ufeffrun the command",
		"ethos \\\nrepair",
		"ethos:repair",
		"ethos: repair",
		"ethos :repair",
		"ethos::repair",
		"run ethos: seed",
	} {
		if bannedIn(s) == "" {
			t.Errorf("%q evades the guard", s)
		}
	}
}

// TestMessageGuardEvasionsInSource pins the declaration and marker cases that
// need a parsed file.
func TestMessageGuardEvasionsInSource(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"function-local const", "package p\nfunc f() string {\n\tconst hint = \"hooks stale; run ethos repair\"\n\treturn hint\n}\n", 1},
		{"exported const declaration", "package p\nconst ProbeRemedy = \"hooks stale; run ethos repair\"\n", 1},
		{"marker inside the literal", "package p\nvar s = \"run ethos repair (msgguard: allow anything)\"\n", 1},
		{"marker on first line of a chain", "package p\nvar s = \"fine\" + // msgguard: allow first line only\n\t\" now run ethos repair\"\n", 1},
		{"marker comment on the line", "package p\nvar s = \"ethos repair\" // msgguard: allow user choice\n", 0},
	}
	for _, c := range cases {
		got := goSourceHits(t, []byte(c.src), "p.go", nil)
		if len(got) != c.want {
			t.Errorf("%s: want %d hits, got %v", c.name, c.want, got)
		}
	}
}

// TestMessageGuardExportedConstAcrossPackages: a const declared in one package
// and used as doctor.X in another is caught where it is declared. Resolving the
// use site across packages is out of scope because the declaration is judged:
// the user-facing text lives in the const, wherever it is printed.
func TestMessageGuardExportedConstAcrossPackages(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) string {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	decl := write("internal/doctor/hint.go", "package doctor\n\nconst Remedy = \"hooks stale; run ethos repair\"\n")
	use := write("cmd/ethos/use.go", "package main\n\nimport \"example.com/internal/doctor\"\n\nvar msg = doctor.Remedy\n")
	if got := goLiteralHits(t, decl, "internal/doctor/hint.go"); len(got) != 1 {
		t.Errorf("declaration: want 1 hit, got %v", got)
	}
	if got := goLiteralHits(t, use, "cmd/ethos/use.go"); len(got) != 0 {
		t.Errorf("use site: want 0 hits (judged at the declaration), got %v", got)
	}
}

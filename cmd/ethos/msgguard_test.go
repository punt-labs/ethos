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
		out[i] = regexp.MustCompile(left + regexp.QuoteMeta(term) + right)
	}
	return out
}()

func bannedIn(s string) string {
	low := strings.ToLower(s)
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
	return ""
}

// goLiteralHits parses one Go file and reports each string literal that
// holds a banned term, unless its line carries the allow marker.
func goLiteralHits(t *testing.T, path, rel string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		term := bannedIn(val)
		if term == "" {
			return true
		}
		line := fset.Position(lit.Pos()).Line
		if msgguardAllow.MatchString(lines[line-1]) {
			return true
		}
		hits = append(hits, rel+":"+strconv.Itoa(line)+": "+term)
		return true
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
	for _, ok := range []string{"restart Claude Code to load new agents", "searching identities by handle", "handheld", "handle-based", "Manage session roster. Methods: roster, join", "show the session roster"} {
		if bannedIn(ok) != "" {
			t.Errorf("%q must pass", ok)
		}
	}
}

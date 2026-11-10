package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every message ethos prints states what this binary did or found. It names no
// later actor and no command this branch removes (DES-084). The phrases below
// are the audit-specific closed set; each must stay absent from every string
// the non-test Go sources under cmd/, internal/ and plugin/ build. The
// commands unit's guard (msgguard_test.go) owns the general removed-command
// list; after the two branches merge this guard should call its matcher and
// keep only auditMessagePhrases.
var auditMessagePhrases = []string{
	"ethos audit quarantine",
	"ethos session purge",
	"retires at session start",
	"retires dead sessions at session start",
	"the next commit includes it",
	"retry in full mode",
	"the storage upgrade finishes it",
	"repair or clear it",
}

// auditMessageImperatives are instructions to a person about the audit store:
// a verb that tells the reader to run or retry something, or a word that only
// ever addresses a person. Descriptive help text ("Run the seal ...") names no
// reader and is not matched.
var auditMessageImperatives = regexp.MustCompile(`\b(?:(?:run|re run|rerun|retry) (?:ethos|it|the (?:seal|audit|command))|please|manually)\b`)

// foldSpace folds each run of whitespace, hyphen or underscore to one space,
// so "ethos-audit-quarantine" and "ethos   audit\tquarantine" read as one
// phrase. Other punctuation is kept: "ethos: audit quarantine:" is a label,
// not a phrase.
var foldSpace = regexp.MustCompile(`[\s\p{Z}\p{Pd}_]+`)

func normalizeMessage(s string) string {
	return strings.TrimSpace(foldSpace.ReplaceAllString(strings.ToLower(s), " "))
}

// bannedMessage returns the phrase or pattern s matches, or "". The
// imperative patterns apply only when imperatives is true (a file of the audit
// store); the removed-command phrases apply everywhere.
func bannedMessage(s string, imperatives bool) string {
	n := normalizeMessage(s)
	for _, p := range auditMessagePhrases {
		if strings.Contains(n, normalizeMessage(p)) {
			return p
		}
	}
	if imperatives {
		if m := auditMessageImperatives.FindString(n); m != "" {
			return m
		}
	}
	return ""
}

// auditScoped reports whether path is part of the audit store, whose messages
// must not instruct the reader. The other units' messages (doctor, uninstall,
// mission) are theirs to judge.
func auditScoped(path string) bool {
	p := filepath.ToSlash(path)
	return strings.Contains(p, "internal/audit/") || strings.Contains(p, "internal/autorepair/") ||
		strings.HasPrefix(filepath.Base(p), "audit") && (strings.Contains(p, "cmd/ethos/") || strings.Contains(p, "internal/hook/"))
}

// foldMessage returns the constant value of n when it is a string literal, a
// package-level string const, or a "+" chain of those.
func foldMessage(n ast.Node, consts map[string]string) (string, bool) {
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
		return foldMessage(x.X, consts)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, lok := foldMessage(x.X, consts)
		r, rok := foldMessage(x.Y, consts)
		return l + r, lok && rok
	}
	return "", false
}

// messageHits parses every file of src (name to source) of one package and
// reports each string expression, folded whole, that holds a banned message.
func messageHits(t *testing.T, src map[string]string, imperatives func(path string) bool) []string {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	for name, body := range src {
		f, err := parser.ParseFile(fset, name, body, parser.SkipObjectResolution)
		require.NoError(t, err, name)
		files = append(files, f)
	}
	consts := map[string]string{}
	for pass := 0; pass < 2; pass++ {
		for _, f := range files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, sp := range gd.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i < len(vs.Values) {
							if v, ok := foldMessage(vs.Values[i], consts); ok {
								consts[name.Name] = v
							}
						}
					}
				}
			}
		}
	}
	var hits []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			// A const is judged where it is used, not where it is declared.
			if gd, ok := n.(*ast.GenDecl); ok && gd.Tok == token.CONST {
				return false
			}
			v, ok := foldMessage(n, consts)
			if !ok {
				return true
			}
			_, chain := n.(*ast.BinaryExpr)
			if p := bannedMessage(v, imperatives(fset.Position(n.Pos()).Filename)); p != "" {
				hits = append(hits, fset.Position(n.Pos()).String()+": "+p)
			}
			return !chain
		})
	}
	return hits
}

func TestMessagesStateOnlyWhatThisCodeDid(t *testing.T) {
	root := filepath.Join("..", "..")
	var hits []string
	for _, dir := range []string{"cmd", "internal", "plugin"} {
		pkgs := map[string]map[string]string{}
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			// The command table names every cobra path the refusal policy
			// covers; removing the `audit quarantine` entry belongs to the
			// commands unit that deletes the command.
			if filepath.Base(path) == "store_refusal.go" {
				return nil
			}
			// internal/session/store.go names `ethos session purge` in a
			// stderr notice owned by the session unit; it is not edited here.
			if filepath.ToSlash(path) == "../../internal/session/store.go" {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			pkg := filepath.Dir(path)
			if pkgs[pkg] == nil {
				pkgs[pkg] = map[string]string{}
			}
			pkgs[pkg][path] = string(b)
			return nil
		}))
		for _, src := range pkgs {
			hits = append(hits, messageHits(t, src, auditScoped)...)
		}
	}
	assert.Empty(t, hits)
}

func always(string) bool { return true }

// The guard catches the spellings a fixed-substring match missed: a hyphenated
// command, a literal assembled with "+", a const, and an instruction to run a
// removed command.
func TestMessageGuardEvasionsAreCaught(t *testing.T) {
	for name, src := range map[string]string{
		"hyphen":   `package p; var a = "recover with ethos-audit-quarantine"`,
		"concat":   `package p; var a = "recover with ethos audit " + "quarantine <chunk>"`,
		"run":      `package p; var a = "run ethos session purge to clear it"`,
		"const":    `package p; const c = "ethos audit "; var a = c + "quarantine"`,
		"retry":    `package p; var a = "retry the seal later"`,
		"manually": `package p; var a = "clean it up manually"`,
		"please":   `package p; var a = "please remove it"`,
		"spaces":   "package p; var a = \"ethos  audit\\tquarantine\"",
	} {
		assert.NotEmpty(t, messageHits(t, map[string]string{"p.go": src}, always), name)
	}
	for _, src := range []string{
		`package p; var a = "audit quarantined 3 lines"`,
		`package p; var a = "second run changed nothing"`,
		`package p; var a = "sessions purged: 2"`,
		`package p; var a = "ethos: audit quarantine: %v"`,
	} {
		assert.Empty(t, messageHits(t, map[string]string{"p.go": src}, always), src)
	}
}

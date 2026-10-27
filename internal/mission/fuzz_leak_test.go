package mission

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/punt-labs/ethos/v4/internal/redact"
	"gopkg.in/yaml.v3"
)

// leakSeeds is every input a reviewer has used to get a home path past the
// redactor, plus the shapes around them.
var leakSeeds = []string{
	"/home/zed/x", "///home/bob/x", "file:///home/bob/x", "file:///home/zed/x",
	"-I/home/bob/inc", "-I/home/zed/inc", "-I/home/alice/inc", "Bash(gcc -I/home/zed/inc:*)",
	"x-home-zed-proj", "-home-bob-proj", "/home/zed,bak", "/home/zed;x", "[/home/zed]",
	"Read(//home/zed/x)", "Read(//home/bob/x)", "Read(/home/zed)",
	"Bash(cd /home/zed/x && make check)",
	"~/.claude/projects/-home-zed-Coding/memory/MEMORY.md",
	"/home/zed/.claude/projects/-home-zed-Coding/memory/MEMORY.md",
	"/work/repo/home/alice/x", "~/Users/bob/x", "<repo>/home/bob/x", "~[redacted-user]/home/bob/x",
	"'/home/zed'", `"/home/bob"`, "(/home/zed)", "a:/home/bob/x", "a=/home/zed/x",
	"testdata/home/zed/x", "/tmp/a//home/bob/x", "//home//zed", "/Users/bob/x",
	"/home/zed", "zed", "a/zed/b", "/work/repo", "/work/repo/x", "~", "<repo>",
	"/home/bob\n/home/zed", "\t/home/zed", "/home/", "/home", "-home-", "--home-zed--",
	"x-var-lib-jenkins-proj", "/var/lib/jenkins/x", "/work/repo/internal/runner/x.go",
	"internal/runner/", "x~/home/zed/secret", "~/home/bob/.ssh/id", "/home/runner/x",
	"-var-lib-jenkins", "/Users/zed/x", "-Users-zed-proj",
	"x/home/alice/y", "!/home/alice/x", "/HOME/Zed/x", "/Users/ZED/x", "/home/./zed/x",
	"/./home/zed/x", "/home//zed/x", "%2Fhome%2Fzed%2Fx", `\/home\/zed\/x`,
	"/home/z\u200bed/x", "/home/zéd/x", "/work/repo/home/bob/x", "/home/0/home/0",
	"cat /work/repo/home/bob/x", "https://example.com/home/docs", "app/home/page.tsx",
	"src/Controllers/Users/Index.cs", "/home/zed/a", "~/a",
	"cat /tmp/../home/bob/.ssh/config", "cd ../../home/bob", "../home/bob/x", "./home/bob/x",
	"--rootdir=.", "/rootfs/x", "/ROOTFS", "/root/x", "-root-proj-", "-root", "/root",
	"x/../home/zed/y", "/a/b/../../home/zed",
	"gcc -I/home/bob/inc", "git -C /home/bob/repo", "cd ~/../bob", "/home/zed/../bob/x", "<repo>/../x",
	"edit ./home/page.tsx", "testdata/../home/alice/x", "fix-home-page-layout", "cmd/root/x", "--root-dir x", "pre-root-x",
	"-home-bob-proj", "a=-home-bob-x", "/x/../home/zed/y", "/./home/bob/x", "/../home/bob",
	"`-home-bob-p`", "a\\n-home-zed-proj", "a\n-Users-zed-p", "--include-dir=/home/bob/inc", "https://h.example.com/home/x", ",-home-bob-x-,", "[-home-bob-x-]",
	"cd /home/\t/../jfreeman/x", "/home/\n/../bob/x", "/home/ /../bob/x", "/a /../home/zed/x", "/home/\t\t/../../bob",
	"Read(/.//home/bob/**)", "Read(%2Fhome%2Fbob/**)", "Read(/%68ome/bob)", "file:%2F%2F%2Fhome%2Fbob", "Read(%2Dhome-bob-proj)", " /home/bob/x", "x=/home/bob/x", "a:/home/bob/x", "////////////////////home/bob/x", "Read(app/home/x)",
}

// fuzzHomes are the home directories the fuzz targets run under: the common
// shapes, a system account whose home is several components deep, and one
// whose name is an ordinary directory name.
var fuzzHomes = []string{"/home/zed", "/Users/zed", "/var/lib/jenkins", "/home/runner", "/root"}

// oracleFold lower-cases s and collapses slash runs, removes /./ and resolves
// /x/../, the spellings a filesystem treats as one path.
func oracleFold(s string) string {
	s = strings.ToLower(s)
	// Written apart from redact.normalizePath: split on / and pop a component
	// on each .. unless the component holds whitespace: a shell word boundary
	// is not a path component, and a word followed by .. is shell text.
	segs := strings.Split(s, "/")
	var out []string
	for i, seg := range segs {
		switch {
		case seg == "." || (seg == "" && i > 0):
			continue
		case seg == ".." && len(out) > 0 && out[len(out)-1] != ".." && !(len(out) == 1 && out[0] == "") && !strings.ContainsAny(out[len(out)-1], " \t\n\f\r"):
			out = out[:len(out)-1]
		default:
			out = append(out, seg)
		}
	}
	return strings.Join(out, "/")
}

func oracleNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// oracleEnds reports whether sub occurs in n ending at the end of a path
// component (at the end of n or before a byte that is no part of a name; with
// dash, also before a -).
func oracleEnds(n, sub string, dash bool) bool {
	for i := 0; ; {
		j := strings.Index(n[i:], sub)
		if j < 0 {
			return false
		}
		e := i + j + len(sub)
		if e == len(n) || !oracleNameByte(n[e]) || (dash && n[e] == '-') {
			return true
		}
		i += j + 1
	}
}

// holdsHome is the fuzz oracle, written apart from redact.LeakRule and as plain
// substring tests, on the text as written, JSON-unescaped, percent-decoded and
// folded. It reports whether v holds this machine's home or its dash form
// ending at a component end; and, unless ownOnly, /home/<x> or /Users/<x>, or
// -home-<x>- or -Users-<x>-, anywhere, for any non-empty <x>.
func holdsHome(v, home string, ownOnly bool) bool {
	h := oracleFold(home)
	variants := []string{v, strings.ReplaceAll(v, `\/`, "/")}
	for _, c := range variants[:2] {
		if p, err := url.PathUnescape(c); err == nil {
			variants = append(variants, p)
		}
	}
	const stop = " \t\n\f\r\"'`/"
	for _, c := range variants {
		n := oracleFold(c)
		if oracleEnds(n, h, false) || oracleEnds(n, strings.ReplaceAll(h, "/", "-"), true) {
			return true
		}
		if ownOnly {
			continue
		}
		for _, pre := range []string{"/home/", "/users/"} {
			for i := 0; ; {
				j := strings.Index(n[i:], pre)
				if j < 0 {
					break
				}
				at := i + j + len(pre)
				i += j + 1
				if at < len(n) && !strings.ContainsRune(stop, rune(n[at])) {
					return true
				}
			}
		}
		for _, pre := range []string{"-home-", "-users-"} {
			for i := 0; ; {
				j := strings.Index(n[i:], pre)
				if j < 0 {
					break
				}
				at := i + j + len(pre)
				i += j + 1
				k := at
				for k < len(n) && !strings.ContainsRune(stop+"-", rune(n[k])) {
					k++
				}
				if k > at && k < len(n) && n[k] == '-' {
					return true
				}
			}
		}
	}
	return false
}

// oStr is a string a writer stored, and whether it sits in a path field as a
// repo-relative value, where only this machine's own home is refused.
type oStr struct {
	v       string
	ownOnly bool
}

// oraclePathKeys name the path-tagged fields of the documents the fuzz builds.
var oraclePathKeys = map[string]bool{"write_set": true, "tools": true, "require_read": true, "files": true, "references": true,
	"repo": true, "extract_into": true, "ticket": true, "path": true, "role": true, "spawn_pattern": true, "files_changed": true}

func oracleRepoRel(v string) bool {
	if v == "" || strings.HasPrefix(v, "/") || strings.Contains(v, "//") {
		return false
	}
	for i, seg := range strings.Split(v, "/") {
		if seg == "" && i == len(strings.Split(v, "/"))-1 {
			continue
		}
		if seg == ".." || seg == "" {
			return false
		}
		for _, c := range seg {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.@+*-", c)) {
				return false
			}
		}
	}
	return true
}

func (o oStr) holds(home string) bool { return holdsHome(o.v, home, o.ownOnly) }

// stringsOf collects every string in a decoded document, map keys included.
func stringsOf(v any, out *[]oStr, path bool) {
	switch x := v.(type) {
	case string:
		*out = append(*out, oStr{x, path && oracleRepoRel(x)})
	case []any:
		for _, e := range x {
			stringsOf(e, out, path)
		}
	case map[string]any:
		for k, e := range x {
			*out = append(*out, oStr{k, false})
			stringsOf(e, out, path || oraclePathKeys[k])
		}
	}
}

// nodeStrings collects every value, tag and comment of a YAML node tree, keys
// of any kind included.
func nodeStrings(n *yaml.Node, out *[]oStr, path bool) {
	own := path && n.Kind == yaml.ScalarNode && oracleRepoRel(n.Value)
	*out = append(*out, oStr{n.Value, own})
	for _, v := range []string{n.Tag, n.Anchor, n.HeadComment, n.LineComment, n.FootComment} {
		*out = append(*out, oStr{v, false})
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			nodeStrings(n.Content[i], out, false)
			nodeStrings(n.Content[i+1], out, path || (n.Content[i].Kind == yaml.ScalarNode && oraclePathKeys[n.Content[i].Value]))
		}
		return
	}
	for _, c := range n.Content {
		nodeStrings(c, out, path)
	}
}

// yamlDocs are documents holding s where a map key of each kind, a custom tag
// and a plain value can carry it. Built as nodes, so s can be a non-scalar
// key, which a map[string]any cannot hold.
func yamlDocs(s string) [][]byte {
	scalar := func(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
	seq := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{scalar(s)}}
	mapKey := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("a"), scalar(s)}}
	tagged := scalar("v")
	tagged.Tag = "!" + s
	// yaml.v3 loops forever emitting a tag with some bytes, so only a tag of
	// plain characters is built.
	safeTag := strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._~-") == ""
	docs := []*yaml.Node{
		{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("context"), scalar(s), scalar("write_set"),
			{Kind: yaml.SequenceNode, Content: []*yaml.Node{scalar(s)}}}},
		{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("x"),
			{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar(s), scalar("v")}}}},
		{Kind: yaml.MappingNode, Content: []*yaml.Node{seq, scalar("v")}},
		{Kind: yaml.MappingNode, Content: []*yaml.Node{mapKey, scalar("v")}},
		{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("pipeline"), scalar(s)}},
	}
	if safeTag {
		docs = append(docs, &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("context"), tagged}})
	}
	var out [][]byte
	for _, d := range docs {
		if b, err := yaml.Marshal(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{d}}); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// FuzzNoLeak: whatever the input and whichever home, a writer either refuses
// or writes nothing that names the home, the passes are idempotent, and the
// bytes that reach the guarded writer never name the home on disk.
func FuzzNoLeak(f *testing.F) {
	const repo = "/work/repo"
	for i := range fuzzHomes {
		for _, s := range leakSeeds {
			f.Add(s, uint8(i))
		}
	}
	sink := f.TempDir()

	f.Fuzz(func(t *testing.T, s string, h uint8) {
		home := fuzzHomes[int(h)%len(fuzzHomes)]
		t.Setenv("HOME", home)
		r := PathRedactor{Home: home, Repo: repo}

		once := r.Path(s)
		if twice := r.Path(once); twice != once {
			t.Fatalf("Path is not idempotent (home %s): %q -> %q -> %q", home, s, once, twice)
		}
		c1 := r.Content(s)
		if c2 := r.Content(c1); c2 != c1 {
			t.Fatalf("Content is not idempotent (home %s): %q -> %q -> %q", home, s, c1, c2)
		}

		c := Contract{
			Context: s, WriteSet: []string{s}, Tools: []string{s}, SuccessCriteria: []string{s},
			Inputs:        Inputs{Files: []string{s}, References: []string{s}},
			Preconditions: []Precondition{{Form: PreconditionFormExplicit, RequireRead: []string{s}, Message: s}},
		}
		if out, err := RedactRecord(repo, c); err == nil {
			for _, fs := range redact.FieldStrings(out) {
				if holdsHome(fs.Value, home, fs.Path && oracleRepoRel(fs.Value)) {
					t.Fatalf("RedactRecord wrote %q in %s (home %s) for input %q", fs.Value, fs.Name, home, s)
				}
			}
		}

		for _, doc := range yamlDocs(s) {
			out, _, err := redactYAMLDoc(r.Content, r.Path, reflect.TypeOf(Contract{}), doc)
			if err != nil {
				continue
			}
			var back yaml.Node
			if yaml.Unmarshal(out, &back) != nil {
				continue
			}
			var got []oStr
			nodeStrings(&back, &got, false)
			for _, v := range got {
				if v.holds(home) {
					t.Fatalf("redactYAMLDoc wrote %q (home %s) for input %q in %q", v.v, home, s, doc)
				}
			}
		}

		line, err := json.Marshal(map[string]any{
			"success_criteria": []string{s}, "write_set": []string{s}, "repo": s, "x": s,
			"y": map[string]any{s: "v"},
		})
		if err == nil {
			if out, _, err := redactJSONLines(r.Content, r.Path, reflect.TypeOf(TraceSummary{}), line); err == nil {
				var back map[string]any
				if json.Unmarshal(out, &back) == nil {
					var got []oStr
					stringsOf(back, &got, false)
					for _, v := range got {
						if v.holds(home) {
							t.Fatalf("redactJSONLines wrote %q (home %s) for input %q", v.v, home, s)
						}
					}
				}
			}
		}

		// The sink: arbitrary bytes bound for the tracked tree, including YAML
		// whose comments, anchors, aliases and tags carry s, and raw byte
		// variants of each document. Whatever writeAtomicFile accepts is
		// written in its canonical encoding, and the bytes on disk hold no
		// home outside an exempt path field.
		files := map[string][]byte{"p.md": []byte("x " + s + " y"), "l.jsonl": append(line, '\n')}
		// One raw variant per document, chosen by the input, keeps the
		// number of fsynced writes per run small.
		docs := append(yamlDocs(s), yamlSyntaxDocs(s)...)
		for i, d := range docs {
			vs := rawVariants(d)
			v := vs[(len(s)+i)%len(vs)]
			if i%2 == 0 {
				files[fmt.Sprintf("d%d.yaml", i)] = v
			} else {
				files[fmt.Sprintf("c%d/contract.yaml", i)] = v
			}
		}
		for name, data := range files {
			dest := filepath.Join(sink, name)
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(dest)
			if err := writeAtomicFile(filepath.Dir(dest), ".fuzz-*.tmp", dest, data); err != nil {
				continue
			}
			got, err := os.ReadFile(dest)
			if err != nil {
				t.Fatalf("reading back %s: %v", name, err)
			}
			if c := canonicalOf(t, dest, got); c != string(got) {
				t.Fatalf("%s is not in its canonical encoding (home %s) for input %q:\n%q\nwant %q", name, home, s, got, c)
			}
			if leak := diskLeak(got, filepath.Base(name), home); leak != "" {
				t.Fatalf("the guarded writer let %q into %s (home %s) for input %q", leak, name, home, s)
			}
		}
	})
}

// yamlSyntaxDocs are documents in which s rides only on syntax the canonical
// encoding drops: comments in every position, around an alias too, an anchor
// name and a custom tag.
func yamlSyntaxDocs(s string) [][]byte {
	// yaml.v3 panics emitting a comment that is not valid UTF-8.
	if !utf8.ValidString(s) || strings.ContainsAny(s, "\r\n") {
		return nil
	}
	scalar := func(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
	anchored := scalar("hi")
	anchored.Anchor = "a"
	alias := &yaml.Node{Kind: yaml.AliasNode, Alias: anchored, Value: "a", HeadComment: "# " + s, LineComment: "# " + s, FootComment: "# " + s}
	commented := scalar("ok")
	commented.HeadComment, commented.LineComment, commented.FootComment = "# "+s, "# "+s, "# "+s
	docs := []*yaml.Node{
		{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("notes"), anchored, scalar("context"), alias}},
		{Kind: yaml.MappingNode, HeadComment: "# " + s, Content: []*yaml.Node{scalar("context"), commented}},
	}
	if anchorSafe(s) {
		named := scalar("hi")
		named.Anchor = s
		docs = append(docs, &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("goal"), named, scalar("context"), scalar("ok")}})
		tagged := scalar("v")
		tagged.Tag = "!" + s
		docs = append(docs, &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("context"), tagged}})
	}
	var out [][]byte
	for _, d := range docs {
		if b, err := yaml.Marshal(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{d}}); err == nil {
			out = append(out, b)
		}
	}
	// A directive carrying s, ahead of a plain document.
	if anchorSafe(s) {
		out = append(out, []byte("%TAG !e! tag:example.com,2000:"+s+"/\n---\ncontext: !e!x ok\n"))
	}
	return out
}

// anchorSafe reports whether s is made of characters yaml.v3 emits in an
// anchor or tag without looping or failing.
func anchorSafe(s string) bool {
	return s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._~-") == ""
}

// rawVariants is d as written, behind a byte-order mark, with each other line
// break YAML or Unicode knows in place of \n, and in UTF-16.
func rawVariants(d []byte) [][]byte {
	s := string(d)
	return [][]byte{
		d,
		append([]byte("\ufeff"), d...),
		[]byte(strings.ReplaceAll(s, "\n", "\r")),
		[]byte(strings.ReplaceAll(s, "\n", "\u0085")),
		[]byte(strings.ReplaceAll(s, "\n", "\u2028")),
		[]byte(strings.ReplaceAll(s, "\n", "\u2029")),
		utf16le(s),
	}
}

// diskLeak returns the first decoded string or key of a file that the oracle
// matches outside an exempt path field, or "". The decoded text is judged, not
// the bytes: they hold the escapes of the encoding (a tab is \t in YAML), which
// are no part of any value. A contract's path fields are blanked when they hold
// a plain relative path. Markdown is text and is judged as written.
func diskLeak(got []byte, base, home string) string {
	var strs []oStr
	switch filepath.Ext(base) {
	case ".yaml":
		var n yaml.Node
		if err := yaml.Unmarshal(got, &n); err != nil {
			return "an undecodable file: " + string(got)
		}
		if base == "contract.yaml" {
			blankExempt(&n, false)
		}
		nodeStrings(&n, &strs, false)
	case ".jsonl":
		for _, line := range strings.Split(string(got), "\n") {
			var v any
			if strings.TrimSpace(line) == "" {
				continue
			}
			if err := json.Unmarshal([]byte(line), &v); err != nil {
				return "an undecodable line: " + line
			}
			stringsOf(v, &strs, false)
		}
	default:
		strs = []oStr{{v: string(got)}}
	}
	for _, s := range strs {
		if s.holds(home) {
			return s.v
		}
	}
	return ""
}

// blankExempt replaces each plain relative path under a path key with _.
func blankExempt(n *yaml.Node, path bool) {
	if n.Kind == yaml.ScalarNode && path && oracleRepoRel(n.Value) {
		n.Value = "_"
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			blankExempt(n.Content[i+1], path || (k.Kind == yaml.ScalarNode && oraclePathKeys[k.Value]))
		}
		return
	}
	for _, c := range n.Content {
		blankExempt(c, path)
	}
}

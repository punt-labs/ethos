package hook

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// auditHomes are the home directories the fuzz target runs under: the common
// shapes, a system account whose home is several components deep, and one
// whose name is an ordinary directory name.
var auditHomes = []string{"/home/zed", "/Users/zed", "/var/lib/jenkins", "/home/runner", "/root"}

// auditFold lower-cases s and collapses slash runs, removes /./ and resolves
// /x/../, the spellings a filesystem treats as one path.
func auditFold(s string) string {
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

func auditNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// auditEnds reports whether sub occurs in n ending at the end of a path
// component (at the end of n or before a byte that is no part of a name; with
// dash, also before a -).
func auditEnds(n, sub string, dash bool) bool {
	for i := 0; ; {
		j := strings.Index(n[i:], sub)
		if j < 0 {
			return false
		}
		e := i + j + len(sub)
		if e == len(n) || !auditNameByte(n[e]) || (dash && n[e] == '-') {
			return true
		}
		i += j + 1
	}
}

// auditHoldsHome is the fuzz oracle, written apart from redact.LeakRule and as plain
// substring tests, on the text as written, JSON-unescaped, percent-decoded and
// folded. It reports whether v holds this machine's home or its dash form
// ending at a component end; and, unless ownOnly, /home/<x> or /Users/<x>, or
// -home-<x>- or -Users-<x>-, anywhere, for any non-empty <x>.
func auditHoldsHome(v, home string, ownOnly bool) bool {
	h := auditFold(home)
	variants := []string{v, strings.ReplaceAll(v, `\/`, "/")}
	for _, c := range variants[:2] {
		if p, err := url.PathUnescape(c); err == nil {
			variants = append(variants, p)
		}
	}
	const stop = " \t\n\f\r\"'`/"
	for _, c := range variants {
		n := auditFold(c)
		if auditEnds(n, h, false) || auditEnds(n, strings.ReplaceAll(h, "/", "-"), true) {
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

// auditStrings collects every string in v, map keys included.
func auditStrings(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		*out = append(*out, x)
	case []any:
		for _, e := range x {
			auditStrings(e, out)
		}
	case map[string]any:
		for k, e := range x {
			*out = append(*out, k)
			auditStrings(e, out)
		}
	}
}

// FuzzAuditNoLeak: whatever a tool call carries and whichever home, the audit
// writer stores no string or key that names the home. It replaces what it
// cannot place; it never blocks the call. The same holds for any line that
// reaches the appender, whatever fields it has.
func FuzzAuditNoLeak(f *testing.F) {
	seeds := []string{
		"/home/zed/x", "///home/bob/.ssh/id_ed25519", "file:///home/bob/x", "-I/home/bob/inc",
		"-I/home/alice/inc", "Bash(gcc -I/home/zed/inc:*)", "x-home-zed-proj", "/home/zed,bak",
		"Read(//home/zed/x)", "~/.claude/projects/-home-zed-Coding/memory/MEMORY.md",
		"/work/repo/home/alice/x", "~/Users/bob/x", "'/home/zed'", "a:/home/bob/x",
		"testdata/home/zed/x", "/tmp/a//home/bob/x", "zed", "a/zed/b", "/home/0\xaf",
		"x-var-lib-jenkins-proj", "/var/lib/jenkins/x", "/work/repo/internal/runner/x.go",
		"x~/home/zed/secret", "~/home/bob/.ssh/id", "/home/runner/x", "/Users/zed/x", "-Users-zed-proj",
		"x/home/alice/y", "!/home/alice/x", "/HOME/Zed/x", "/Users/ZED/x", "/home/./zed/x",
		"/home//zed/x", "%2Fhome%2Fzed%2Fx", `\/home\/zed\/x`, "/home/z\u200bed/x", "/home/zéd/x",
		"/home/me/a", "~/a", "app/home/page.tsx", "https://example.com/home/docs",
		"src/Controllers/Users/Index.cs", "/work/repo/app/home/page.tsx",
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
	for i := range auditHomes {
		for _, s := range seeds {
			f.Add(s, uint8(i))
		}
	}
	sink := filepath.Join(f.TempDir(), "audit.jsonl")
	f.Fuzz(func(t *testing.T, s string, h uint8) {
		home := auditHomes[int(h)%len(auditHomes)]
		t.Setenv("HOME", home)

		check := func(what string, v any) {
			t.Helper()
			var got []string
			auditStrings(v, &got)
			for _, g := range got {
				if auditHoldsHome(g, home, false) {
					t.Fatalf("%s wrote %q (home %s) for input %q", what, g, home, s)
				}
			}
		}

		in := map[string]any{"tool_input": map[string]any{
			"file_path": s, "path": s, "command": s,
			"paths": []any{s}, "edits": []any{map[string]any{"old": s}},
			"env": map[string]any{s: "v", "~/a": "w"},
		}}
		out, _ := redactToolInput(in, "Bash", "/work/repo")
		check("redactToolInput", out)

		// A line with every shape of field an appender can be handed: a string
		// tool_input, an unknown cwd, enrichment fields, a key that is the
		// leak, two keys that collide once renamed.
		line, err := json.Marshal(map[string]any{
			"ts": "2026-01-01T00:00:00Z", "session": "s", "tool": "Read",
			"tool_input": s, "tool_input_preview": s, "audit_error": s, "cwd": s,
			"agent_type": s, "agent_id": s, "delegation_id": s,
			"extra": map[string]any{s: "1", "~/a": "2"},
		})
		if err != nil {
			return
		}
		g := guardAuditLine(line)
		if leak := decodedLeak(g, home); leak != "" {
			t.Fatalf("guardAuditLine kept %q (home %s) for input %q", leak, home, s)
		}
		var back any
		if err := json.Unmarshal(g, &back); err != nil {
			t.Fatalf("guardAuditLine returned invalid JSON %q for input %q", g, s)
		}
		check("guardAuditLine", back)

		// No value is lost: every leaf of the input that is not a leak
		// appears in the output at least as often, unless the whole line
		// became a loss marker (it then has no agent_type key).
		if m, ok := back.(map[string]any); ok {
			if _, has := m["agent_type"]; has {
				var in any
				dec := json.NewDecoder(bytes.NewReader(line))
				dec.UseNumber()
				if err := dec.Decode(&in); err == nil {
					if leaf, ok := testhelpers.LostLeaf(lineLeaves(in), lineLeaves(back)); ok {
						t.Fatalf("a value was lost: %q is in the input and nothing stands in for it in %q (home %s, input %q)", leaf, g, home, s)
					}
				}
			}
		}
		if want := remarshalled(g); !bytes.Equal(want, g) {
			t.Fatalf("guardAuditLine wrote %q, not the re-marshalled form %q", g, want)
		}
		// The raw input itself, in any spelling: what comes out is always a
		// re-marshalled object with no home.
		for _, raw := range [][]byte{[]byte(s), append([]byte("\ufeff"), line...), bytes.ReplaceAll(line, []byte(","), []byte(" ,\r\n"))} {
			g := guardAuditLine(raw)
			if leak := decodedLeak(g, home); leak != "" {
				t.Fatalf("guardAuditLine kept %q (home %s) for raw input %q", leak, home, raw)
			}
			if want := remarshalled(g); !bytes.Equal(want, g) {
				t.Fatalf("guardAuditLine wrote %q for raw input %q, not a re-marshalled object", g, raw)
			}
		}

		// The appender itself.
		_ = os.Remove(sink)
		if err := writeAuditEntry(sink, auditEntry{
			Ts: "2026-01-01T00:00:00Z", Session: "s", Tool: "Read", AgentType: s, AgentID: s,
			ToolInput: map[string]any{"file_path": s},
		}); err != nil {
			t.Fatalf("writeAuditEntry: %v", err)
		}
		// The whole file is read, so no line is cut off by a token limit and
		// a read error fails the test.
		stored, err := os.ReadFile(sink)
		if err != nil {
			t.Fatalf("reading the appended line back: %v", err)
		}
		for _, l := range strings.Split(string(stored), "\n") {
			if decodedLeak([]byte(l), home) != "" {
				t.Fatalf("the appender stored %q (home %s) for input %q", l, home, s)
			}
		}
	})
}

// lineLeaves are the leaves of a decoded audit line, per key path. The fields a
// rewrite recomputes or coerces (redacted, tool_input_hash, tool_input_preview,
// a non-map tool_input) are left out of both sides before they are compared.
func lineLeaves(v any) testhelpers.Leaves {
	if m, ok := v.(map[string]any); ok {
		c := make(map[string]any, len(m))
		for k, e := range m {
			c[k] = e
		}
		delete(c, "redacted")
		delete(c, "tool_input_hash")
		delete(c, "tool_input_preview")
		if _, isMap := c["tool_input"].(map[string]any); !isMap {
			delete(c, "tool_input")
		}
		v = c
	}
	return testhelpers.CollectLeaves(v)
}

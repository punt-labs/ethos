package layout_test

import (
	"path"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"a/b", "a/b", true},
		{"a/b", "a/c", false},
		{"a/*", "a/b", true},
		{"a/*", "a/b/c", false},
		{"a/**", "a", true},
		{"a/**", "a/b/c", true},
		{"a/**/z", "a/z", true},
		{"a/**/z", "a/b/c/z", true},
		{"a/**/z", "a/b/c", false},
		{"**/hook", "hook", true},
		{"**/hook", "x/y/hook", true},
		{"a/*.lock", "a/.lock", true},
		{"a/*.local.*", "a/q.local.yaml", true},
		{"a/*.local.*", "a/q.yaml", false},
	}
	for _, tt := range tests {
		if got := layout.Match(tt.pattern, tt.path); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		path string
		want layout.Class
		ok   bool
	}{
		{".punt-labs/ethos/identities/bwk.yaml", layout.Source, true},
		{".punt-labs/ethos/identities/bwk.ext/quarry.yaml", layout.Source, true},
		{".punt-labs/ethos.yaml", layout.Source, true},
		{".claude/agents/bwk.md", layout.Source, true},
		{".githooks/pre-commit", layout.Source, true},
		{".punt-labs/ethos/missions/m-1/contract.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions/m-1/delegations/d-1/record.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions/m-1/corrections.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions.jsonl", layout.Blame, true},
		{".punt-labs/ethos/adrs/DES-079.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions/m-1.contract.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions/m-1/delegations/d-1/prompt.md", layout.Trace, true},
		{".punt-labs/ethos/missions/m-1/reflections.yaml", layout.Trace, true},
		{".punt-labs/ethos/missions/m-1/artifacts/notes.md", layout.Trace, true},
		{".punt-labs/local/ethos/trace/missions/m-1/reflections.yaml", layout.Trace, true},
		// Class 4 wins over the class that would otherwise match.
		{".punt-labs/ethos/missions/m-1/.lock", layout.Never, true},
		{".punt-labs/ethos/missions/.create.lock", layout.Never, true},
		{".punt-labs/ethos/missions/m-1/delegations/d-1/record.yaml.tmp", layout.Never, true},
		{".punt-labs/ethos/identities/bwk.ext/quarry.local.yaml", layout.Never, true},
		{".punt-labs/ethos/identities/bwk.yaml.lock", layout.Never, true},
		{".punt-labs/local/ethos/missions/m-1/s-1.lock", layout.Never, true},
		// No entry.
		{".punt-labs/ethos/ethos.yaml", 0, false},
		{".punt-labs/ethos/unknown/file", 0, false},
		{"README.md", 0, false},
	}
	for _, tt := range tests {
		got, e, ok := layout.Classify(tt.path)
		if ok != tt.ok || got != tt.want {
			t.Errorf("Classify(%q) = %v, %q, %v; want %v, ok=%v", tt.path, got, e.Pattern, ok, tt.want, tt.ok)
		}
	}
}

func TestClassifyNormalizesPath(t *testing.T) {
	for _, p := range []string{
		"./.punt-labs/ethos/missions.jsonl",
		".punt-labs//ethos/missions.jsonl",
		".punt-labs/ethos/./missions.jsonl",
	} {
		if c, _, ok := layout.Classify(p); !ok || c != layout.Blame {
			t.Errorf("Classify(%q) = %v, %v; want blame", p, c, ok)
		}
	}
}

func TestTablePatternsWellFormed(t *testing.T) {
	for _, e := range layout.Table {
		for _, seg := range strings.Split(e.Pattern, "/") {
			if _, err := path.Match(seg, ""); err != nil {
				t.Errorf("pattern %q: bad segment %q: %v", e.Pattern, seg, err)
			}
		}
		if e.Class < layout.Source || e.Class > layout.Never {
			t.Errorf("pattern %q: invalid class %d", e.Pattern, e.Class)
		}
	}
}

// TestClassFourComesFirst pins the one overlap the table resolves by order.
func TestClassFourComesFirst(t *testing.T) {
	seenOther := false
	for _, e := range layout.Table {
		if e.Class != layout.Never {
			seenOther = true
		} else if seenOther {
			t.Errorf("class-4 entry %q follows a non-class-4 entry", e.Pattern)
		}
	}
}

// witness returns a concrete path matching pattern, so every entry can be
// checked for overlap without a hand-written list of paths.
func witness(pattern string) string {
	segs := strings.Split(pattern, "/")
	for i, s := range segs {
		if s == "**" {
			segs[i] = "x/y"
			continue
		}
		segs[i] = strings.ReplaceAll(s, "*", "x")
	}
	return strings.Join(segs, "/")
}

// conflicts returns, for each path, a description of two entries of different
// classes that both match it when neither is class 4.
func conflicts(table []layout.Entry, paths []string) []string {
	var out []string
	for _, p := range paths {
		var first *layout.Entry
		ms := layout.Matches(table, p)
		for i := range ms {
			e := ms[i]
			if e.Class == layout.Never {
				continue
			}
			if first == nil {
				first = &e
				continue
			}
			if e.Class != first.Class {
				out = append(out, p+": "+first.Pattern+" ("+first.Class.String()+") and "+e.Pattern+" ("+e.Class.String()+")")
			}
		}
	}
	return out
}

// unclassified returns the paths no entry of table matches.
func unclassified(table []layout.Entry, paths []string) []string {
	var out []string
	for _, p := range paths {
		if _, _, ok := layout.ClassifyIn(table, p); !ok {
			out = append(out, p)
		}
	}
	return out
}

func witnesses(table []layout.Entry) []string {
	out := make([]string, 0, len(table))
	for _, e := range table {
		out = append(out, witness(e.Pattern))
	}
	return out
}

func TestEntriesAreExclusive(t *testing.T) {
	paths := append(witnesses(layout.Table), writerPaths(t)...)
	for _, c := range conflicts(layout.Table, paths) {
		t.Errorf("overlapping classes: %s", c)
	}
}

// TestExclusivityDetectsOverlap is the mutant: a class-3 entry that overlaps
// the class-2 contract.yaml entry must be reported, and a class-4 overlap
// must not.
func TestExclusivityDetectsOverlap(t *testing.T) {
	mutant := append(append([]layout.Entry{}, layout.Table...),
		layout.Entry{Pattern: ".punt-labs/ethos/missions/*/*.yaml", Class: layout.Trace})
	got := conflicts(mutant, witnesses(mutant))
	if len(got) == 0 {
		t.Fatal("class-2/class-3 overlap not detected")
	}

	ok := append(append([]layout.Entry{}, layout.Table...),
		layout.Entry{Pattern: ".punt-labs/ethos/missions/*/contract.yaml", Class: layout.Never})
	if got := conflicts(ok, witnesses(ok)); len(got) != 0 {
		t.Errorf("class-4 overlap reported: %v", got)
	}
}

func TestEveryWriterPathIsClassified(t *testing.T) {
	paths := writerPaths(t)
	for _, p := range unclassified(layout.Table, paths) {
		t.Errorf("writer created %s, which no entry of layout.Table matches", p)
	}
}

// TestCoverageDetectsUnmatchedWriter is the mutant: a writer path with no
// entry, and a table that lost an entry a writer needs, must both be reported.
func TestCoverageDetectsUnmatchedWriter(t *testing.T) {
	paths := append(writerPaths(t), ".punt-labs/ethos/newthing/file.bin")
	got := unclassified(layout.Table, paths)
	if len(got) != 1 || got[0] != ".punt-labs/ethos/newthing/file.bin" {
		t.Errorf("unclassified = %v, want the new writer path only", got)
	}

	var trimmed []layout.Entry
	for _, e := range layout.Table {
		if e.Pattern != ".punt-labs/ethos/missions/*/contract.yaml" {
			trimmed = append(trimmed, e)
		}
	}
	if got := unclassified(trimmed, writerPaths(t)); len(got) == 0 {
		t.Error("removing the contract.yaml entry left every writer path classified")
	}
}

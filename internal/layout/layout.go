// Package layout classifies every path ethos writes inside a repository's
// working tree (DES-079). The ordered Table is the authority; prose lists of
// paths cannot stay complete as writers are added, a failing test can.
package layout

import (
	"path"
	"path/filepath"
	"strings"
)

// Class says what a file is, and so whether git should track it.
type Class int

const (
	// Source is configuration and the identity graph: tracked.
	Source Class = 1
	// Blame is the record git blame provenance needs: tracked by default.
	Blame Class = 2
	// Trace is forensic detail: machine-local unless the repository opts in.
	Trace Class = 3
	// Never is transient or machine-specific: never tracked, in any mode.
	Never Class = 4
)

func (c Class) String() string {
	switch c {
	case Source:
		return "source"
	case Blame:
		return "blame"
	case Trace:
		return "trace"
	case Never:
		return "never"
	}
	return "unknown"
}

// Entry maps one path pattern, relative to the repository root, to a class.
// A pattern is slash-separated; each segment is a path.Match pattern, and a
// segment of "**" matches zero or more whole segments.
type Entry struct {
	Pattern string
	Class   Class
}

// Classify returns the class of relPath, a path relative to the repository
// root, and the entry that gave it. The first matching entry wins. ok is false
// when no entry matches.
func Classify(relPath string) (Class, Entry, bool) {
	return ClassifyIn(Table, relPath)
}

// ClassifyIn is Classify against an explicit table. The tests use it to prove
// they fail on a mutated table.
func ClassifyIn(table []Entry, relPath string) (Class, Entry, bool) {
	p := normalize(relPath)
	for _, e := range table {
		if Match(e.Pattern, p) {
			return e.Class, e, true
		}
	}
	return 0, Entry{}, false
}

// Matches returns every entry of table that matches relPath, in table order.
func Matches(table []Entry, relPath string) []Entry {
	p := normalize(relPath)
	var out []Entry
	for _, e := range table {
		if Match(e.Pattern, p) {
			out = append(out, e)
		}
	}
	return out
}

// Match reports whether the slash-separated path p matches pattern.
func Match(pattern, p string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			for i := 0; i <= len(segs); i++ {
				if matchSegments(rest, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

func normalize(p string) string {
	return strings.TrimPrefix(path.Clean(filepath.ToSlash(p)), "./")
}

// Table is the ordered path-class table. Class-4 entries come first so that a
// lock inside a mission directory, or an ext/<ns>.local.yaml file, is never
// tracked; that precedence is the only overlap the table resolves by order.
// Every other pair of entries must be disjoint, which the package tests check.
var Table = []Entry{
	// Class 4: never tracked.
	{".punt-labs/**/*.local.*", Never},
	{".punt-labs/ethos/missions/**/*.lock", Never},
	{".punt-labs/ethos/missions.jsonl.lock", Never},
	{".punt-labs/ethos/identities/*.yaml.lock", Never},
	{".punt-labs/local/ethos/**/*.lock", Never},
	// Temporary names writers create for an atomic rename.
	{".punt-labs/**/*.tmp", Never},
	{".claude/agents/*.tmp", Never},
	{".punt-labs/ethos/.CLAUDE.md.*", Never},
	{".punt-labs/ethos/.ETHOS-SETUP.md.*", Never},
	{".punt-labs/ethos/.vendored-manifest.*", Never},
	{".gitignore.*", Never},
	{".CLAUDE.md.*", Never},
	{"**/.pre-commit.*", Never},
	{"**/.commit-msg.*", Never},

	// Class 3: trace tier. The live files and the record: blame archive sit
	// in the machine-local zone; the rest are written at tracked paths today.
	{".punt-labs/local/ethos/sessions/*.audit.jsonl", Trace},
	{".punt-labs/local/ethos/missions/*/*.log.jsonl", Trace},
	{".punt-labs/local/ethos/missions/*.jsonl", Trace},
	{".punt-labs/local/ethos/trace/**", Trace},
	{".punt-labs/ethos/missions/*/delegations/*/prompt.md", Trace},
	{".punt-labs/ethos/missions/*/reflections.yaml", Trace},
	{".punt-labs/ethos/missions/*/log.jsonl", Trace},
	{".punt-labs/ethos/missions/*/log-*.jsonl", Trace},
	{".punt-labs/ethos/missions/*/log-*.quarantine", Trace},
	{".punt-labs/ethos/missions/*/log-*.jsonl.corrupt*", Trace},
	// Agent-written; no ethos code writes, moves or exports them.
	{".punt-labs/ethos/missions/*/artifacts/**", Trace},
	{".punt-labs/ethos/sessions/*/audit.jsonl", Trace},
	{".punt-labs/ethos/sessions/*/audit-*.jsonl", Trace},
	{".punt-labs/ethos/sessions/*/audit-*.quarantine", Trace},
	{".punt-labs/ethos/sessions/*/audit-*.jsonl.corrupt*", Trace},

	// Class 2: blame tier.
	{".punt-labs/ethos/missions.jsonl", Blame},
	{".punt-labs/ethos/missions/*/contract.yaml", Blame},
	{".punt-labs/ethos/missions/*/results.yaml", Blame},
	{".punt-labs/ethos/missions/*/corrections.yaml", Blame},
	{".punt-labs/ethos/missions/*/delegations/*/record.yaml", Blame},
	{".punt-labs/ethos/missions/*.contract.yaml", Blame},
	{".punt-labs/ethos/missions/*.result.yaml", Blame},
	{".punt-labs/ethos/adrs/*.yaml", Blame},

	// Class 1: source and configuration.
	{".punt-labs/ethos.yaml", Source},
	{".punt-labs/ethos/config.yaml", Source}, // legacy; read, never written
	{".punt-labs/ethos/.vendor.yaml", Source},
	{".punt-labs/ethos/.vendored-manifest", Source},
	{".punt-labs/ethos/CLAUDE.md", Source},
	{".punt-labs/ethos/ETHOS-SETUP.md", Source},
	{".punt-labs/ethos/enabled", Source},
	{".punt-labs/ethos/identities/*.yaml", Source},
	{".punt-labs/ethos/identities/*.ext/*.yaml", Source},
	{".punt-labs/ethos/personalities/*.md", Source},
	{".punt-labs/ethos/writing-styles/*.md", Source},
	{".punt-labs/ethos/talents/*.md", Source},
	{".punt-labs/ethos/roles/*.yaml", Source},
	{".punt-labs/ethos/teams/*.yaml", Source},
	{".punt-labs/ethos/agents/*.md", Source},
	{".punt-labs/ethos/archetypes/*.yaml", Source},
	{".punt-labs/ethos/pipelines/*.yaml", Source},
	{".punt-labs/ethos-bundles/**", Source},
	{".claude/agents/*.md", Source},
	{".gitignore", Source},
	{"CLAUDE.md", Source},
	// The hook files `ethos enable` writes when core.hooksPath puts the hooks
	// directory inside the work tree; the directory is the user's choice.
	{"**/pre-commit", Source},
	{"**/commit-msg", Source},
}

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
	// Fallback marks an entry that claims only paths no other entry matches. It
	// must come after every non-fallback entry, and the exclusivity check does
	// not compare it with them: it cannot take a path from them.
	Fallback bool
	// Temp marks a class-4 entry for a name built by a helper in temp.go.
	Temp bool
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
// lock inside a mission directory, an ext/<ns>.local.yaml file, or a staging
// file is never tracked; order resolves those overlaps. A Fallback entry is the
// other exemption: it claims only what no other entry does, so it may overlap
// any of them. Every pair of entries of different classes, other than class 4
// and fallback entries, must be disjoint, which the package tests check;
// entries of one class may overlap.
var Table = []Entry{
	// Class 4: never tracked.
	{Pattern: ".punt-labs/**/*.local.*", Class: Never},
	{Pattern: ".punt-labs/ethos/missions/**/*.lock", Class: Never},
	{Pattern: ".punt-labs/ethos/missions.jsonl.lock", Class: Never},
	{Pattern: ".punt-labs/ethos/identities/*.yaml.lock", Class: Never},
	{Pattern: ".punt-labs/local/ethos/**/*.lock", Class: Never},
	// The original bytes of a retired chunk the redaction check withheld from
	// the tracked tree: <stem>.<hash>.corrupt, kept in the machine-local zone.
	{Pattern: ".punt-labs/local/ethos/quarantine/*.corrupt", Class: Never},
	// Every staging name a helper in temp.go builds contains TmpMarker: the
	// file, and a staging directory with whatever a crash leaves in it.
	temp("**/*" + TmpMarker + "*"),
	temp("**/*" + TmpMarker + "*/**"),
	// The staging directory `ethos mission migrate` used before the marker,
	// ".<id>.migrate-<random>". Not a Temp entry: new code never builds the
	// name, and the marker set stays marker-only.
	{Pattern: ".punt-labs/ethos/missions/.*.migrate-*/**", Class: Never},

	// Class 3: trace tier. The live files and the record: blame archive sit
	// in the machine-local zone; the rest are written at tracked paths today.
	{Pattern: ".punt-labs/local/ethos/sessions/*.audit.jsonl", Class: Trace},
	{Pattern: ".punt-labs/local/ethos/missions/*/*.log.jsonl", Class: Trace},
	{Pattern: ".punt-labs/local/ethos/missions/*.jsonl", Class: Trace},
	{Pattern: ".punt-labs/local/ethos/trace/**", Class: Trace},
	{Pattern: ".punt-labs/ethos/missions/*/delegations/*/prompt.md", Class: Trace},
	{Pattern: ".punt-labs/ethos/missions/*/reflections.yaml", Class: Trace},
	{Pattern: ".punt-labs/ethos/missions/*/log.jsonl", Class: Trace},
	{Pattern: ".punt-labs/ethos/missions/*/log-*.jsonl", Class: Trace},
	{Pattern: ".punt-labs/ethos/missions/*/log-*.quarantine", Class: Trace},
	{Pattern: ".punt-labs/ethos/missions/*/log-*.jsonl.corrupt*", Class: Trace},
	// Agent-written; no ethos code writes, moves or exports them.
	{Pattern: ".punt-labs/ethos/missions/*/artifacts/**", Class: Trace},
	{Pattern: ".punt-labs/ethos/sessions/*/audit.jsonl", Class: Trace},
	{Pattern: ".punt-labs/ethos/sessions/*/audit-*.jsonl", Class: Trace},
	{Pattern: ".punt-labs/ethos/sessions/*/audit-*.quarantine", Class: Trace},
	{Pattern: ".punt-labs/ethos/sessions/*/audit-*.jsonl.corrupt*", Class: Trace},

	// Class 2: blame tier.
	{Pattern: ".punt-labs/ethos/missions.jsonl", Class: Blame},
	{Pattern: ".punt-labs/ethos/missions/*/contract.yaml", Class: Blame},
	{Pattern: ".punt-labs/ethos/missions/*/results.yaml", Class: Blame},
	// No writer yet: the corrections slice of ethos-g3g3 will create it. The
	// entry is kept so the file has a class on the day it appears.
	{Pattern: ".punt-labs/ethos/missions/*/corrections.yaml", Class: Blame},
	{Pattern: ".punt-labs/ethos/missions/*/delegations/*/record.yaml", Class: Blame},
	{Pattern: ".punt-labs/ethos/adrs/*.yaml", Class: Blame},

	// Class 1: source and configuration.
	{Pattern: ".punt-labs/ethos.yaml", Class: Source},
	{Pattern: ".punt-labs/ethos/config.yaml", Class: Source}, // legacy; read, never written
	{Pattern: ".punt-labs/ethos/.vendor.yaml", Class: Source},
	{Pattern: ".punt-labs/ethos/.vendored-manifest", Class: Source},
	{Pattern: ".punt-labs/ethos/CLAUDE.md", Class: Source},
	{Pattern: ".punt-labs/ethos/ETHOS-SETUP.md", Class: Source},
	{Pattern: ".punt-labs/ethos/enabled", Class: Source},
	{Pattern: ".punt-labs/ethos/identities/*.yaml", Class: Source},
	{Pattern: ".punt-labs/ethos/identities/*.ext/*.yaml", Class: Source},
	{Pattern: ".punt-labs/ethos/personalities/*.md", Class: Source},
	{Pattern: ".punt-labs/ethos/writing-styles/*.md", Class: Source},
	{Pattern: ".punt-labs/ethos/talents/*.md", Class: Source},
	{Pattern: ".punt-labs/ethos/roles/*.yaml", Class: Source},
	{Pattern: ".punt-labs/ethos/teams/*.yaml", Class: Source},
	{Pattern: ".punt-labs/ethos/agents/*.md", Class: Source},
	{Pattern: ".punt-labs/ethos/archetypes/*.yaml", Class: Source},
	{Pattern: ".punt-labs/ethos/pipelines/*.yaml", Class: Source},
	{Pattern: ".punt-labs/ethos-bundles/**", Class: Source},
	{Pattern: ".claude/agents/*.md", Class: Source},
	{Pattern: ".gitignore", Class: Source},
	{Pattern: ".gitmodules", Class: Source}, // written by git submodule add (team add-bundle)
	{Pattern: "CLAUDE.md", Class: Source},
	// The hook files `ethos enable` writes when core.hooksPath puts the hooks
	// directory inside the work tree are class 1 too; the directory is the
	// user's choice, so HookEntries derives them from the resolved path.

	// The shape `ethos mission export` writes. With --dir the directory is the
	// caller's choice (missions-closed/ in several repositories), so the shape
	// is claimed anywhere under .punt-labs/ethos, but only where no entry
	// above claims the path.
	{Pattern: ".punt-labs/ethos/**/m-*.contract.yaml", Class: Blame, Fallback: true},
	{Pattern: ".punt-labs/ethos/**/m-*.result.yaml", Class: Blame, Fallback: true},
}

// temp is a class-4 entry for a name a helper in temp.go builds.
func temp(pattern string) Entry {
	return Entry{Pattern: pattern, Class: Never, Temp: true}
}

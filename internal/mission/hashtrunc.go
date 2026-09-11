package mission

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Values YAML silently truncated at an unquoted '#'.
//
// YAML opens a comment at a '#' that follows whitespace, so
//
//	- name: PR #515 merged, 6/6 checks green
//
// parses as the two-character name "PR" and discards the rest. The
// value is still non-empty, so every downstream validator passes and
// the artifact records a claim it no longer makes. This shipped for
// real on PR #516: a result asserting a merge, a CI count, and a
// review-thread count was persisted as "PR" with status: pass, and
// round-tripped through `ethos mission close` intact.
//
// The detection is exact, not a heuristic. yaml.v3 hands back the text
// it discarded as the node's LineComment, so a truncated scalar is
// precisely one that is plain (unquoted) and carries a line comment.
// Nothing else is flagged, because nothing else lost anything:
//
//   - a quoted value keeps its '#' and produces no comment;
//   - a block scalar (prose: |) has no comment syntax at all, so every
//     '#' inside it is content;
//   - a '#' with no leading space ("ethos-56a#2") does not open a
//     comment;
//   - a comment on its own line attaches as a head comment, not a line
//     comment.
//
// A minimum-length rule would have done neither half of this job: it
// rejects legitimate short names ("make check") and misses long
// truncations ("PR #515 ..." truncated to a 40-character prefix).
//
// Which fields are checked follows from one property: whether the
// field's grammar admits whitespace. A handle, an enum, a mission ID, a
// timestamp, and a number cannot contain " #", so a trailing comment
// there discards nothing and flagging it would be a false report — and
// would reject the scaffolds ethos itself ships, which annotate exactly
// those fields. Free text and paths both admit whitespace, so both are
// checked.

// seq is the fieldPath step that iterates a sequence.
const seq = "[]"

// fieldPath is the route from the document root to one or more
// scalars: a plain step enters a mapping key, seq iterates a sequence.
//
//	{"prose"}                        → prose
//	{"success_criteria", seq}        → success_criteria[*]
//	{"evidence", seq, "name"}        → evidence[*].name
//	{"inputs", "trigger", "subject"} → inputs.trigger.subject
type fieldPath []string

// String renders the path the way the error message and the test names
// refer to it, with [] standing in for the index.
func (p fieldPath) String() string {
	var b strings.Builder
	for _, step := range p {
		if step == seq {
			b.WriteString(seq)
			continue
		}
		if b.Len() > 0 {
			b.WriteString(".")
		}
		b.WriteString(step)
	}
	return b.String()
}

// Paths are checked alongside free text because a path is not a
// constrained value: validateWriteSetEntry rejects null bytes, colons,
// drive letters, traversal, and control characters, but not a space.
// `reports/PR #515.txt` therefore becomes `reports/PR`, which can still
// satisfy write-set containment — persisting a different file than the
// worker declared.
var resultHashScope = []fieldPath{
	{"prose"},
	{"open_questions", seq},
	{"evidence", seq, "name"},
	{"files_changed", seq, "path"},
}

var reflectionHashScope = []fieldPath{
	{"reason"},
	{"signals", seq},
}

// A correction is the artifact for retracting a false claim, so claim
// and corrected are the two values in the whole schema most likely to
// cite the PR or bead number that exposed it.
var correctionHashScope = []fieldPath{
	{"claim"},
	{"corrected"},
	{"evidence", seq, "name"},
}

// preconditions[].message is the reason a blocked worker is shown when
// a gate denies a tool call. Validate rejects an empty one so a failed
// gate is never silently named — a truncated one is the same harm with
// none of the noise.
//
// spawn_pattern is a regular expression, and a regex admits
// whitespace, so it belongs here on the same rule as the rest even
// though a pattern containing " #" would be unusual.
var contractHashScope = []fieldPath{
	{"context"},
	{"success_criteria", seq},
	{"write_set", seq},
	{"extract_into", seq},
	{"inputs", "files", seq},
	{"inputs", "references", seq},
	{"inputs", "trigger", "subject"},
	{"preconditions", seq, "message"},
	{"preconditions", seq, "require_read", seq},
	{"delegations", seq, "spawn_pattern"},
	{"delegations", seq, "extract_into", seq},
}

// CheckContractHashTruncation reports the first contract value that
// YAML cut short at an unquoted '#'.
//
// Results, reflections, and corrections fold the check into their own
// strict decoder, because nothing but a submitted file reaches those.
// Contracts cannot: DecodeContractStrict also reads contracts back off
// disk, where the bytes are ethos's own yaml.Marshal output and cannot
// carry a comment. Putting a new rejection on a read path it can never
// help is not worth the risk, so this runs at the three places a
// human's file enters the store instead — `mission create` (CLI and
// MCP) and `mission lint`.
func CheckContractHashTruncation(data []byte, label string) error {
	if err := checkHashTruncation(data, contractHashScope); err != nil {
		return fmt.Errorf("invalid contract %s: %w", label, err)
	}
	return nil
}

// checkHashTruncation returns an error naming the first scalar in data
// that YAML cut short at an unquoted '#'.
func checkHashTruncation(data []byte, scope []fieldPath) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// Every caller decodes these same bytes into a typed struct
		// first, so this parse cannot realistically fail — and if it
		// ever does, the two readings disagree about the same file,
		// which is worth saying out loud rather than passing the
		// submission through unchecked.
		return fmt.Errorf("re-reading the submitted YAML: %w", err)
	}
	root := documentRoot(&doc)
	if root == nil {
		return nil
	}
	if err := rejectIndirection(root); err != nil {
		return err
	}
	for _, path := range scope {
		if err := walkField(root, path, "", hashTruncated); err != nil {
			return err
		}
	}
	return nil
}

// rejectIndirection refuses YAML aliases and merge keys anywhere in a
// submitted artifact.
//
// They defeat the check by construction: it reads the node tree, but
// typed decoding resolves indirection first, so an anchored scalar
// truncated at its '#' arrives in a checked field as an alias node with
// no value and no comment of its own to inspect. Verified reachable —
// an anchor on `author` feeding `evidence[0].name` decodes to the
// truncated "PR" with nothing at the name for the walk to see.
//
// Refusing is the whole fix; resolving would be the wrong one. A
// mission artifact is an audit record, one that needs alias resolution
// to read is a poor audit record, and a resolver subtly wrong about
// nested or recursive merges would reopen the same bypass while looking
// closed. An anchor nothing refers to moves no text and stays legal.
func rejectIndirection(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	// The merge key is checked first because its value is an alias: a
	// bare alias message would name the mechanism and not the syntax
	// the operator typed.
	if n.Tag == mergeTag {
		return fmt.Errorf("line %d: a YAML merge key (<<) is not allowed in a mission artifact; write the fields out in full", n.Line)
	}
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("line %d: a YAML alias (*%s) is not allowed in a mission artifact; write the value out in full", n.Line, n.Value)
	}
	for _, c := range n.Content {
		if err := rejectIndirection(c); err != nil {
			return err
		}
	}
	return nil
}

// walkField follows path from n, calling visit on every node it names.
// A step whose shape does not match the document is not an error here:
// a field of the wrong type is the typed decoder's to report.
func walkField(n *yaml.Node, path fieldPath, label string, visit func(string, *yaml.Node) error) error {
	if n == nil {
		return nil
	}
	if len(path) == 0 {
		return visit(label, n)
	}
	step, rest := path[0], path[1:]
	if step == seq {
		if n.Kind != yaml.SequenceNode {
			return nil
		}
		for i, item := range n.Content {
			if err := walkField(item, rest, fmt.Sprintf("%s[%d]", label, i), visit); err != nil {
				return err
			}
		}
		return nil
	}
	next := label + "." + step
	if label == "" {
		next = step
	}
	return walkField(mapValue(n, step), rest, next, visit)
}

// mergeTag is the resolved tag yaml.v3 gives the `<<` key.
const mergeTag = "!!merge"

// hashTruncated reports an error when n is a plain scalar carrying a
// line comment — text that YAML took off the end of the line and did
// not put in the value.
//
// The message states only what is certain: where YAML ended the value,
// what it kept, and what it dropped. Whether the dropped text was meant
// as part of the value or as a comment is the operator's to say, and
// both remedies are offered. Claiming "truncated" outright would be
// wrong for the legitimate case — a `path/to/file.go   # note` line
// loses nothing — and a guard that overstates what it found is the same
// defect it exists to catch.
func hashTruncated(label string, n *yaml.Node) error {
	if n == nil || n.Kind != yaml.ScalarNode || n.LineComment == "" {
		return nil
	}
	if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return nil
	}
	return fmt.Errorf(
		"%s: line %d ends at an unquoted '#' — YAML kept %q and dropped %q; quote the value if the dropped text belongs to it, or move the comment to its own line",
		label, n.Line, n.Value, n.LineComment)
}

// documentRoot returns the mapping at the top of a decoded document, or
// nil when the document is empty or is not a mapping.
func documentRoot(doc *yaml.Node) *yaml.Node {
	n := doc
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

// mapValue returns the value node stored under key in a mapping node,
// or nil when the node is not a mapping or has no such key.
func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

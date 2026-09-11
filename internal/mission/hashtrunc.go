package mission

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Free-text scalars YAML silently truncated at an unquoted '#'.
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
// Only free-text fields are checked — the ones where a '#' is ordinary
// content and its loss changes meaning. Enum, pattern, numeric, handle,
// and path fields already constrain their parsed value, so a trailing
// comment there cannot corrupt it, and flagging one would reject the
// scaffolds ethos itself ships: ScaffoldResultYAML and
// ScaffoldContractYAML annotate mission, round, author, verdict,
// confidence, and the files_changed path and counts exactly that way.

// hashScope names the free-text scalars of one document schema by the
// shape they take in the YAML: a bare scalar, a list of scalars, or a
// field of each record in a list.
type hashScope struct {
	scalar []string          // key → free-text scalar
	list   []string          // key → list of free-text scalars
	record map[string]string // key → field of each record that is free text
}

var resultHashScope = hashScope{
	scalar: []string{"prose"},
	list:   []string{"open_questions"},
	record: map[string]string{"evidence": "name"},
}

var reflectionHashScope = hashScope{
	scalar: []string{"reason"},
	list:   []string{"signals"},
}

// A correction is the artifact for retracting a false claim, so claim
// and corrected are the two values in the whole schema most likely to
// cite the PR or bead number that exposed it.
var correctionHashScope = hashScope{
	scalar: []string{"claim", "corrected"},
	record: map[string]string{"evidence": "name"},
}

var contractHashScope = hashScope{
	scalar: []string{"context"},
	list:   []string{"success_criteria"},
	record: map[string]string{"delegations": "message"},
}

// CheckContractHashTruncation reports the first free-text contract
// field that YAML truncated at an unquoted '#'.
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

// checkHashTruncation returns an error naming the first free-text
// scalar in data whose value YAML cut short at an unquoted '#'.
func checkHashTruncation(data []byte, scope hashScope) error {
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
	lines := strings.Split(string(data), "\n")
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i].Value, root.Content[i+1]
		switch {
		case slices.Contains(scope.scalar, key):
			if err := hashTruncated(key, val, lines); err != nil {
				return err
			}
		case slices.Contains(scope.list, key):
			for j, item := range sequence(val) {
				if err := hashTruncated(fmt.Sprintf("%s[%d]", key, j), item, lines); err != nil {
					return err
				}
			}
		default:
			field, ok := scope.record[key]
			if !ok {
				continue
			}
			for j, item := range sequence(val) {
				label := fmt.Sprintf("%s[%d].%s", key, j, field)
				if err := hashTruncated(label, mapValue(item, field), lines); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// hashTruncated reports an error when n is a plain scalar carrying a
// line comment — the text YAML took out of its value. The message shows
// what survived beside the line it came from, so the loss is visible
// without opening the file.
func hashTruncated(label string, n *yaml.Node, lines []string) error {
	if n == nil || n.Kind != yaml.ScalarNode || n.LineComment == "" {
		return nil
	}
	if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return nil
	}
	return fmt.Errorf(
		"%s: value truncated at an unquoted '#': YAML kept %q from line %d (%s); quote the value, or move the comment to its own line",
		label, n.Value, n.Line, strings.TrimSpace(sourceLine(lines, n.Line)))
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

// sequence returns the items of a sequence node, or nil for any other
// node. A field of the wrong shape is the typed decoder's error to
// report, not this check's.
func sequence(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
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

// sourceLine returns the 1-indexed line of the submitted file, or "" if
// the node's line is out of range.
func sourceLine(lines []string, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	return lines[line-1]
}

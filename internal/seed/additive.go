package seed

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// additiveMerge repairs a stale file left behind by an older seed: existing
// is what is on disk, data is the shipped content. It returns the merged
// bytes and true when the only difference between the two is that data's
// top-level YAML mapping carries keys existing lacks entirely — the shape
// left by a release that adds a required field to an already-deployed file
// (GH #525: v4.19.0 added require_delegated_worker to implement.yaml and
// test.yaml, but those files predate the seed manifest and so sit in the
// skip-if-exists category forever, with no path back to compliance).
//
// It returns false, with reason naming why, for anything else: a value
// existing shares with data but disagrees on, a key existing has that data
// does not, non-mapping content (most seeded files are Markdown, not YAML),
// or a parse failure. Each of those means the difference is a real edit,
// not a stale-schema gap, and decide's ordinary no-clobber skip must still
// apply — but the reason rides along so the skip line can say why, instead
// of recreating GH #525's own shape one level down: a bare "skipped
// (exists)" gives an operator no way to tell "this needs a hand-edit" from
// "seed is stuck," the exact ambiguity that made the original doctor
// FAIL's remedy look like a no-op.
//
// The merge itself is textual, not a re-marshal: existing's bytes are kept
// verbatim, and each missing key is appended as the literal line range it
// occupies in data, in the order data defines it. A structural re-marshal
// would reformat every line to reflect its own quoting and indentation
// choices, silently rewriting content the user never touched.
func additiveMerge(existing, data []byte) (merged []byte, reason string, ok bool) {
	exRoot, ok := topLevelMapping(existing)
	if !ok {
		return nil, "not a YAML mapping", false
	}
	seedRoot, ok := topLevelMapping(data)
	if !ok {
		return nil, "shipped content is not a YAML mapping", false
	}

	var existingVal, seedVal map[string]any
	if exRoot.Decode(&existingVal) != nil || seedRoot.Decode(&seedVal) != nil {
		return nil, "could not decode as key/value pairs", false
	}

	// A key existing has that data does not is a user addition, not a stale
	// schema gap — leave it to the ordinary skip. Sorted so the named key is
	// deterministic when more than one qualifies.
	var extra []string
	for k := range existingVal {
		if _, ok := seedVal[k]; !ok {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return nil, fmt.Sprintf("local key %q is not in the shipped content", extra[0]), false
	}

	var missing []string
	for i := 0; i < len(seedRoot.Content); i += 2 {
		k := seedRoot.Content[i].Value
		if ev, ok := existingVal[k]; ok {
			if !reflect.DeepEqual(ev, seedVal[k]) {
				return nil, fmt.Sprintf("conflicting value for key %q", k), false
			}
			continue
		}
		missing = append(missing, k)
	}
	if len(missing) == 0 {
		return nil, "no missing keys explain the difference", false
	}

	blocks, ok := keyBlocks(data, seedRoot)
	if !ok {
		return nil, "could not locate the shipped lines for a missing key", false
	}

	merged = existing
	if len(merged) > 0 && merged[len(merged)-1] != '\n' {
		merged = append(merged, '\n')
	}
	for _, k := range missing {
		merged = append(merged, blocks[k]...)
	}
	return merged, "", true
}

// topLevelMapping parses raw as YAML and returns its top-level mapping node.
// It reports false for a parse error, an empty document, or content whose
// root is not a mapping (most seeded content is Markdown, not YAML, and
// even a YAML file can define a top-level sequence or scalar).
func topLevelMapping(raw []byte) (*yaml.Node, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		return nil, false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false
	}
	return root, true
}

// keyBlocks slices raw into the literal line range each of mapping's
// top-level keys occupies — from the key's own line up to (but not
// including) the next key's line, or end of file for the last key — and
// returns them keyed by name. Each block carries its own trailing newline,
// so callers can concatenate blocks directly. It reports false when a key
// node's line number is out of range, which should not happen for a
// mapping just parsed from raw but is checked rather than trusted.
func keyBlocks(raw []byte, mapping *yaml.Node) (map[string][]byte, bool) {
	lines := strings.Split(string(raw), "\n")
	blocks := make(map[string][]byte, len(mapping.Content)/2)
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		start := key.Line
		end := len(lines) + 1
		if i+2 < len(mapping.Content) {
			end = mapping.Content[i+2].Line
		}
		if start < 1 || end < start || end > len(lines)+1 {
			return nil, false
		}
		block := strings.TrimRight(strings.Join(lines[start-1:end-1], "\n"), "\n")
		blocks[key.Value] = []byte(block + "\n")
	}
	return blocks, true
}

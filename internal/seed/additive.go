package seed

import (
	"reflect"
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
// It returns false for anything else: a value existing shares with data but
// disagrees on, a key existing has that data does not, non-mapping content
// (most seeded files are Markdown, not YAML), or a parse failure. Each of
// those means the difference is a real edit, not a stale-schema gap, and
// decide's ordinary no-clobber skip must still apply.
//
// The merge is textual, not a re-marshal: existing's bytes are kept
// verbatim, and each missing key is appended as the literal line range it
// occupies in data, in the order data defines it. A structural re-marshal
// would reformat every line to reflect its own quoting and indentation
// choices, silently rewriting content the user never touched.
func additiveMerge(existing, data []byte) ([]byte, bool) {
	exRoot, ok := topLevelMapping(existing)
	if !ok {
		return nil, false
	}
	seedRoot, ok := topLevelMapping(data)
	if !ok {
		return nil, false
	}

	var existingVal, seedVal map[string]any
	if exRoot.Decode(&existingVal) != nil || seedRoot.Decode(&seedVal) != nil {
		return nil, false
	}

	// A key existing has that data does not is a user addition, not a stale
	// schema gap — leave it to the ordinary skip.
	for k := range existingVal {
		if _, ok := seedVal[k]; !ok {
			return nil, false
		}
	}

	var missing []string
	for i := 0; i < len(seedRoot.Content); i += 2 {
		k := seedRoot.Content[i].Value
		if ev, ok := existingVal[k]; ok {
			if !reflect.DeepEqual(ev, seedVal[k]) {
				return nil, false // shared key, conflicting value
			}
			continue
		}
		missing = append(missing, k)
	}
	if len(missing) == 0 {
		return nil, false // hashes differed, but nothing additive explains why
	}

	blocks, ok := keyBlocks(data, seedRoot)
	if !ok {
		return nil, false
	}

	merged := existing
	if len(merged) > 0 && merged[len(merged)-1] != '\n' {
		merged = append(merged, '\n')
	}
	for _, k := range missing {
		merged = append(merged, blocks[k]...)
	}
	return merged, true
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

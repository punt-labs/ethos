package layout

import "strings"

// LocalPrefix is the repository-relative directory of machine-local ethos
// data. None of it belongs in a commit.
const LocalPrefix = ".punt-labs/local/"

// MisplacedLocal distinguishes physical placement from both classification and
// Git tracking. Published/full/exported trace is legitimate in the source tree;
// live streams, scratch, overlays, locks and staging never are.
func MisplacedLocal(rel string) bool {
	const source = ".punt-labs/ethos/"
	if !strings.HasPrefix(rel, source) {
		return false
	}
	if class, _, ok := Classify(rel); ok && class == Never {
		return true
	}
	for _, pattern := range []string{
		".punt-labs/ethos/**/*.lock",
		".punt-labs/ethos/locks/**",
		".punt-labs/ethos/tmp/**",
		".punt-labs/ethos/receipts/**",
		".punt-labs/ethos/missions/*/log.jsonl",
		".punt-labs/ethos/missions/*/*.log.jsonl",
		".punt-labs/ethos/missions/*/artifacts/**",
		".punt-labs/ethos/missions/*/reflection-intent.yaml",
		".punt-labs/ethos/missions/*/correction-intent.yaml",
		".punt-labs/ethos/missions/*/log-*.quarantine",
		".punt-labs/ethos/missions/*/log-*.jsonl.corrupt*",
		".punt-labs/ethos/sessions/*/audit.jsonl",
		".punt-labs/ethos/sessions/*.audit.jsonl",
		".punt-labs/ethos/sessions/*/audit-*.quarantine",
		".punt-labs/ethos/sessions/*/audit-*.jsonl.corrupt*",
		".punt-labs/ethos/**/quarantine/**",
	} {
		if Match(pattern, rel) {
			return true
		}
	}
	return false
}

// TrackedLocal reports whether rel, a path relative to the repository root,
// is machine-local data that must not be tracked: anything under LocalPrefix,
// any class-4 path, or a misplaced local file in the record tree.
func TrackedLocal(rel string) bool {
	if rel == "" {
		return false
	}
	class, _, ok := Classify(rel)
	return strings.HasPrefix(rel, LocalPrefix) || ok && class == Never || MisplacedLocal(rel)
}

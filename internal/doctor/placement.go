package doctor

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// misplacedLocal distinguishes physical placement from both classification and
// Git tracking. Published/full/exported trace is legitimate in the source tree;
// live streams, scratch, overlays, locks and staging never are.
func misplacedLocal(rel string) bool {
	const source = ".punt-labs/ethos/"
	if !strings.HasPrefix(rel, source) {
		return false
	}
	if class, _, ok := layout.Classify(rel); ok && class == layout.Never {
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
		if layout.Match(pattern, rel) {
			return true
		}
	}
	return false
}

// CheckPhysicalPlacement detects recognized local files physically under the
// source tree, even when the classifier recognizes them and Git ignores them.
// The root is explicit: RunAll checks C and, when different, shared S separately.
// Symlinks and nested working trees are reported, never traversed.
func CheckPhysicalPlacement(root string) Result {
	const name = "Physical storage placement"
	if root == "" {
		return Result{Name: name, Status: "PASS", Detail: "not scanned: not in a repository"}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}
	s := newScan()
	for _, zone := range unclassifiedZones {
		if _, err := s.collectUnclassified(layout.Table, root, zone); err != nil {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("scanning physical placement in %s: %v", root, err)}
		}
	}
	var parts []string
	if paths := sortedKeys(s.misplaced); len(paths) > 0 {
		parts = append(parts, "recognized legacy data found at "+strings.Join(paths, ", ")+" (agent-owned artifacts are not legacy)")
	}
	if skipped := sortedKeys(s.skipped); len(skipped) > 0 {
		parts = append(parts, "not scanned: "+strings.Join(skipped, ", "))
	}
	if len(parts) > 0 {
		return Result{Name: name, Status: "FAIL", Detail: root + ": " + strings.Join(parts, "; ")}
	}
	return Result{Name: name, Status: "PASS", Detail: root + ": no machine-local files under source tree"}
}

package doctor

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

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
		parts = append(parts, "machine-local files physically under source tree: "+strings.Join(paths, ", ")+" — quiesce old writers; copy/verify recognized legacy data into the shared .punt-labs/local/ethos/ before removing old paths; inspect agent-owned artifacts manually; do not add source-tree ignore exceptions")
	}
	if skipped := sortedKeys(s.skipped); len(skipped) > 0 {
		parts = append(parts, "not scanned: "+strings.Join(skipped, ", "))
	}
	if len(parts) > 0 {
		return Result{Name: name, Status: "FAIL", Detail: root + ": " + strings.Join(parts, "; ")}
	}
	return Result{Name: name, Status: "PASS", Detail: root + ": no machine-local files under source tree"}
}

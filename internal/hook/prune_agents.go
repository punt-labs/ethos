package hook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/punt-labs/ethos/v4/internal/agentmark"
)

// PruneOrphanedGeneratedAgents deletes every ethos-generated agent file
// under <checkoutRoot>/.claude/agents/ whose handle is not in keep. A file
// is deleted only when it carries the agentmark provenance marker — an
// unmarked file is never ethos's to delete, whatever its handle, and is
// left alone. Returns the handles deleted, sorted for a stable log, and
// joins any read or remove error encountered along the way; one file's
// error never stops the rest of the sweep. A missing agents directory is
// not an error: there is nothing to prune.
func PruneOrphanedGeneratedAgents(checkoutRoot string, keep map[string]bool) (deleted []string, err error) {
	// An empty checkoutRoot would make filepath.Join collapse to the relative
	// ".claude/agents", globbing and deleting against the process CWD instead
	// of a real checkout. Refuse it: the caller gates on a resolved checkout,
	// and this is the defense-in-depth backstop for that contract.
	if checkoutRoot == "" {
		return nil, errors.New("prune agents: empty checkout root")
	}
	dir := filepath.Join(checkoutRoot, ".claude", "agents")
	matches, globErr := filepath.Glob(filepath.Join(dir, "*.md"))
	if globErr != nil {
		return nil, fmt.Errorf("listing agent files in %s: %w", dir, globErr)
	}
	var errs []error
	for _, path := range matches {
		handle := fileHandle(path)
		if keep[handle] {
			continue
		}
		// Never delete through a symlink: a checkout whose .claude/agents (or a
		// file under it) links to a shared or another checkout's tree must not
		// have that target removed. CheckAgentPath is the same symlink-refusing
		// guard the generator and installer use, so prune honors the same zone
		// boundary. A symlinked path is skipped, not deleted.
		if symErr := CheckAgentPath(dir, path); symErr != nil {
			errs = append(errs, symErr)
			continue
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			if !errors.Is(readErr, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("reading agent file %s: %w", path, readErr))
			}
			continue
		}
		if !agentmark.Has(string(content)) {
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil {
			errs = append(errs, fmt.Errorf("removing agent file %s: %w", path, removeErr))
			continue
		}
		deleted = append(deleted, handle)
	}
	sort.Strings(deleted)
	return deleted, errors.Join(errs...)
}

// fileHandle returns path's base name with its .md extension stripped.
func fileHandle(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(filepath.Ext(base))]
}

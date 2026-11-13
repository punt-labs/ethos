package seed

import (
	"fmt"
	"io/fs"
	"strings"
)

// ChecklistAgentNames returns the handles of the seeded review-checklist
// agents (code-reviewer, silent-failure-hunter,
// invariant-completeness-reviewer), read directly from the embedded
// sidecar/agents/ so the set can never drift from what `ethos seed`
// deploys. They carry no team membership by design (no persona, no
// identity) — callers that classify agent files by team ownership exempt
// these handles explicitly rather than guessing from provenance.
//
// fsys and root are parameterized (rather than reading Agents directly) so
// a test can inject a broken fs.FS and exercise the error path without
// depending on an unreadable compile-time embed.
func ChecklistAgentNames(fsys fs.FS, root string) (map[string]bool, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("reading embedded %s: %w", root, err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		names[strings.TrimSuffix(e.Name(), ".md")] = true
	}
	return names, nil
}

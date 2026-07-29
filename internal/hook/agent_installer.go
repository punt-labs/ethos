package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/resolve"
)

// InstallAgentDefinitions copies agent .md files from the ethos agents
// directory to .claude/agents/. Only copies files that are missing or
// have different content. Returns the list of deployed filenames.
//
// generated is the set of handles the DES-026 generator owns (see
// GeneratedAgentHandles). A stub for such a handle is NOT copied: the
// generator writes those files from identity data, so copying a legacy
// stub over them would double-write and leave a sticky stub the
// generator has to overwrite every session — the copy-from-agents path
// DES-026 rejected and the dirty-tree risk DES-013 flags. A nil set
// skips nothing, preserving the copy-everything behavior.
func InstallAgentDefinitions(ethosRoot string, generated map[string]bool) ([]string, error) {
	srcDir := filepath.Join(ethosRoot, "agents")
	entries, err := os.ReadDir(srcDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading agents dir %s: %w", srcDir, err)
	}

	repoRoot := resolve.FindRepoRoot()
	if repoRoot == "" {
		return nil, fmt.Errorf("no git repo found for agent installation")
	}
	destDir := filepath.Join(repoRoot, ".claude", "agents")

	var deployed []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}

		// Subordinate the stub copy to the generator: never copy a stub
		// for a handle the generator owns.
		if generated[strings.TrimSuffix(e.Name(), ".md")] {
			continue
		}

		srcPath := filepath.Join(srcDir, e.Name())
		srcData, err := os.ReadFile(srcPath)
		if err != nil {
			return deployed, fmt.Errorf("reading agent file %s: %w", srcPath, err)
		}

		destPath := filepath.Join(destDir, e.Name())
		destData, err := os.ReadFile(destPath)
		if err == nil && string(destData) == string(srcData) {
			continue // identical, skip
		}

		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return deployed, fmt.Errorf("creating agents dir %s: %w", destDir, err)
		}

		if err := os.WriteFile(destPath, srcData, 0o644); err != nil {
			return deployed, fmt.Errorf("writing agent file %s: %w", destPath, err)
		}
		deployed = append(deployed, e.Name())
	}

	sort.Strings(deployed)
	return deployed, nil
}

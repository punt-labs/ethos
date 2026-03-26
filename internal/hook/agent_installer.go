package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/punt-labs/ethos/internal/resolve"
)

// InstallAgentDefinitions copies agent .md files from the ethos agents
// directory to .claude/agents/. Only copies files that are missing or
// have different content. Returns the list of deployed filenames.
func InstallAgentDefinitions(ethosRoot string) ([]string, error) {
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

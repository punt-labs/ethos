package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/agentmark"
	"github.com/punt-labs/ethos/v4/internal/resolve"
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
// DES-026 rejected and the dirty-tree risk DES-013 flags.
//
// A nil set skips nothing and copies everything. That is correct only
// when the caller KNOWS the generator owns no handle. Never pass nil
// because the ownership lookup failed — that silently reinstates the
// pre-DES-026 clobber. Callers fail closed instead and install nothing;
// see installStubAgentDefs.
func InstallAgentDefinitions(ethosRoot string, generated map[string]bool) ([]string, error) {
	files, err := PlanInstalledAgentDefinitions(ethosRoot, generated)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	loc := resolve.Roots()
	if loc.CheckoutErr != nil {
		return nil, fmt.Errorf("locating the checkout for agent installation: %w", loc.CheckoutErr)
	}
	if err := loc.StoreFailure(); err != nil {
		return nil, fmt.Errorf("locating the store for agent staging: %w", err)
	}
	if loc.Checkout == "" || loc.Store == "" {
		return nil, fmt.Errorf("no git repo found for agent installation")
	}
	var deployed []string
	for _, file := range files {
		path := filepath.Join(loc.Checkout, ".claude", "agents", filepath.Base(file.Handle)+".md")
		changed, err := writeAgentFile(loc.Store, loc.Checkout, path, file.Content)
		if err != nil {
			return deployed, err
		}
		if changed {
			deployed = append(deployed, file.Handle+".md")
		}
	}
	return deployed, nil
}

// PlanInstalledAgentDefinitions returns the exact source-copy bytes, including
// the shared provenance marker, without resolving a checkout or writing files.
func PlanInstalledAgentDefinitions(ethosRoot string, generated map[string]bool) ([]AgentFile, error) {
	srcDir := filepath.Join(ethosRoot, "agents")
	if err := CheckAgentPath(ethosRoot, srcDir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(srcDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading agents dir %s: %w", srcDir, err)
	}

	var files []AgentFile
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
		if err := CheckAgentPath(ethosRoot, srcPath); err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(srcPath)
		if err != nil {
			return nil, fmt.Errorf("reading agent file %s: %w", srcPath, err)
		}
		// The marker lets doctor tell this copy from a hand-authored file
		// if its identity is later removed.
		files = append(files, AgentFile{Handle: strings.TrimSuffix(e.Name(), ".md"), Content: agentmark.Add(string(raw))})
	}

	return files, nil
}

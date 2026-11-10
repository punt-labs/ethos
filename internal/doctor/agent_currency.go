package doctor

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/bundle"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/seed"
	"github.com/punt-labs/ethos/v4/internal/team"
)

// CheckAgentCurrency compares actual checkout bytes to the same plan generation
// and installation use. Seeded checklists remain operator-owned: differences
// warn, rather than inviting an overwrite of a deliberate edit.
func CheckAgentCurrency(checkout, storeRoot string, teams *team.LayeredStore, s identity.IdentityStore) Result {
	const name = "Agent file currency"
	if checkout == "" || storeRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}
	cfg, err := resolve.ConfigOf(storeRoot)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}
	return CheckAgentCurrencyFromConfig(checkout, storeRoot, cfg, teams, s)
}

// CheckAgentCurrencyFromConfig does not reread configuration or locate roots.
func CheckAgentCurrencyFromConfig(checkout, storeRoot string, cfg *resolve.RepoConfig, teams *team.LayeredStore, s identity.IdentityStore) Result {
	const name = "Agent file currency"
	if checkout == "" || storeRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}
	fail := func(err error) Result { return Result{Name: name, Status: "FAIL", Detail: err.Error()} }
	if s == nil {
		return fail(fmt.Errorf("no identity store available for agent rendering"))
	}
	view := references(s)
	mode := layout.RecordBlame
	if cfg != nil {
		var err error
		mode, err = cfg.RecordingMode()
		if err != nil {
			return fail(err)
		}
	}
	p, err := layout.NewRepository(storeRoot, checkout, mode)
	if err != nil {
		return fail(err)
	}
	repo, active, global, authoritative := s.Root(), "", "", false
	if ls, ok := s.(*identity.LayeredStore); ok {
		repo, active, global, authoritative = ls.RepoRoot(), ls.BundleRoot(), ls.GlobalRoot(), ls.RepoAuthoritative()
	}
	roles := role.NewLayeredStoreWithBundle(repo, active, global, authoritative)
	// Team and role stores are supplied separately but use these same layers.
	// Check their flat source directories before the planner follows any path.
	for _, root := range view.roots {
		for _, dir := range []string{"teams", "roles"} {
			if err := checkDiagnosticDirectory(root, dir); err != nil {
				return fail(err)
			}
		}
	}
	var skills []string
	b, err := bundle.ResolveActiveFromConfig(storeRoot, global, cfg)
	if err != nil {
		return fail(err)
	}
	if b != nil {
		skills = b.Manifest.DefaultSkills
	}
	plan, err := hook.PlanGeneratedAgentFilesFromConfig(cfg, hook.AgentProjectPatterns(checkout), skills, view, teams, roles)
	if err != nil {
		return fail(fmt.Errorf("could not render expected agents: %w", err))
	}
	if len(plan.Problems) > 0 {
		return fail(fmt.Errorf("could not render expected agents: %s", strings.Join(plan.Problems, "; ")))
	}
	installed, err := hook.PlanInstalledAgentDefinitions(p.RecordRoot(), plan.Owned)
	if err != nil {
		return fail(err)
	}
	var drift, warnings []string
	compare := func(file hook.AgentFile, seeded bool) error {
		path := filepath.Join(checkout, ".claude", "agents", filepath.Base(file.Handle)+".md")
		if err := hook.CheckAgentPath(checkout, path); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if seeded && os.IsNotExist(err) {
			return nil // seeding is opt-in; no file to diagnose
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		if err != nil || string(data) != file.Content {
			if seeded {
				warnings = append(warnings, file.Handle+".md")
			} else {
				drift = append(drift, file.Handle+".md")
			}
		}
		return nil
	}
	owned := make(map[string]bool)
	for _, file := range append(plan.Files, installed...) {
		owned[file.Handle] = true
		if err := compare(file, false); err != nil {
			return fail(err)
		}
	}
	entries, err := fs.ReadDir(seed.Agents, "sidecar/agents")
	if err != nil {
		return fail(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") || entry.Name() == "README.md" {
			continue
		}
		handle := strings.TrimSuffix(entry.Name(), ".md")
		if owned[handle] {
			continue // generator/installer ownership takes precedence
		}
		data, err := fs.ReadFile(seed.Agents, "sidecar/agents/"+entry.Name())
		if err != nil {
			return fail(err)
		}
		if err := compare(hook.AgentFile{Handle: handle, Content: string(data)}, true); err != nil {
			return fail(err)
		}
	}
	sort.Strings(drift)
	sort.Strings(warnings)
	status, detail := "PASS", "generated and installed agent bytes match their sources"
	if len(drift) > 0 {
		status, detail = "FAIL", "generated/installed agent drift (missing or different): "+strings.Join(drift, ", ")+" — regenerate from the current sources"
	}
	if len(warnings) > 0 {
		if status == "PASS" {
			status, detail = "WARN", ""
		} else {
			detail += "; "
		}
		detail += "seeded agents differ from this binary (local edits or older content): " + strings.Join(warnings, ", ") + ""
	}
	return Result{Name: name, Status: status, Detail: detail}
}

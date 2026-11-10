package autorepair

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/adr"
)

const stepRepoADRs = "repo-adrs"

func init() {
	Register(Step{Name: stepRepoADRs, Scope: ScopeBoth, Run: runRepoADRs})
}

var adrIDPattern = regexp.MustCompile(`^DES-\d{3}$`)

// runRepoADRs copies each global ADR that this repository's missions link
// into the repository store. The global copy stays: other repositories may
// link it too. An ADR with a divergent repository copy is a residual.
func runRepoADRs(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	global := adr.NewStore(filepath.Join(env.Global, "adrs"))
	ids, err := global.List()
	if err != nil {
		return out, fmt.Errorf("listing global ADRs: %w", err)
	}
	if len(ids) == 0 {
		return out, nil
	}
	missions, linked := missionADRLinks(env.Roots.Store)
	repo, err := adr.NewRepositoryStore(env.Policy, filepath.Join(env.Global, "adrs"))
	if err != nil {
		return out, fmt.Errorf("opening repository ADR store: %w", err)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			out.Deferred = true
			return out, nil
		}
		a, err := global.Load(id)
		if err != nil {
			out.Residuals = append(out.Residuals, Residual{Step: stepRepoADRs, ID: id, Reason: "global ADR unreadable: " + err.Error()})
			continue
		}
		if !linked[id] && !missions[a.MissionID] {
			continue
		}
		res, err := repo.Migrate([]string{id}, true)
		if err != nil {
			env.Log.Logf(stepRepoADRs, "copy-failed", []string{id}, err)
			out.Residuals = append(out.Residuals, Residual{Step: stepRepoADRs, ID: id, Reason: err.Error()})
			continue
		}
		if len(res) == 1 && res[0].Action == "copied" {
			out.Items++
			env.Log.Logf(stepRepoADRs, "copy", []string{filepath.Join(env.Global, "adrs", id+".yaml"), filepath.Join(repo.Dir(), id+".yaml")}, nil)
		}
	}
	return out, nil
}

// missionADRLinks reads every repository contract. It returns the set of
// mission IDs and the set of DES ids named in contract inputs.references.
func missionADRLinks(store string) (missions, linked map[string]bool) {
	missions, linked = map[string]bool{}, map[string]bool{}
	dir := filepath.Join(store, ".punt-labs", "ethos", "missions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "contract.yaml"))
		if err != nil {
			continue
		}
		var c struct {
			MissionID string `yaml:"mission_id"`
			Inputs    struct {
				References []string `yaml:"references"`
			} `yaml:"inputs"`
		}
		if yaml.Unmarshal(b, &c) != nil {
			continue
		}
		missions[c.MissionID] = true
		for _, r := range c.Inputs.References {
			if adrIDPattern.MatchString(r) {
				linked[r] = true
			}
		}
	}
	return
}

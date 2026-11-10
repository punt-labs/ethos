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

// adrIDPattern finds an ADR id anywhere in a reference entry: the bare id,
// a path to its page (docs/DES-084.md) or the id with a title.
var adrIDPattern = regexp.MustCompile(`\bDES-\d{3}\b`)

// runRepoADRs copies each global ADR that this repository's missions link
// into the repository store. The global copy stays: other repositories may
// link it too. An ADR with a divergent repository copy is a residual. The
// step prints nothing; every action, notice and failure goes to env.Log.
func runRepoADRs(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	globalDir := filepath.Join(env.Global, "adrs")
	global := adr.NewStore(globalDir)
	ids, err := global.List()
	if err != nil {
		return out, fmt.Errorf("listing global ADRs: %w", err)
	}
	if len(ids) == 0 {
		return out, nil
	}
	missions, linked := missionADRLinks(env, env.Roots.Store)
	repo, err := adr.NewRepositoryStore(env.Policy, globalDir)
	if err != nil {
		return out, fmt.Errorf("opening repository ADR store: %w", err)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			out.Deferred = true
			return out, nil
		}
		src := filepath.Join(globalDir, id+".yaml")
		a, err := global.Load(id)
		if err != nil {
			env.Log.Logf(stepRepoADRs, "unreadable", []string{src}, err)
			out.Residuals = append(out.Residuals, Residual{Step: stepRepoADRs, ID: id, Reason: "global ADR unreadable: " + err.Error()})
			continue
		}
		if !linked[id] && !(a.MissionID != "" && missions[a.MissionID]) {
			continue
		}
		res, err := repo.MigrateQuiet([]string{id})
		if err != nil {
			env.Log.Logf(stepRepoADRs, "copy-failed", []string{src}, err)
			out.Residuals = append(out.Residuals, Residual{Step: stepRepoADRs, ID: id, Reason: err.Error()})
			continue
		}
		if len(res) == 1 && res[0].Action == "copied" {
			out.Items++
			dst := filepath.Join(repo.Dir(), id+".yaml")
			env.Log.Logf(stepRepoADRs, "copy", []string{src, dst}, nil)
			if res[0].Notice != "" {
				env.Log.Logf(stepRepoADRs, "redacted", []string{dst}, nil)
			}
		}
	}
	return out, nil
}

// missionADRLinks reads every repository contract. It returns the set of
// mission IDs and the set of DES ids named anywhere in contract
// inputs.references. A contract with no mission_id adds nothing to missions,
// so the empty id never matches an ADR without one. A contract that cannot
// be read or parsed is logged and skipped.
func missionADRLinks(env Env, store string) (missions, linked map[string]bool) {
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
		path := filepath.Join(dir, e.Name(), "contract.yaml")
		b, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				env.Log.Logf(stepRepoADRs, "contract-unreadable", []string{path}, err)
			}
			continue
		}
		var c struct {
			MissionID string `yaml:"mission_id"`
			Inputs    struct {
				References []string `yaml:"references"`
			} `yaml:"inputs"`
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			env.Log.Logf(stepRepoADRs, "contract-unreadable", []string{path}, err)
			continue
		}
		if c.MissionID != "" {
			missions[c.MissionID] = true
		}
		for _, r := range c.Inputs.References {
			for _, id := range adrIDPattern.FindAllString(r, -1) {
				linked[id] = true
			}
		}
	}
	return
}

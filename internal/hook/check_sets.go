package hook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/bundle"
	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/team"
)

func configuredStageChecks(checkout, agent, stage string) ([]checkset.Check, bool, error) {
	store, err := resolve.StoreOf(checkout)
	if err != nil {
		return nil, false, err
	}
	if store == "" {
		return nil, false, nil
	}
	cfg, err := resolve.ConfigOf(store)
	if err != nil {
		return nil, false, err
	}
	repo := filepath.Join(store, ".punt-labs", "ethos")
	ref, projectRepo, err := resolve.CheckPolicyOf(resolve.Located{Store: store, Checkout: checkout, Ethos: repo, Config: cfg})
	if err != nil {
		return nil, false, err
	}
	if cfg == nil && agent == "" && ref == "" {
		return nil, false, nil
	}
	if cfg == nil {
		cfg = &resolve.RepoConfig{}
	}
	policy, err := resolve.ResolutionFromConfig(cfg)
	if err != nil {
		return nil, false, err
	}
	global, err := layout.MachineRoot()
	if err != nil {
		return nil, false, err
	}
	bundleRoot, active, err := bundle.ResolveRootFromConfig(store, global, cfg)
	if err != nil {
		return nil, false, err
	}
	repoOnly := policy == resolve.ResolutionRepoOnly
	if repoOnly {
		storeRoot := ""
		if info, err := os.Stat(repo); err == nil {
			if info.IsDir() {
				storeRoot = repo
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, false, err
		}
		if err := bundle.VerifyRepoOnly(storeRoot, bundleRoot, active); err != nil {
			return nil, false, err
		}
	}
	sets := checkset.NewLayeredStoreWithBundle(repo, bundleRoot, global, repoOnly)
	project := true
	if agent != "" && attribute.ValidateSlug(agent) == nil {
		var bundleIDs *identity.Store
		if bundleRoot != "" {
			bundleIDs = identity.NewStore(bundleRoot)
		}
		ids := identity.NewLayeredStoreWithBundle(identity.NewStore(repo), bundleIDs, identity.NewStore(global), repoOnly)
		id, err := ids.Load(agent, identity.Reference(true), identity.WithoutMigration())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("identity %q: %w", agent, err)
		}
		if err == nil {
			if id.Checks != "" {
				ref = id.Checks
				project = false
			} else if cfg.Team != "" && agent != cfg.Agent && id.Kind == "agent" {
				teams := team.NewLayeredStoreWithBundle(repo, bundleRoot, global, repoOnly)
				selected, err := teams.Load(cfg.Team)
				if err != nil {
					return nil, false, err
				}
				roles := role.NewLayeredStoreWithBundle(repo, bundleRoot, global, repoOnly)
				var bound *role.Role
				for _, member := range selected.Members {
					if member.Identity != agent {
						continue
					}
					r, err := roles.Load(member.Role)
					if err != nil {
						return nil, false, err
					}
					if len(normalizedAgentTools(r.Tools)) > 0 {
						bound = r
					}
				}
				if bound != nil && bound.Checks != "" {
					ref = bound.Checks
					project = false
				}
			}
		}
	}
	if ref == "none" {
		return nil, true, nil
	}
	if ref == "" {
		return nil, false, nil
	}
	if err := checkset.ValidateReference(ref); err != nil {
		return nil, true, err
	}
	if project {
		sets = checkset.NewLayeredStoreWithBundle(projectRepo, bundleRoot, global, repoOnly)
	}
	load := sets.Load
	if stage == "handoff" {
		load = sets.LoadForHandoff
	}
	set, err := load(ref)
	if err != nil {
		return nil, true, err
	}
	var checks []checkset.Check
	for _, check := range set.Checks {
		if check.Stage == stage {
			checks = append(checks, check)
		}
	}
	return checks, true, nil
}

func configuredEditChecks(checkout, root, file, agent string) ([]editCheck, bool, error) {
	configured, selected, err := configuredStageChecks(root, agent, "edit")
	if err != nil || !selected {
		return nil, selected, err
	}
	rel, err := filepath.Rel(root, file)
	if err != nil {
		return nil, true, err
	}
	dir, err := filepath.Rel(root, filepath.Dir(file))
	if err != nil {
		return nil, true, err
	}
	packageDir := "./"
	if dir != "." {
		packageDir += filepath.ToSlash(dir)
	}
	var checks []editCheck
	for _, check := range configured {
		if check.Stage != "edit" || !check.Matches(filepath.ToSlash(rel)) {
			continue
		}
		argv, err := check.Argv()
		if err != nil {
			return nil, true, err
		}
		values := map[string]string{"{file}": file, "{files}": file, "{root}": root, "{package_dir}": packageDir}
		for i, arg := range argv {
			if value, ok := values[arg]; ok {
				argv[i] = value
			}
		}
		cwd := root
		if check.Cwd == "file_dir" || check.Cwd == "package_dir" {
			cwd = filepath.Dir(file)
		}
		checks = append(checks, editCheck{name: check.Name, argv: argv, failOnOutput: check.FailOnOutput, onFail: check.OnFail, timeout: check.Duration(), cwd: cwd, policy: &check})
	}
	return checks, true, nil
}

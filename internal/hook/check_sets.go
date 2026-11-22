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
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/team"
)

func configuredEditChecks(checkout, root, file, agent string) ([]editCheck, bool, error) {
	cfg, err := resolve.ConfigOf(checkout)
	if err != nil {
		return nil, false, err
	}
	if cfg == nil && agent == "" {
		return nil, false, nil
	}
	if cfg == nil {
		cfg = &resolve.RepoConfig{}
	}
	policy, err := resolve.ResolutionFromConfig(cfg)
	if err != nil {
		return nil, false, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false, err
	}
	global := filepath.Join(home, ".punt-labs", "ethos")
	bundleRoot, _, err := bundle.ResolveRootFromConfig(checkout, global, cfg)
	if err != nil {
		return nil, false, err
	}
	repo := filepath.Join(checkout, ".punt-labs", "ethos")
	repoOnly := policy == resolve.ResolutionRepoOnly
	sets := checkset.NewLayeredStoreWithBundle(repo, bundleRoot, global, repoOnly)
	ref := cfg.Checks
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
			} else if cfg.Team != "" {
				teams := team.NewLayeredStoreWithBundle(repo, bundleRoot, global, repoOnly)
				selected, err := teams.Load(cfg.Team)
				if err != nil {
					return nil, false, err
				}
				roleName := ""
				// Generation overwrites a handle on each binding: the last wins.
				for _, member := range selected.Members {
					if member.Identity == agent {
						roleName = member.Role
					}
				}
				if roleName != "" {
					r, err := role.NewLayeredStoreWithBundle(repo, bundleRoot, global, repoOnly).Load(roleName)
					if err != nil {
						return nil, false, err
					}
					if r.Checks != "" {
						ref = r.Checks
					}
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
	set, err := sets.Load(ref)
	if err != nil {
		return nil, true, err
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
	for _, check := range set.Checks {
		if check.Stage != "edit" || !check.Matches(filepath.ToSlash(rel)) {
			continue
		}
		argv := append([]string(nil), check.Run...)
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
		checks = append(checks, editCheck{name: check.Name, argv: argv, failOnOutput: check.FailOnOutput, onFail: check.OnFail, timeout: check.Duration(), cwd: cwd})
	}
	return checks, true, nil
}

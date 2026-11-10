package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

func init() { Register(Step{Name: "bead-alias", Scope: ScopeRepository, Run: runBeadAlias}) }

// runBeadAlias rewrites every repository contract that uses the deprecated
// inputs.bead key to inputs.ticket (DES-049, removed in v5.0.0). Items counts
// contracts rewritten. A contract that sets both keys, or cannot be parsed, is
// left as it is and recorded as a residual.
func runBeadAlias(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	ids, err := repositoryMissionIDs(env)
	if err != nil {
		return out, fmt.Errorf("listing missions: %w", err)
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			out.Deferred = true
			return out, nil
		}
		path := repositoryContractPath(env, id)
		data, err := os.ReadFile(path)
		if err != nil {
			env.Log.Logf("bead-alias", "read", []string{path}, err)
			out.Deferred = true
			continue
		}
		if mission.BeadAlias(data) == "" {
			continue
		}
		err = mission.WithRecordLockTry(env.Policy, id, func() error {
			cur, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			renamed, changed, err := mission.RenameBeadAlias(cur)
			if err != nil || !changed {
				return err
			}
			return mission.ReplaceRecordFile(env.Policy, path, renamed)
		})
		switch {
		case errors.Is(err, mission.ErrLockBusy):
			env.Log.Logf("bead-alias", "defer-busy", []string{path}, nil)
			out.Deferred = true
		case err != nil:
			env.Log.Logf("bead-alias", "refuse", []string{path}, err)
			out.Residuals = append(out.Residuals, Residual{Step: "bead-alias", ID: id, Reason: "contract uses inputs.bead and cannot be rewritten: " + err.Error()})
		default:
			env.Log.Logf("bead-alias", "rename-inputs-bead", []string{path}, nil)
			out.Items++
		}
	}
	return out, nil
}

// repositoryMissionIDs lists the mission directories of the repository store
// that hold a contract, in order.
func repositoryMissionIDs(env Env) ([]string, error) {
	dir := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() || !mission.IsMissionID(e.Name()) {
			continue
		}
		if _, err := os.Stat(repositoryContractPath(env, e.Name())); err == nil {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func repositoryContractPath(env Env, id string) string {
	return filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions", id, "contract.yaml")
}

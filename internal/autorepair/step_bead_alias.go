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
// contracts rewritten. A contract that sets both keys, cannot be read or parsed,
// or cannot be written is left as it is and recorded as a residual. A busy
// mission lock defers that contract to the next run.
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
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			env.Log.Logf("bead-alias", "read", []string{path}, err)
			out.Residuals = append(out.Residuals, Residual{Step: "bead-alias", ID: id, Reason: "contract is unreadable"})
			continue
		}
		if mission.BeadAlias(data) == "" {
			continue
		}
		var durability error
		var stage string
		err = mission.WithRecordLockTry(env.Policy, id, func() error {
			stage = "read"
			cur, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			stage = "rename"
			renamed, changed, err := mission.RenameBeadAlias(cur)
			if err != nil || !changed {
				return err
			}
			stage = "write"
			durability, err = mission.ReplaceRecordFile(env.Policy, path, renamed)
			return err
		})
		switch {
		case errors.Is(err, mission.ErrLockBusy):
			env.Log.Logf("bead-alias", "defer-busy", []string{path}, nil)
			out.Deferred = true
		case err != nil:
			env.Log.Logf("bead-alias", "refuse", []string{path}, err)
			out.Residuals = append(out.Residuals, Residual{Step: "bead-alias", ID: id, Reason: beadAliasReason(stage, err)})
		default:
			if durability != nil {
				env.Log.Logf("bead-alias", "durability-unconfirmed", []string{path}, durability)
			}
			env.Log.Logf("bead-alias", "rename-inputs-bead", []string{path}, nil)
			out.Items++
		}
	}
	return out, nil
}

// beadAliasReason names the condition that left a contract as it was. It is
// derived from the stage and the kind of error, never from error text: a YAML
// error carries line numbers and values from the file.
func beadAliasReason(stage string, err error) string {
	switch stage {
	case "read":
		return "contract is unreadable"
	case "rename":
		return renameReason(err)
	default:
		return "contract uses inputs.bead and the rewritten contract cannot be installed"
	}
}

func renameReason(err error) string {
	const prefix = "contract uses inputs.bead and cannot be rewritten: "
	switch {
	case errors.Is(err, mission.ErrBothBeadKeys):
		return prefix + "it sets both inputs.ticket and inputs.bead"
	case errors.Is(err, mission.ErrContractNotMapping):
		return prefix + "it is not a mapping"
	case errors.Is(err, mission.ErrContractParse):
		return prefix + "it cannot be parsed"
	}
	return prefix + "the rename failed"
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

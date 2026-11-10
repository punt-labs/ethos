package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func init() {
	Register(Step{Name: "config-file", Scope: ScopeRepository, Run: runConfigFile})
}

// syncConfigDirs confirms a rename or removal under .punt-labs: the entry's
// directory and its ancestors up to the store root. A test replaces it.
var syncConfigDirs = layout.SyncRepositoryDestination

// runConfigFile renames .punt-labs/ethos/config.yaml (ethos v0.6 to v2.1) to
// .punt-labs/ethos.yaml. When both exist the new file wins and the old one is
// deleted: the new file is the one every current binary reads.
func runConfigFile(ctx context.Context, env Env) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{Deferred: true}, nil
	}
	store := env.Roots.Store
	oldPath := filepath.Join(store, ".punt-labs", "ethos", "config.yaml")
	newPath := filepath.Join(store, ".punt-labs", "ethos.yaml")

	oldInfo, err := os.Lstat(oldPath)
	if errors.Is(err, os.ErrNotExist) {
		return Outcome{}, nil
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("examining legacy config %s: %w", oldPath, err)
	}
	if !oldInfo.Mode().IsRegular() {
		return Outcome{Residuals: []Residual{{
			Step:   "config-file",
			ID:     oldPath,
			Reason: "legacy config is not a regular file; left in place",
		}}}, nil
	}

	newInfo, err := os.Lstat(newPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Rename(oldPath, newPath); err != nil {
			env.Log.Logf("config-file", "rename", []string{oldPath, newPath}, err)
			return Outcome{}, fmt.Errorf("renaming legacy config %s to %s: %w", oldPath, newPath, err)
		}
		env.Log.Logf("config-file", "rename", []string{oldPath, newPath}, nil)
		return syncedOutcome(env, "rename", oldPath, newPath)
	case err != nil:
		return Outcome{}, fmt.Errorf("examining config %s: %w", newPath, err)
	case !newInfo.Mode().IsRegular():
		// A dangling or redirected ethos.yaml is not a config the old file can
		// safely be dropped in favour of.
		return Outcome{Residuals: []Residual{{
			Step:   "config-file",
			ID:     newPath,
			Reason: "ethos.yaml is not a regular file; legacy config kept",
		}}}, nil
	}

	if err := os.Remove(oldPath); err != nil {
		env.Log.Logf("config-file", "delete", []string{oldPath}, err)
		return Outcome{}, fmt.Errorf("deleting legacy config %s: %w", oldPath, err)
	}
	env.Log.Logf("config-file", "delete", []string{oldPath}, nil)
	return syncedOutcome(env, "delete", oldPath)
}

// syncedOutcome reports the applied change and makes it durable. The change has
// happened, so a failed sync is logged and does not fail the step.
func syncedOutcome(env Env, action string, paths ...string) (Outcome, error) {
	for _, p := range paths {
		if err := syncConfigDirs(env.Policy, p); err != nil {
			env.Log.Logf("config-file", action+"-sync", []string{p}, err)
		}
	}
	return Outcome{Items: 1}, nil
}

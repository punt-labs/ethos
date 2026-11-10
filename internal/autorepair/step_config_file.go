package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
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
	if !env.InRepository() {
		return Outcome{}, nil
	}
	store := repoStore(env)
	oldPath := filepath.Join(store, ".punt-labs", "ethos", "config.yaml")
	newPath := filepath.Join(store, ".punt-labs", "ethos.yaml")
	oldRel := filepath.ToSlash(filepath.Join(".punt-labs", "ethos", "config.yaml"))
	newRel := ".punt-labs/ethos.yaml"
	residual := func(id, reason string) (Outcome, error) {
		env.Log.Logf("config-file", "residual", []string{oldPath, newPath}, errors.New(reason))
		return Outcome{Residuals: []Residual{{Step: "config-file", ID: id, Reason: reason}}}, nil
	}

	link, err := symlinkUnder(store, oldPath)
	if err != nil {
		return Outcome{}, fmt.Errorf("examining legacy config %s: %w", oldPath, err)
	}
	if link != "" {
		return residual(link, "symlink at "+link+"; legacy config left in place")
	}
	oldInfo, err := os.Lstat(oldPath)
	if errors.Is(err, os.ErrNotExist) {
		return Outcome{}, nil
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("examining legacy config %s: %w", oldPath, err)
	}
	if !oldInfo.Mode().IsRegular() {
		return residual(oldRel, "legacy config is not a regular file; left in place")
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
		return residual(newRel, "ethos.yaml is not a regular file; legacy config kept")
	}

	// The legacy file may be the only readable copy: delete it only after the
	// kept one loads.
	if _, err := resolve.ConfigOf(store); err != nil {
		return residual(newRel, "ethos.yaml does not parse; legacy config kept")
	}
	if err := os.Remove(oldPath); err != nil {
		env.Log.Logf("config-file", "delete", []string{oldPath}, err)
		return Outcome{}, fmt.Errorf("deleting legacy config %s: %w", oldPath, err)
	}
	env.Log.Logf("config-file", "delete", []string{oldPath}, nil)
	return syncedOutcome(env, "delete", oldPath)
}

// symlinkUnder returns the root-relative path of the first symlink on the way
// from root (exclusive) down to path, or "" when there is none or the walk
// reaches a missing entry. A symlink is never followed: the step would act on
// its target.
func symlinkUnder(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	cur := root
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, name)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			r, err := filepath.Rel(root, cur)
			if err != nil {
				return "", fmt.Errorf("relating %s to %s: %w", cur, root, err)
			}
			return filepath.ToSlash(r), nil
		}
	}
	return "", nil
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

// repoStore returns the store root the policy was built from ("" outside a
// repository). It derives the root from the policy, which is not subject to a
// located error, so a step never reads a Located part unchecked.
func repoStore(env Env) string {
	root := env.Policy.RecordRoot()
	if root == "" {
		return ""
	}
	return filepath.Dir(filepath.Dir(root))
}

// storeRel makes every occurrence of the root base in s relative to it, so a
// residual recorded in the layout stamp names a store-relative path only.
func storeRel(base, s string) string {
	b := filepath.ToSlash(base)
	s = strings.ReplaceAll(filepath.ToSlash(s), b+"/", "")
	return strings.ReplaceAll(s, b, ".")
}

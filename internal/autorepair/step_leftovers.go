package autorepair

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/repair"
)

const stepLeftovers = "leftovers"

// staleAfter is how old a staging file or directory must be before step 15
// treats it as abandoned rather than in flight.
const staleAfter = time.Hour

func init() { Register(Step{Name: stepLeftovers, Scope: ScopeBoth, Run: runLeftovers}) }

// runLeftovers deletes old lock files, migration staging directories and
// temporary-file leftovers from the record trees and the global store. The
// repository's current locks under S/.punt-labs/local and the global lock
// directory ~/.punt-labs/ethos/locks belong to running code and are never
// touched. A lock another process holds is skipped and the step defers.
func runLeftovers(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	var roots []string
	if env.InRepository() {
		roots = append(roots, env.Policy.RecordRoot())
		if c := env.Policy.CheckoutRecordRoot(); c != "" && c != env.Policy.RecordRoot() {
			roots = append(roots, c)
		}
	}
	if env.Global != "" {
		roots = append(roots, env.Global)
	}
	for _, root := range roots {
		if err := sweepLeftovers(ctx, env, root, &out); err != nil {
			if ctx.Err() != nil {
				out.Deferred = true
				return out, nil
			}
			out.Residuals = append(out.Residuals, Residual{Step: stepLeftovers, ID: root, Reason: err.Error()})
		}
	}
	return out, nil
}

func sweepLeftovers(ctx context.Context, env Env, root string, out *Outcome) error {
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	global := root == env.Global
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return filepath.SkipDir
		}
		name := d.Name()
		if d.IsDir() {
			switch {
			case name == ".git" || name == "artifacts" || name == "delegations" && !global:
				return filepath.SkipDir
			case global && path == filepath.Join(root, "locks"):
				return filepath.SkipDir
			case isMigrateStaging(name):
				removeStale(env, path, true, out)
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(name, ".lock"):
			removeIdleLock(env, path, out)
		case isTempLeftover(name):
			removeStale(env, path, false, out)
		}
		return nil
	})
}

func isMigrateStaging(name string) bool {
	return strings.HasPrefix(name, ".") && strings.Contains(name, ".migrate"+layout.TmpMarker)
}

func isTempLeftover(name string) bool {
	if strings.HasSuffix(name, layout.TmpMarker) {
		return true
	}
	if !strings.HasSuffix(name, layout.LegacyTmpSuffix) {
		return false
	}
	stem := strings.TrimSuffix(name, layout.LegacyTmpSuffix)
	for _, ext := range []string{".yaml", ".yml", ".json", ".jsonl"} {
		if strings.HasSuffix(stem, ext) {
			return true
		}
	}
	return strings.HasPrefix(name, ".")
}

// removeStale deletes a staging file or directory unmodified for staleAfter.
func removeStale(env Env, path string, dir bool, out *Outcome) {
	info, err := os.Lstat(path)
	if err != nil || env.Now.Sub(info.ModTime()) < staleAfter {
		return
	}
	if dir {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	env.Log.Logf(stepLeftovers, "delete-staging", []string{path}, err)
	if err != nil {
		out.Residuals = append(out.Residuals, Residual{Step: stepLeftovers, ID: path, Reason: fmt.Sprintf("cannot delete: %v", err)})
		return
	}
	out.Items++
}

// removeIdleLock deletes the lock file at path if no process holds it.
func removeIdleLock(env Env, path string, out *Outcome) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		env.Log.Logf(stepLeftovers, "open-lock", []string{path}, err)
		out.Residuals = append(out.Residuals, Residual{Step: stepLeftovers, ID: path, Reason: fmt.Sprintf("cannot open lock: %v", err)})
		return
	}
	if err := repair.TryLock(f); err != nil {
		f.Close()
		env.Log.Logf(stepLeftovers, "skip-busy-lock", []string{path}, nil)
		out.Deferred = true
		return
	}
	// Closing releases the lock; Windows cannot delete an open file.
	err = errors.Join(f.Close(), os.Remove(path))
	env.Log.Logf(stepLeftovers, "delete-lock", []string{path}, err)
	if err != nil {
		out.Residuals = append(out.Residuals, Residual{Step: stepLeftovers, ID: path, Reason: fmt.Sprintf("cannot delete: %v", err)})
		return
	}
	out.Items++
}

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
)

const stepLeftovers = "leftovers"

// staleAfter is how old a staging file or directory must be before step 15
// treats it as abandoned rather than in flight.
const staleAfter = time.Hour

func init() { Register(Step{Name: stepLeftovers, Scope: ScopeBoth, Run: runLeftovers}) }

// runLeftovers deletes migration staging directories and temporary-file
// leftovers from the record trees and the global store, and the four obsolete
// lock files older releases created inside the record tree
// (missions.jsonl.lock, missions/.create.lock, missions/<id>/.lock and
// identities/<handle>.yaml.lock), which no current writer opens.
//
// No other lock is ever deleted. A lock a current writer opens by path
// (session, counter, ADR, mission, delegation and dispatch-pending locks in
// the global store; everything under the local lock directory) is live even
// when idle: unlinking it lets a second writer create a second inode and two
// holders share one critical section (internal/identity/persistence.go).
// An obsolete lock that is held is skipped and the step defers.
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
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		switch {
		case strings.HasSuffix(name, ".lock"):
			if !global && isObsoleteRecordLock(filepath.ToSlash(rel)) {
				removeObsoleteLock(env, path, out)
			}
		case isTempLeftover(name):
			removeStale(env, path, false, out)
		}
		return nil
	})
}

// isObsoleteRecordLock reports whether rel (slash-separated, relative to a
// record root) is one of the four lock shapes older releases wrote into the
// record tree. The list is repair.obsolete's; the record tree holds no other
// lock a current writer opens.
func isObsoleteRecordLock(rel string) bool {
	a := strings.Split(rel, "/")
	base := a[len(a)-1]
	switch {
	case rel == "missions.jsonl.lock", rel == "missions/.create.lock":
		return true
	case len(a) == 3 && a[0] == "missions" && base == ".lock":
		return true
	case len(a) == 2 && a[0] == "identities" && strings.HasSuffix(base, ".yaml.lock"):
		return true
	}
	return false
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

// removeObsoleteLock deletes an obsolete record-tree lock if no process holds
// it. A held one is an older writer still running: skip it and defer.
func removeObsoleteLock(env Env, path string, out *Outcome) {
	held, err := lockHeld(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		env.Log.Logf(stepLeftovers, "probe-lock", []string{path}, err)
		out.Residuals = append(out.Residuals, Residual{Step: stepLeftovers, ID: path, Reason: fmt.Sprintf("cannot probe lock: %v", err)})
		return
	}
	if held {
		env.Log.Logf(stepLeftovers, "skip-busy-lock", []string{path}, nil)
		out.Deferred = true
		return
	}
	err = os.Remove(path)
	env.Log.Logf(stepLeftovers, "delete-lock", []string{path}, err)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		out.Residuals = append(out.Residuals, Residual{Step: stepLeftovers, ID: path, Reason: fmt.Sprintf("cannot delete: %v", err)})
		return
	}
	out.Items++
}

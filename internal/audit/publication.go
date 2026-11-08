package audit

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// StagePublication stages an explicit trace selection in a private candidate
// index. Git's actual lock excludes intervening cooperating writers from the
// snapshot through installation. A failed add never changes the actual index;
// no old whole-index snapshot is ever restored over another writer's work.
// A post-install directory sync failure returns layout.InstallDurabilityError:
// the index is applied, not rolled back, and retry must confirm durability again.
// This function is used ONLY for full publication, never local archival.
func StagePublication(p layout.Policy, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := p.LockPath("audit-publication", "validate"); err != nil {
		return err
	}
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	specs := make([]string, 0, len(paths))
	for _, path := range paths {
		rel, err := filepath.Rel(p.CheckoutRecordRoot(), path)
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("publication outside checkout record tree: %s", path)
		}
		rel, err = filepath.Rel(checkout, path)
		if err != nil {
			return err
		}
		specs = append(specs, filepath.ToSlash(rel))
	}
	env, err := gitenv.ActualIndexEnv()
	if err != nil {
		return err
	}
	out, err := gitenv.Run(gitenv.Spec{Git: "git", Dir: checkout, Env: env, Args: []string{"rev-parse", "--git-path", "index"}})
	if err != nil {
		return err
	}
	index, err := gitenv.IndexPathOutput(out)
	if err != nil {
		return fmt.Errorf("reading publication index path: %w", err)
	}
	if !filepath.IsAbs(index) {
		index = filepath.Join(checkout, index)
	}
	lock, err := os.OpenFile(index+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("taking publication index lock: %w", err)
	}
	defer os.Remove(lock.Name())
	if err := lock.Close(); err != nil {
		return err
	}
	data, err := os.ReadFile(index)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	if !missing && len(data) == 0 {
		return fmt.Errorf("audit publication: actual index %s is empty/corrupt; repair it explicitly (actual index unchanged)", index)
	}
	candidate, err := layout.CreateLocalTemp(p, "audit-index", "")
	if err != nil {
		return err
	}
	defer os.Remove(candidate.Name())
	defer os.Remove(candidate.Name() + ".lock")
	if len(data) > 0 {
		if _, err := candidate.Write(data); err != nil {
			candidate.Close()
			return err
		}
	}
	if err := candidate.Close(); err != nil {
		return err
	}
	// An absent index must stay absent for git to initialize it, rather than
	// presenting an invalid empty index file.
	if missing {
		if err := os.Remove(candidate.Name()); err != nil {
			return err
		}
	}
	env = gitenv.WithIndex(env, candidate.Name())
	args := append([]string{"--literal-pathspecs", "add", "--"}, specs...)
	if _, err := gitenv.Run(gitenv.Spec{Git: "git", Dir: checkout, Env: env, Args: args}); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return fmt.Errorf("staging audit publication (actual index unchanged): %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return fmt.Errorf("staging audit publication (actual index unchanged): %w", err)
	}
	f, err := os.OpenFile(candidate.Name(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(candidate.Name(), index); err != nil {
		return fmt.Errorf("installing audit publication index (no cross-device fallback): %w", err)
	}
	// Git's index directory already exists; we create no destination ancestors.
	// Confirm that exact parent while still owning index.lock, even if an
	// alternate index or a linked checkout's shared Git directory is outside S/C.
	if err := syncAuditDirectory(filepath.Dir(index)); err != nil {
		return fmt.Errorf("confirming audit publication index: %w", &layout.InstallDurabilityError{Path: index, Err: err})
	}
	return nil
}

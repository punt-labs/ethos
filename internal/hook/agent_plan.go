package hook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// AgentProjectPatterns exposes the generator's checkout-specific project input.
func AgentProjectPatterns(checkout string) string { return projectFilePatterns(checkout) }

// CheckAgentPath refuses symlinks in a path below its explicit root. Doctor and
// writers use the same rule: an agent source or destination cannot cross zones.
// Missing components are allowed; the eventual read or create decides absence.
func CheckAgentPath(root, path string) error {
	if root == "" {
		return fmt.Errorf("agent root is empty")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("making agent root absolute: %w", err)
	}
	for current := absRoot; ; current = filepath.Dir(current) {
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("agent root ancestor %s is a symlink; not followed", current)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("examining agent root %s: %w", current, err)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("agent path %s is outside root %s", path, root)
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("examining agent path %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("agent path %s is a symlink; not followed", current)
		}
	}
	return nil
}

// writeAgentFile stages at the shared store's local root, never beside the
// source output. A failed rename (including EXDEV) leaves the old file intact.
func writeAgentFile(storeRoot, checkout, path, content string) (bool, error) {
	if err := CheckAgentPath(checkout, path); err != nil {
		return false, err
	}
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == content {
		// A previous rename may have applied without confirming directory
		// durability. Equal bytes avoid replacement, not that confirmation.
		return false, syncAgentFileParents(checkout, path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("reading agent %s: %w", path, err)
	}
	// Agent staging is mode-independent. The public compatibility entry point
	// has explicit store/checkout roots but no recording-policy dependency.
	storeRoot, err = filepath.Abs(storeRoot)
	if err != nil {
		return false, err
	}
	checkout, err = filepath.Abs(checkout)
	if err != nil {
		return false, err
	}
	p, err := layout.NewRepository(storeRoot, checkout, layout.RecordBlame)
	if err != nil {
		return false, err
	}
	return writeAgentFileWithPolicy(p, path, content)
}

func writeAgentFileWithPolicy(p layout.Policy, path, content string) (changed bool, err error) {
	return writeAgentFileWithRename(p, path, content, os.Rename)
}

func writeAgentFileWithRename(p layout.Policy, path, content string, rename func(string, string) error) (changed bool, err error) {
	f, err := layout.CreateLocalTemp(p, "agent", ".md")
	if err != nil {
		return false, err
	}
	defer func() {
		if removeErr := os.Remove(f.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing agent staging file: %w", removeErr))
		}
	}()
	if err := f.Chmod(0o644); err != nil {
		return false, errors.Join(err, f.Close())
	}
	if _, err := f.WriteString(content); err != nil {
		return false, errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return false, errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := rename(f.Name(), path); err != nil {
		return false, fmt.Errorf("installing agent %s: %w", path, err)
	}
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	return true, syncAgentFileParents(checkout, path)
}

func syncAgentFileParents(checkout, path string) error {
	if err := syncAgentParents(checkout, filepath.Dir(path)); err != nil {
		return fmt.Errorf("agent %s installed, but syncing its directory tree failed: %w", path, err)
	}
	return nil
}

// Sync the output directory and its parents through C. When .claude/agents
// was newly created, syncing only that leaf would not persist its ancestors'
// directory entries. Existing ancestors may be synced again without harm.
func syncAgentParents(checkout, dir string) error {
	checkout, err := filepath.Abs(checkout)
	if err != nil {
		return err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := CheckAgentPath(checkout, dir); err != nil {
		return err
	}
	for {
		if err := syncAgentDirectory(dir); err != nil {
			return fmt.Errorf("syncing agent directory %s: %w", dir, err)
		}
		if dir == checkout {
			return nil
		}
		dir = filepath.Dir(dir)
	}
}

func reportAgentPlanProblems(plan AgentPlan) {
	for _, problem := range plan.Problems {
		fmt.Fprintf(os.Stderr, "ethos: generate-agents: skipping %s\n", problem)
	}
}

// ApplyAgentPlan reports skipped members and installs a plan made from a validated
// request snapshot. Roots and configuration are never rediscovered here.
func ApplyAgentPlan(p layout.Policy, plan AgentPlan) error {
	reportAgentPlanProblems(plan)
	if p.CheckoutRecordRoot() == "" || p.LocalRoot() == "" {
		return fmt.Errorf("agent plan requires a repository policy")
	}
	// Policy exposes C's record destination, not C itself. That destination is
	// defined as C/.punt-labs/ethos in both recording modes.
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	for _, file := range plan.Files {
		path := filepath.Join(checkout, ".claude", "agents", filepath.Base(file.Handle)+".md")
		if err := CheckAgentPath(checkout, path); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && string(data) == file.Content {
			if err := syncAgentFileParents(checkout, path); err != nil {
				return err
			}
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := writeAgentFileWithPolicy(p, path, file.Content); err != nil {
			return err
		}
	}
	return nil
}

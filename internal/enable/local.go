package enable

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/claudemd"
	"github.com/punt-labs/ethos/v4/internal/claudesettings"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

const (
	localGuide    = ".punt-labs/local/ethos/CLAUDE.md"
	localImport   = "@" + localGuide
	localHost     = "CLAUDE.local.md"
	localSettings = ".claude/settings.local.json"
)

func EnableLocal(root string, refresh bool) (_ *Report, resultErr error) {
	rep := &Report{RepoRoot: root}
	optedOut, err := resolve.ProjectOptedOut(root)
	if err != nil {
		return rep, err
	}
	if optedOut {
		return rep, fmt.Errorf("ethos is opted out by %s", resolve.ProjectOptOutFile)
	}
	shared, err := resolve.EnabledMarker(root)
	if err != nil {
		return rep, err
	}
	off := filepath.Join(root, resolve.LocalDisabledFile)
	if shared {
		if err := checkExistingLocalTargets(root, resolve.LocalDisabledFile); err != nil {
			return rep, err
		}
		return rep, removeLocalFile(off)
	}
	if err := checkLocalTargets(root, localGuide, localHost, localSettings, resolve.LocalEnabledFile, resolve.LocalDisabledFile); err != nil {
		return rep, err
	}
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	if err != nil {
		return rep, err
	}
	host := filepath.Join(root, localHost)
	if err := claudemd.Preflight(host, localImport); err != nil {
		return rep, err
	}
	if err := claudesettings.PreflightWithEnv(root, PermissionRules, EnvDefaults, claudesettings.Local); err != nil {
		return rep, err
	}
	tx := fileTransaction{p: p}
	defer tx.finish(&resultErr)
	if err := ensurePersonalLockExcludes(root, &tx, false); err != nil {
		return rep, err
	}
	guide := filepath.Join(root, localGuide)
	if err := tx.snapshot(guide); err != nil {
		return rep, err
	}
	old := tx.files[len(tx.files)-1]
	if !old.exists || refresh && !bytes.Equal(old.data, Guide) {
		if err := writeVendored(guide, Guide, p); err != nil {
			return rep, err
		}
	}
	if err := tx.snapshot(host); err != nil {
		return rep, err
	}
	if _, err := claudemd.Register(host, localImport, claudemd.WithRepository(p)); err != nil {
		return rep, err
	}
	if err := tx.snapshot(filepath.Join(root, localSettings)); err != nil {
		return rep, err
	}
	granted, envAdded, err := claudesettings.RegisterWithEnv(root, PermissionRules, EnvDefaults, p, claudesettings.Local)
	if err != nil {
		return rep, err
	}
	permissionStep(rep, granted, "added ", "all rules already present")
	envStep(rep, envAdded, "set ", "all env defaults already present")

	marker := filepath.Join(root, resolve.LocalEnabledFile)
	if err := tx.snapshot(off, marker); err != nil {
		return rep, err
	}
	if err := removeLocalFile(off); err != nil {
		return rep, err
	}
	if err := ensureMarkerFile(marker, p); err != nil {
		return rep, err
	}
	rep.step("marker", "done", resolve.LocalEnabledFile)
	return rep, nil
}

func DisableLocal(root string) (_ *Report, resultErr error) {
	rep := &Report{RepoRoot: root}
	shared, err := resolve.EnabledMarker(root)
	if err != nil {
		return rep, err
	}
	if err := checkExistingLocalTargets(root, localHost, localSettings, resolve.LocalEnabledFile, resolve.LocalDisabledFile); err != nil {
		return rep, err
	}
	if shared {
		if err := checkLocalTargets(root, resolve.LocalDisabledFile); err != nil {
			return rep, err
		}
	}
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	if err != nil {
		return rep, err
	}
	tx := fileTransaction{p: p}
	defer tx.finish(&resultErr)
	host := filepath.Join(root, localHost)
	on := filepath.Join(root, resolve.LocalEnabledFile)
	off := filepath.Join(root, resolve.LocalDisabledFile)
	if err := tx.snapshot(host, filepath.Join(root, localSettings), on, off); err != nil {
		return rep, err
	}
	if err := ensurePersonalLockExcludes(root, &tx, true); err != nil {
		return rep, err
	}
	revoked, envRemoved, err := claudesettings.DeregisterWithEnv(root, disableRules, EnvDefaults, p, claudesettings.Local)
	if err != nil {
		return rep, err
	}
	permissionStep(rep, revoked, "removed ", "no ethos rules present")
	envStep(rep, envRemoved, "removed ", "no ethos env defaults present")
	if _, err := os.Lstat(host); !os.IsNotExist(err) {
		if _, err := claudemd.Deregister(host, localImport, claudemd.WithRepository(p)); err != nil {
			return rep, err
		}
	}
	if err := removeLocalFile(on); err != nil {
		return rep, err
	}
	if shared {
		return rep, ensureMarkerFile(off, p)
	}
	return rep, removeLocalFile(off)
}

func ensureMarkerFile(path string, p layout.Policy) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
		return nil
	} else if !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("%s is not a regular file", path)
	}
	return writeVendored(path, nil, p)
}

func removeLocalFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func checkExistingLocalTargets(root string, targets ...string) error {
	var existing []string
	for _, target := range targets {
		if _, err := os.Lstat(filepath.Join(root, target)); !os.IsNotExist(err) {
			existing = append(existing, target)
		}
	}
	return checkLocalTargets(root, existing...)
}

func ensurePersonalLockExcludes(root string, tx *fileTransaction, existingOnly bool) error {
	var targets []string
	for _, lock := range []struct{ domain, target string }{
		{"claudemd", localHost},
		{"claudesettings", localSettings},
	} {
		host := filepath.Join(root, lock.target)
		if existingOnly {
			if _, err := os.Lstat(host); os.IsNotExist(err) {
				continue
			}
		}
		path, err := tx.p.LockPath(lock.domain, host)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		targets = append(targets, filepath.ToSlash(rel))
	}
	return ensureLocalExcludes(root, tx, targets...)
}

func ensureLocalExcludes(root string, tx *fileTransaction, targets ...string) error {
	if len(targets) == 0 {
		return nil
	}
	matches, err := gitIgnoreMatchesInScope(root, targets, true)
	if err != nil {
		return err
	}
	var rules []string
	for _, target := range targets {
		if pattern := matches[target].pattern; pattern == "" || strings.HasPrefix(pattern, "!") {
			rules = append(rules, "/"+target)
		}
	}
	if len(rules) > 0 {
		out, err := gitenv.Run(gitenv.Spec{
			Git: "git", Dir: root, Env: gitenv.Parsed(),
			Args: []string{"rev-parse", "--path-format=absolute", "--git-path", "info/exclude"},
		})
		if err != nil {
			return fmt.Errorf("locating Git's local exclude file: %w", err)
		}
		path, err := gitenv.IndexPathOutput(out)
		if err != nil {
			return err
		}
		if err := tx.snapshot(path); err != nil {
			return err
		}
		next := tx.files[len(tx.files)-1]
		if !next.exists {
			next.mode = 0o644
		} else if !next.mode.IsRegular() {
			return fmt.Errorf("Git's local exclude file %s is not a regular file", path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		next.data = bytes.Clone(next.data)
		if len(next.data) > 0 && next.data[len(next.data)-1] != '\n' {
			next.data = append(next.data, '\n')
		}
		next.data = append(next.data, strings.Join(rules, "\n")+"\n"...)
		if err := next.replace(tx.p); err != nil {
			return err
		}
		if err := layout.SyncRepositoryDestination(tx.p, path); err != nil {
			return err
		}
	}
	return checkLocalTargets(root, targets...)
}

func checkLocalTargets(root string, targets ...string) error {
	if len(targets) == 0 {
		return nil
	}
	if !filepath.IsAbs(root) || root != filepath.Clean(root) {
		return fmt.Errorf("invalid checkout root %q", root)
	}
	matches, err := gitIgnoreMatchesInScope(root, targets, true)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if pattern := matches[target].pattern; pattern == "" || strings.HasPrefix(pattern, "!") {
			return fmt.Errorf("personal scope requires %s to be ignored by git", target)
		}
		for path := filepath.Join(root, target); path != root; path = filepath.Dir(path) {
			fi, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("personal target %s crosses symlink %s", target, path)
			}
		}
	}
	args := append([]string{"ls-files", "-z", "--cached", "--"}, targets...)
	out, err := gitenv.Run(gitenv.Spec{Git: "git", Dir: root, Args: args, Env: gitenv.Parsed()})
	if err != nil {
		return fmt.Errorf("checking tracked personal files: %w", err)
	}
	if first, _, _ := bytes.Cut(out, []byte{0}); len(first) != 0 {
		return fmt.Errorf("personal target %s is tracked by git", first)
	}
	return nil
}

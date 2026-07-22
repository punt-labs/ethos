package enable

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The vendored zone is exactly these two repo-relative paths. enable writes
// them wholesale and never any other path under .punt-labs/ethos/, so the
// overwrite is bounded to the vendored zone by construction (§7).
const (
	guideRel    = ".punt-labs/ethos/CLAUDE.md"
	manifestRel = ".punt-labs/ethos/.vendored-manifest"
)

// deposit writes the vendored zone under §7 manifest semantics: it writes
// exactly the new-manifest set (the guide and the manifest itself), removes
// any path the previous manifest listed but the new one does not, and
// collision-errors on any new-manifest path that already exists but is not in
// the previous manifest. On the first manifest-aware run (no previous
// manifest) the new set is grandfathered as the previous set, so an
// already-deposited guide does not error — but a manifest path in a
// non-vendored zone errors unconditionally.
//
// It returns any warnings — on first contact (bootstrap), overwriting an
// existing vendored file whose content differs from what we deposit is
// grandfathered by punt-labs-dir §7, but the overwrite is surfaced by naming
// the path so the (git-tracked, recoverable) clobber is not silent (S2).
func deposit(repoRoot string, guide []byte) ([]string, error) {
	newSet := []string{guideRel, manifestRel}
	want := map[string][]byte{guideRel: guide, manifestRel: manifestBytes(newSet)}

	prev, err := readManifest(filepath.Join(repoRoot, manifestRel))
	if err != nil {
		return nil, err
	}
	prevSet := prev
	bootstrap := prev == nil
	if bootstrap {
		prevSet = newSet
	}

	var warnings []string
	for _, rel := range newSet {
		if !isVendored(rel) {
			return nil, fmt.Errorf("enable: manifest path %s is not in the vendored zone — refusing to deposit", rel)
		}
		if contains(prevSet, rel) {
			// Grandfathered on first contact: warn when we are about to
			// overwrite differing content, so the clobber is visible.
			if bootstrap {
				if existing, err := os.ReadFile(filepath.Join(repoRoot, rel)); err == nil && !bytes.Equal(existing, want[rel]) {
					warnings = append(warnings, fmt.Sprintf(
						"%s existed with different content and was overwritten by the vendored guide (punt-labs-dir §7 first-enable grandfather; the prior file is recoverable from git)", rel))
				}
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err == nil {
			return nil, fmt.Errorf("enable: %s already exists and is not in the previous vendored manifest — refusing to overwrite repo-owned data (collision)", rel)
		}
	}

	for _, rel := range prevSet {
		if bootstrap || contains(newSet, rel) {
			continue
		}
		if err := os.Remove(filepath.Join(repoRoot, rel)); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("enable: removing stale vendored path %s: %w", rel, err)
		}
	}

	if err := writeVendored(filepath.Join(repoRoot, guideRel), guide); err != nil {
		return nil, err
	}
	if err := writeVendored(filepath.Join(repoRoot, manifestRel), manifestBytes(newSet)); err != nil {
		return nil, err
	}
	return warnings, nil
}

// isVendored reports whether rel is one of the two paths the vendored zone
// owns. Any other path under .punt-labs/ethos/ is Config, Local, or
// seal-managed data enable must never write.
func isVendored(rel string) bool {
	return rel == guideRel || rel == manifestRel
}

// manifestBytes renders the manifest: one repo-relative path per line.
func manifestBytes(paths []string) []byte {
	return []byte(strings.Join(paths, "\n") + "\n")
}

// readManifest returns the paths listed in the manifest at path, or nil when
// the manifest does not exist (a repo enable has not yet run under the
// manifest-aware code).
func readManifest(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading vendored manifest %s: %w", path, err)
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// writeVendored writes data to path (creating parent dirs), atomically via a
// temp file in the target's own directory.
func writeVendored(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("setting mode on %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", name, path, err)
	}
	return nil
}

func contains(set []string, s string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

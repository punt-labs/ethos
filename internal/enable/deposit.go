package enable

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// The vendored zone is exactly these three repo-relative paths. enable writes
// them wholesale and never any other path under .punt-labs/ethos/, so the
// overwrite is bounded to the vendored zone by construction (§7).
const (
	guideRel    = ".punt-labs/ethos/CLAUDE.md"
	setupRel    = ".punt-labs/ethos/ETHOS-SETUP.md"
	manifestRel = ".punt-labs/ethos/.vendored-manifest"
)

// deposit writes the vendored zone under §7 manifest semantics: it writes
// exactly the new-manifest set (the guide, the setup playbook, and the
// manifest itself), validates the entire previous manifest against the owned
// paths, and collision-errors on any new-manifest path that already
// exists but is not in the previous manifest. On the first manifest-aware run
// (no previous manifest) the new set is grandfathered as the previous set, so
// an already-deposited guide does not error — but a manifest path in a
// non-vendored zone errors unconditionally.
//
// It returns any warnings — on first contact (bootstrap), overwriting an
// existing vendored file whose content differs from what we deposit is
// grandfathered by punt-labs-dir §7, but the overwrite is surfaced by naming
// the path so the (git-tracked, recoverable) clobber is not silent (S2).
func deposit(repoRoot string, guide, setup []byte, policies ...layout.Policy) ([]string, error) {
	p, err := layout.NewRepository(repoRoot, repoRoot, layout.RecordBlame)
	if len(policies) > 0 {
		p = policies[0]
	} else if err != nil {
		return nil, fmt.Errorf("deposit layout: %w", err)
	}
	newSet := []string{guideRel, setupRel, manifestRel}
	want := map[string][]byte{guideRel: guide, setupRel: setup, manifestRel: manifestBytes(newSet)}

	// Validate before creating even a staging directory. A manifest is an
	// inventory of known deposits, not authority to claim or delete arbitrary
	// repository files. Its physical location must not redirect the deposit.
	if err := preflightVendoredAncestors(repoRoot); err != nil {
		return nil, err
	}
	prev, err := readManifest(filepath.Join(repoRoot, manifestRel))
	if err != nil {
		return nil, err
	}
	prevSet := prev
	bootstrap := prev == nil
	if bootstrap {
		if err := preflightBootstrapContent(repoRoot); err != nil {
			return nil, err
		}
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
		if _, err := os.Lstat(filepath.Join(repoRoot, rel)); err == nil {
			return nil, fmt.Errorf("enable: %s already exists and is not in the previous vendored manifest — refusing to overwrite repo-owned data (collision)", rel)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("enable: examining new vendored path %s: %w", rel, err)
		}
	}

	// There are no supported retired paths to remove: DES-059's original
	// guide/manifest pair is a subset of DES-071's current three-file set.
	// Never infer historical ownership from an untrusted manifest entry.

	// The manifest is written first, ahead of the content it describes. Once
	// it lands, a retry's prevSet already equals newSet, so every
	// newSet path is grandfathered rather than checked for collision — an
	// interrupt between here and the last content write is safe to resume.
	// Writing content first would leave a retry unable to tell "our own
	// interrupted deposit" from "someone else's file at that path," and
	// hard-collide on the former.
	if err := writeVendored(filepath.Join(repoRoot, manifestRel), want[manifestRel], p); err != nil {
		return nil, err
	}
	if err := writeVendored(filepath.Join(repoRoot, guideRel), guide, p); err != nil {
		return nil, err
	}
	if err := writeVendored(filepath.Join(repoRoot, setupRel), setup, p); err != nil {
		return nil, err
	}
	return warnings, nil
}

// preflightBootstrapContent checks both leaves before any content comparison or
// deposit mutation. Only absent or regular files can be grandfathered: ReadFile
// on a FIFO or device could block indefinitely, and links must not redirect it.
// Like the ancestor preflight, this assumes no concurrent path replacement.
func preflightBootstrapContent(repoRoot string) error {
	for _, rel := range []string{guideRel, setupRel} {
		info, err := os.Lstat(filepath.Join(repoRoot, rel))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("enable: examining bootstrap content %s: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("enable: bootstrap content %s is not a regular file; refusing to deposit", rel)
		}
	}
	return nil
}

// isVendored reports whether rel is one of the three paths the vendored zone
// owns. Any other path under .punt-labs/ethos/ is Config, Local, or
// seal-managed data enable must never write.
func isVendored(rel string) bool {
	return rel == guideRel || rel == setupRel || rel == manifestRel
}

// preflightVendoredAncestors requires physical directory ancestors, like the
// shared-local staging helper. Missing directories may be created by the writer,
// but existing links (including dangling ones) must not redirect reads or writes.
// This is a preflight, not a sandbox against concurrent ancestor replacement;
// callers supply the physical checkout root from the resolver.
func preflightVendoredAncestors(repoRoot string) error {
	var dirs []string
	for dir := filepath.Dir(filepath.Join(repoRoot, manifestRel)); ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return nil // descendants of this absent directory are absent too
		}
		if err != nil {
			return fmt.Errorf("enable: examining vendored ancestor %s: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("enable: vendored ancestor %s is not a physical directory; refusing to deposit", dir)
		}
	}
	return nil
}

// manifestBytes renders the manifest: one repo-relative path per line.
func manifestBytes(paths []string) []byte {
	return []byte(strings.Join(paths, "\n") + "\n")
}

// readManifest returns the paths listed in the manifest at path, or nil when
// the manifest does not exist (a repo enable has not yet run under the
// manifest-aware code). Existing manifests must be regular files containing
// only exact owned paths, one per LF/CRLF-delimited line, without duplicates.
// CRLF supports Git's Windows checkout conversion. Do not trim path whitespace
// or clean paths: that would turn malformed input into ownership.
func readManifest(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading vendored manifest %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("enable: vendored manifest %s is not a regular file; refusing to deposit", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading vendored manifest %s: %w", path, err)
	}
	paths := make([]string, 0, 3) // non-nil: an existing empty file is not bootstrap
	if len(data) == 0 {
		return paths, nil
	}
	for i, line := range strings.SplitAfter(string(data), "\n") {
		if line == "" {
			continue // only the empty suffix after a final newline
		}
		rel := line
		if strings.HasSuffix(rel, "\n") {
			rel = strings.TrimSuffix(strings.TrimSuffix(rel, "\n"), "\r")
		}
		if !isVendored(rel) {
			return nil, fmt.Errorf("enable: vendored manifest %s line %d names an unowned path %q; refusing to deposit", path, i+1, rel)
		}
		if contains(paths, rel) {
			return nil, fmt.Errorf("enable: vendored manifest %s line %d repeats path %q; refusing to deposit", path, i+1, rel)
		}
		paths = append(paths, rel)
	}
	return paths, nil
}

// writeVendored writes data to path (creating parent dirs), atomically via a
// unique temp file in the canonical shared-local zone.
func writeVendored(path string, data []byte, p layout.Policy) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := createVendoredTemp(path, p)
	if err != nil {
		return fmt.Errorf("creating staging file for %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting mode on %s: %w", name, err)
	}
	if err := syncVendoredFile(tmp); err != nil {
		return fmt.Errorf("syncing %s: %w", name, errors.Join(err, tmp.Close()))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", name, path, err)
	}
	if err := syncVendoredDestination(p, path); err != nil {
		return &layout.InstallDurabilityError{Path: path, Err: err}
	}
	return nil
}

var syncVendoredFile = (*os.File).Sync
var syncVendoredDestination = layout.SyncRepositoryDestination

func createVendoredTemp(path string, p layout.Policy) (*os.File, error) {
	return layout.CreateLocalTemp(p, filepath.Base(path), "")
}

func contains(set []string, s string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

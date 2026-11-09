package enable

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/githook"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/textscan"
)

// RefreshHooks re-chains the git hook sections in the repo at repoRoot when
// they differ from what this build installs. See RefreshHooksTo.
func RefreshHooks(repoRoot string) (*Report, error) {
	return RefreshHooksTo(repoRoot, repoRoot)
}

// Step statuses RefreshHooksTo records beyond githook's write actions. Neither
// changed the hook: the section is still stale, and the caller should say so.
const (
	// StatusSkippedNonShell is githook's action for a Python/Node/binary hook the
	// section cannot be chained into, so the seal never runs from it.
	StatusSkippedNonShell = "skipped-non-shell"
	// StatusSkippedTracked: core.hooksPath puts the hook inside the work tree,
	// a tracked file a refresh must not rewrite unasked.
	StatusSkippedTracked = "skipped-tracked"
)

// RefreshHooksTo is the hook-only slice of EnableTo: it deposits nothing,
// touches no .gitignore, writes no marker, and adds no import. A release that
// changes a hook section otherwise has no effect until the operator re-runs
// `ethos enable` in every repo, and a stale seal section can block every
// commit. SessionStart calls this to close that gap.
//
// It does nothing in a repo without the enabled marker, and nothing for a hook
// whose section already matches this build. The report carries one step per
// hook it changed or could not change (see the Status constants), so an empty
// Steps means nothing was stale. A failure on one hook does not stop the other;
// the errors are joined, and the report still lists what was written.
func RefreshHooksTo(repoRoot, storeRoot string) (*Report, error) {
	rep := &Report{RepoRoot: repoRoot}
	present, err := resolve.EnabledMarker(repoRoot)
	if err != nil {
		return rep, fmt.Errorf("refreshing hooks: %w", err)
	}
	if !present {
		return rep, nil
	}
	return rep, refreshHooks(repoRoot, storeRoot, rep)
}

// refreshHooks is RefreshHooksTo past the marker check.
func refreshHooks(repoRoot, storeRoot string, rep *Report) error {
	p, err := layout.NewRepository(storeRoot, repoRoot, layout.RecordBlame)
	if err != nil {
		return fmt.Errorf("refresh hooks layout: %w", err)
	}
	l, dir, err := hooksTarget(repoRoot)
	if err != nil {
		return err
	}
	rep.Warnings = append(rep.Warnings, l.Warnings...)
	var errs []error
	for _, name := range layout.HookNames() {
		s, ok := hookSpecs[name]
		if !ok {
			errs = append(errs, fmt.Errorf("no hook template for %s", name))
			continue
		}
		path := filepath.Join(dir, name)
		current, err := sectionCurrent(path, s.src, s.tag, s.ident)
		if err != nil {
			errs = append(errs, fmt.Errorf("checking %s: %w", name, err))
			continue
		}
		if current {
			continue
		}
		if l.InWorkTree {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf(
				"%s is a tracked file inside the work tree (core.hooksPath) and its ethos section is stale; not rewriting it — run `ethos enable` to update it", path))
			rep.step("hook:"+name, StatusSkippedTracked, path)
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, fmt.Errorf("creating hooks dir %s: %w", dir, err))
			continue
		}
		res, err := githook.Chain(path, s.src, s.tag, s.ident, githook.WithRepository(p))
		rep.Warnings = append(rep.Warnings, res.Warnings...)
		// A durability failure still wrote the bytes: record the step so a
		// partial refresh is not read as nothing refreshed.
		if res.Action != "" {
			rep.step("hook:"+name, res.Action, res.Path)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("chaining %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// sectionCurrent reports whether the hook at path holds exactly one section for
// tag that equals this build's section, ignoring line terminators as doctor's
// currency check does. A missing file or section is not current. A malformed
// section returns the githook error so the caller reports it.
func sectionCurrent(path string, src []byte, tag, ident string) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	got, ok, err := githook.InstalledSection(data, tag, ident)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return bytes.Equal(normalizeEOL(got), normalizeEOL(githook.ExpectedSection(tag, src))), nil
}

func normalizeEOL(b []byte) []byte {
	var out bytes.Buffer
	for _, line := range textscan.SplitKeepEnds(b) {
		out.WriteString(textscan.StripTerminator(line))
		out.WriteByte('\n')
	}
	return out.Bytes()
}

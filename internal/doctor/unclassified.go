package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/githook"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// unclassifiedZones are the two trees whose files ethos owns. Files other
// tools keep under .punt-labs/ are not ethos's to classify (DES-079).
var unclassifiedZones = []string{
	filepath.Join(".punt-labs", "ethos"),
	filepath.Join(".punt-labs", "local", "ethos"),
}

// maxListedUnclassified bounds the files named in the check's detail line.
const maxListedUnclassified = 10

// CheckUnclassifiedFiles warns about files under .punt-labs/ethos/ and
// .punt-labs/local/ethos/ that no entry of layout.Table matches, and about
// hook files ethos wrote elsewhere under .punt-labs/. Such a file is one some
// writer created without a class, or one a person put there; either way git
// cannot be told whether to track it. Symlinks are reported, not followed.
//
// repoRoot is the working tree doctor runs in. Inside a linked worktree that is
// the worktree, not the shared main checkout: the check describes the tree
// whose files git would commit.
func CheckUnclassifiedFiles(repoRoot string) Result {
	name := "Unclassified files"
	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not scanned: not in a repository"}
	}
	// ETHOS_REPO_ROOT may be relative; every comparison below needs one spelling.
	repoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("making repository root absolute: %v", err)}
	}

	lk := githook.HooksDir(repoRoot)
	hooksDir := lk.Display() // a degraded lookup is reported below, not acted on
	table, err := layout.TableFor(repoRoot, hooksDir)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("resolving hooks directory %s: %v", hooksDir, err)}
	}

	sc := newScan()
	for _, zone := range unclassifiedZones {
		scanned, err := sc.collectUnclassified(table, repoRoot, zone)
		if err != nil {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("scanning %s: %v", filepath.Join(repoRoot, zone), err)}
		}
		if scanned {
			sc.zones++
		}
	}
	if err := sc.collectHookFiles(repoRoot, hooksDir); err != nil {
		return Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}

	note := ""
	switch {
	case lk.Degraded:
		note = "; hooks directory not verified: " + lk.Reason
	case len(lk.Warnings) > 0:
		note = "; hooks directory: " + strings.Join(lk.Warnings, "; ")
	}
	status, detail := "PASS", ""
	switch {
	case !sc.clean():
		status, detail = "WARN", sc.detail()+" — see DES-079"
	case sc.zones == 0:
		detail = "not scanned: neither .punt-labs/ethos nor .punt-labs/local/ethos exists"
	default:
		detail = "every file under .punt-labs/ethos and .punt-labs/local/ethos has a class"
	}
	if lk.Degraded {
		status = "WARN"
	}
	return Result{Name: name, Status: status, Detail: detail + note}
}

// scan is what the check found: unclassified files, paths it could not scan,
// and summary lines that are neither. Only files are counted as files.
type scan struct {
	files   map[string]bool // paths no table entry matches
	skipped map[string]bool // why a path was not scanned
	notes   map[string]bool // one-line findings about the hooks directory
	zones   int             // zones that existed and were walked
}

func newScan() *scan {
	return &scan{files: map[string]bool{}, skipped: map[string]bool{}, notes: map[string]bool{}}
}

func (s *scan) clean() bool { return len(s.files)+len(s.skipped)+len(s.notes) == 0 }

// detail renders the findings: the unclassified files with their count, then
// what was not scanned, then the hooks-directory lines.
func (s *scan) detail() string {
	var parts []string
	if files := sortedKeys(s.files); len(files) > 0 {
		listed, more := files, ""
		if len(files) > maxListedUnclassified {
			listed = files[:maxListedUnclassified]
			more = fmt.Sprintf(" and %d more", len(files)-maxListedUnclassified)
		}
		count := fmt.Sprintf("%d files match", len(files))
		if len(files) == 1 {
			count = "1 file matches"
		}
		parts = append(parts, fmt.Sprintf("%s no entry of the path-class table: %s%s", count, strings.Join(listed, ", "), more))
	}
	if skipped := sortedKeys(s.skipped); len(skipped) > 0 {
		parts = append(parts, "not scanned: "+strings.Join(skipped, ", "))
	}
	parts = append(parts, sortedKeys(s.notes)...)
	return strings.Join(parts, "; ")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// collectUnclassified records every file under root/zone that no table entry
// matches, as a path relative to root, and every symlink it does not follow.
// It reports whether it walked the zone. Each component from root down to the
// zone is checked first: a symlink anywhere on the way, .punt-labs included,
// stops the scan there and is recorded, because walking it would read files
// outside the tree. A zone that does not exist is not an error.
func (s *scan) collectUnclassified(table []layout.Entry, root, zone string) (bool, error) {
	parts := strings.Split(filepath.ToSlash(zone), "/")
	for i := range parts {
		comp := path.Join(parts[:i+1]...)
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(comp)))
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			s.skipped[comp+" is a symlink"] = true
			return false, nil
		}
	}
	return s.walkZone(table, root, zone)
}

// walkZone walks root/zone, recording what collectUnclassified records. A
// directory with a .git of its own is a separate working tree: it is recorded
// and skipped, not scanned. It reports whether the zone root was walked; a root
// removed after the caller's Lstat was not.
func (s *scan) walkZone(table []layout.Entry, root, zone string) (bool, error) {
	zoneRoot := filepath.Join(root, zone)
	walked := false
	err := filepath.WalkDir(zoneRoot, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // removed while we walked, e.g. a migrate staging dir
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if d.IsDir() {
			if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return err
				}
				s.skipped[filepath.ToSlash(rel)+" is a separate git working tree"] = true
				return fs.SkipDir
			} else if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("checking %s for .git: %w", p, err)
			}
			if p == zoneRoot {
				walked = true
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			s.skipped[rel+" is a symlink"] = true
			return nil
		}
		if _, _, ok := layout.ClassifyIn(table, rel); !ok {
			s.files[rel] = true
		}
		return nil
	})
	return walked && err == nil, err
}

// collectHookFiles records one note naming the hooks directory, when
// core.hooksPath puts it at or under .punt-labs/ and ethos hook files exist
// there. layout refuses entries for such a directory, and a zone entry may
// claim the files by accident (a mission's artifacts/ is class 3), so the
// check names the directory rather than rely on the zone walk. The walk's own
// entries for these files are dropped as duplicates, matched by file identity:
// on a case-insensitive file system the walk may spell a path differently from
// core.hooksPath.
func (s *scan) collectHookFiles(root, hooksDir string) error {
	paths, err := layout.PuntLabsHookPaths(root, hooksDir)
	if err != nil {
		return fmt.Errorf("resolving hooks directory %s: %w", hooksDir, err)
	}
	var names []string
	for _, rel := range paths {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("checking hook file %s: %w", rel, err)
		}
		names = append(names, path.Base(rel))
		if err := dropSameFile(s.files, root, info); err != nil {
			return err
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	s.notes[fmt.Sprintf("core.hooksPath %s is inside .punt-labs; ethos hook files there are unclassified: %s",
		path.Dir(paths[0]), strings.Join(names, ", "))] = true
	return nil
}

// dropSameFile deletes from files every path (relative to root) that names the
// file info describes. A path that has vanished since the walk is not that file.
func dropSameFile(files map[string]bool, root string, info os.FileInfo) error {
	for rel := range files {
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("checking %s: %w", rel, err)
		}
		if os.SameFile(fi, info) {
			delete(files, rel)
		}
	}
	return nil
}

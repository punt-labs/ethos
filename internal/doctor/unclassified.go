package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	hooksDir, warns := githook.HooksDir(repoRoot)
	table, err := layout.TableFor(repoRoot, hooksDir)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("resolving hooks directory %s: %v", hooksDir, err)}
	}

	seen := make(map[string]bool)
	for _, zone := range unclassifiedZones {
		if err := collectUnclassified(table, repoRoot, zone, seen); err != nil {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("scanning %s: %v", filepath.Join(repoRoot, zone), err)}
		}
	}
	if err := collectHookFiles(table, repoRoot, hooksDir, seen); err != nil {
		return Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}

	note := ""
	if len(warns) > 0 {
		note = "; hooks directory: " + strings.Join(warns, "; ")
	}
	if len(seen) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "every file under .punt-labs/ethos and .punt-labs/local/ethos has a class" + note}
	}

	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	listed := files
	more := ""
	if len(files) > maxListedUnclassified {
		listed = files[:maxListedUnclassified]
		more = fmt.Sprintf(" and %d more", len(files)-maxListedUnclassified)
	}
	count := fmt.Sprintf("%d files match", len(files))
	if len(files) == 1 {
		count = "1 file matches"
	}
	return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
		"%s no entry of the path-class table or were not scanned: %s%s — see DES-079%s",
		count, strings.Join(listed, ", "), more, note)}
}

// collectUnclassified adds to seen every entry under root/zone that no table
// entry matches, or that is a symlink and so is not scanned, as a path relative
// to root. A zone that does not exist is not an error.
func collectUnclassified(table []layout.Entry, root, zone string, seen map[string]bool) error {
	dir := filepath.Join(root, zone)
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		seen[filepath.ToSlash(zone)+" is a symlink; not scanned"] = true
		return nil
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // removed while we walked, e.g. a migrate staging dir
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			seen[rel+" (symlink, not scanned)"] = true
			return nil
		}
		if _, _, ok := layout.ClassifyIn(table, rel); !ok {
			seen[rel] = true
		}
		return nil
	})
}

// collectHookFiles adds to seen the hook files ethos wrote into a hooks
// directory under .punt-labs/ that no table entry matches. The zone walk
// misses them when the directory lies outside both zones.
func collectHookFiles(table []layout.Entry, root, hooksDir string, seen map[string]bool) error {
	paths, err := layout.PuntLabsHookPaths(root, hooksDir)
	if err != nil {
		return fmt.Errorf("resolving hooks directory %s: %w", hooksDir, err)
	}
	for _, rel := range paths {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("checking hook file %s: %w", rel, err)
		}
		if _, _, ok := layout.ClassifyIn(table, rel); !ok {
			seen[rel] = true
		}
	}
	return nil
}

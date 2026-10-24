package doctor

import (
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
// .punt-labs/local/ethos/ that no entry of layout.Table matches. Such a file
// is one some writer created without a class, or one a person put there; either
// way git cannot be told whether to track it. It reads both the checkout and
// the shared store, which differ only inside a linked worktree.
func CheckUnclassifiedFiles(repoRoot, storeRoot string) Result {
	name := "Unclassified files"

	roots := distinctRoots(repoRoot, storeRoot)
	if len(roots) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	seen := make(map[string]bool)
	for _, root := range roots {
		hooksDir, _ := githook.HooksDir(root)
		table := layout.TableFor(root, hooksDir)
		for _, zone := range unclassifiedZones {
			if err := collectUnclassified(table, root, zone, seen); err != nil {
				return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("scanning %s: %v", filepath.Join(root, zone), err)}
			}
		}
	}
	if len(seen) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "every file under .punt-labs/ethos and .punt-labs/local/ethos has a class"}
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
	return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
		"%d files match no entry of the path-class table: %s%s — see DES-079",
		len(files), strings.Join(listed, ", "), more)}
}

// distinctRoots returns the non-empty roots, once each.
func distinctRoots(roots ...string) []string {
	var out []string
	for _, r := range roots {
		if r == "" {
			continue
		}
		dup := false
		for _, o := range out {
			if o == r {
				dup = true
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out
}

// collectUnclassified adds to seen every file under root/zone that no table
// entry matches, as a path relative to root. A missing zone is not an error.
func collectUnclassified(table []layout.Entry, root, zone string, seen map[string]bool) error {
	dir := filepath.Join(root, zone)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, _, ok := layout.ClassifyIn(table, rel); !ok {
			seen[rel] = true
		}
		return nil
	})
}

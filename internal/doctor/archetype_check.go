package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

// codeArchetypeNames are the archetypes whose delegated-worker invariant is
// data-driven from the deployed YAML (Archetype.RequireDelegatedWorker):
// leader-as-worker is forbidden for these, so shipped code always goes
// through a distinct, traceable delegate. An operator who upgrades the
// ethos binary without re-running `ethos seed` keeps running whatever YAML
// is already on disk — pre-field content parses the field as false, and
// the guard silently stops enforcing (ethos-e05k).
var codeArchetypeNames = []string{"implement", "test"}

// CheckDelegatedWorkerArchetypes flags a deployed "implement" or "test"
// archetype whose require_delegated_worker is not true. The detail names
// the resolving layer — "repo-local" or "global" — because a repo-local
// archetype that predates the field shadows an updated global one and
// `ethos seed` alone will not touch it; the operator needs to know which
// file to fix.
//
// An archetype that is not deployed in either layer is not this check's
// concern: mission creation already refuses loudly with "archetype not
// found" in that case (internal/mission.ErrArchetypeNotFound), so there is
// no silent gap there for doctor to surface.
func CheckDelegatedWorkerArchetypes(storeRoot string) Result {
	name := "Code archetype delegated-worker guard"

	if storeRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	globalRoot := ""
	if home, err := os.UserHomeDir(); err == nil {
		globalRoot = filepath.Join(home, ".punt-labs", "ethos")
	}
	store := mission.NewArchetypeStore(filepath.Join(storeRoot, ".punt-labs", "ethos"), globalRoot)

	repoArchRoot := filepath.Join(storeRoot, ".punt-labs", "ethos")
	var stale, broken []string
	for _, n := range codeArchetypeNames {
		a, layer, err := store.LoadLayer(n)
		if err != nil {
			if errors.Is(err, mission.ErrArchetypeNotFound) {
				continue // not deployed anywhere — not this check's concern
			}
			// A YAML parse failure, a strict-decode rejection, a permission
			// error, or any other non-not-found error means this archetype's
			// require_delegated_worker invariant could not be read at all —
			// treating that as "nothing to enforce" is exactly ethos-e05k's
			// failure mode recurring one layer up, inside the check written
			// to catch it. Report it loudly instead, naming the file this
			// check tried to load.
			archLayer, archPath := archetypeAttemptedPath(repoArchRoot, globalRoot, n)
			broken = append(broken, fmt.Sprintf("%s (%s, %s): %v", n, archLayer, archPath, err))
			continue
		}
		if !a.RequireDelegatedWorker {
			stale = append(stale, fmt.Sprintf("%s (%s)", n, layer))
		}
	}
	if len(stale) == 0 && len(broken) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "implement and test archetypes both require a delegated worker"}
	}
	sort.Strings(stale)
	sort.Strings(broken)
	var parts []string
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf(
			"require_delegated_worker is not set on: %s — run `ethos seed` to refresh (a repo-local file must be hand-edited or deleted first)",
			strings.Join(stale, ", ")))
	}
	if len(broken) > 0 {
		parts = append(parts, "could not load: "+strings.Join(broken, "; "))
	}
	return Result{Name: name, Status: "FAIL", Detail: strings.Join(parts, "; ")}
}

// archetypeAttemptedPath reports which layer and file LoadLayer(name) was
// reading when it returned a non-not-found error. LoadLayer tries the
// repo-local file first and only falls through to global on a not-found
// error, so a non-not-found error on a repo-local file never reaches the
// global layer — the repo-local file existing on disk is proof the error
// came from there.
func archetypeAttemptedPath(repoArchRoot, globalRoot, name string) (layer, path string) {
	repoPath := filepath.Join(repoArchRoot, "archetypes", name+".yaml")
	if _, err := os.Stat(repoPath); err == nil {
		return "repo-local", repoPath
	}
	return "global", filepath.Join(globalRoot, "archetypes", name+".yaml")
}

package doctor

import (
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

	var stale []string
	for _, n := range codeArchetypeNames {
		a, layer, err := store.LoadLayer(n)
		if err != nil {
			continue // not deployed anywhere — not this check's concern
		}
		if !a.RequireDelegatedWorker {
			stale = append(stale, fmt.Sprintf("%s (%s)", n, layer))
		}
	}
	if len(stale) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "implement and test archetypes both require a delegated worker"}
	}
	sort.Strings(stale)
	return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
		"require_delegated_worker is not set on: %s — run `ethos seed` to refresh (a repo-local file must be hand-edited or deleted first)",
		strings.Join(stale, ", "))}
}

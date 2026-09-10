package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/seed"
	"gopkg.in/yaml.v3"
)

// codeArchetypeNames derives the archetypes whose delegated-worker
// invariant is data-driven from the deployed YAML
// (Archetype.RequireDelegatedWorker) by reading the SAME embedded seed
// content `ethos seed` deploys from (seed.Archetypes), rather than a
// hand-maintained list.
//
// This is the fix for P2, ethos-e05k's failure mode recurring a second
// time inside the very check written to catch it: a hardcoded list can
// only ever monitor the archetypes someone remembered to add to it, so a
// third archetype gaining require_delegated_worker: true in the seed
// content would silently go unmonitored — the same "enforcement lost with
// a green check" shape as e05k itself, and the same shape C1 (this file's
// error-swallowing bug) already reproduced once. Deriving the list from
// the embed, the same pattern checklistAgentNames already uses for the
// seeded review-checklist agents (doctor.go), makes it structurally
// impossible for the monitored set to drift from what `ethos seed` ships.
//
// fsys and root are parameterized (rather than reading seed.Archetypes
// directly) so a test can inject a third archetype with the flag set and
// prove it gets picked up automatically — the property a hardcoded list
// could never demonstrate — without depending on the compile-time embed's
// actual current contents.
func codeArchetypeNames(fsys fs.FS, root string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("reading embedded %s: %w", root, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := fs.ReadFile(fsys, root+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading seeded archetype %s: %w", e.Name(), err)
		}
		var a mission.Archetype
		if err := yaml.Unmarshal(data, &a); err != nil {
			return nil, fmt.Errorf("parsing seeded archetype %s: %w", e.Name(), err)
		}
		if a.RequireDelegatedWorker {
			names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	sort.Strings(names)
	return names, nil
}

// CheckDelegatedWorkerArchetypes flags a deployed archetype named by
// codeArchetypeNames whose require_delegated_worker is not true. The
// detail names the resolving layer — "repo-local" or "global" — because a
// repo-local archetype that predates the field shadows an updated global
// one and `ethos seed` alone will not touch it; the operator needs to know
// which file to fix.
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

	monitored, err := codeArchetypeNames(seed.Archetypes, "sidecar/archetypes")
	if err != nil {
		// A broken embed is a build-time defect, not a runtime condition to
		// swallow — reporting it as an ordinary FAIL-with-nothing-flagged
		// would misread as "everything is fine" when this check could not
		// even determine what to look at (matches checklistAgentNames'
		// precedent for the same failure shape).
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("could not determine which archetypes require monitoring: %v", err)}
	}

	globalRoot := ""
	if home, err := os.UserHomeDir(); err == nil {
		globalRoot = filepath.Join(home, ".punt-labs", "ethos")
	}
	store := mission.NewArchetypeStore(filepath.Join(storeRoot, ".punt-labs", "ethos"), globalRoot)

	repoArchRoot := filepath.Join(storeRoot, ".punt-labs", "ethos")
	var stale, broken []string
	for _, n := range monitored {
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
		return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf(
			"%s archetypes all require a delegated worker", strings.Join(monitored, ", "))}
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

// validate-content checks all ethos content files against their package validators.
// It is CI infrastructure: it exercises validators that already exist and reports
// all failures in a single pass before exiting.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
)

// result records a single check result.
type result struct {
	pass   bool
	label  string
	detail string
}

func pass(label string) result { return result{pass: true, label: label} }
func fail(label, detail string) result {
	return result{pass: false, label: label, detail: detail}
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate-content: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}

	defaultEthosRoot := resolve.FindRepoEthosRoot()
	defaultGlobalRoot := filepath.Join(home, ".punt-labs", "ethos")

	var ethosRoot, globalRoot string
	flag.StringVar(&ethosRoot, "ethos-root", defaultEthosRoot, "path to .punt-labs/ethos/")
	flag.StringVar(&globalRoot, "global-root", defaultGlobalRoot, "path to global ethos dir")
	flag.Parse()

	if ethosRoot == "" {
		fmt.Fprintf(os.Stderr, "validate-content: ethos root not found\n")
		os.Exit(1)
	}
	if _, err := os.Stat(ethosRoot); err != nil {
		fmt.Fprintf(os.Stderr, "validate-content: ethos root not found: %s\n", ethosRoot)
		os.Exit(1)
	}

	hasGlobal := false
	if _, err := os.Stat(globalRoot); err == nil {
		hasGlobal = true
	}

	var results []result

	// Build identity stores.
	repoIDStore := identity.NewStore(ethosRoot)
	globalIDStore := identity.NewStore(globalRoot)
	layeredID := identity.NewLayeredStore(repoIDStore, globalIDStore)

	// List identities once. LayeredStore.List deduplicates by handle.
	listResult, err := layeredID.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate-content: listing identities: %v\n", err)
		os.Exit(1)
	}

	// Check 4: List warnings are load failures.
	for _, w := range listResult.Warnings {
		results = append(results, fail("identities: load failure", w))
	}

	// Check 4: duplicate handle detection.
	handleCount := make(map[string]int)
	for _, id := range listResult.Identities {
		handleCount[id.Handle]++
	}
	for h, n := range handleCount {
		if n > 1 {
			results = append(results, fail("identities: duplicate handle", fmt.Sprintf("%q (%d occurrences)", h, n)))
		}
	}

	// Checks 1 & 2: struct validation and referential integrity.
	nIdentities := len(listResult.Identities)
	idFails := 0
	for _, idRef := range listResult.Identities {
		// Reload with attribute resolution to populate Warnings.
		id, loadErr := layeredID.Load(idRef.Handle)
		if loadErr != nil {
			results = append(results, fail("identities: load", fmt.Sprintf("%s: %v", idRef.Handle, loadErr)))
			idFails++
			continue
		}
		if valErr := id.Validate(); valErr != nil {
			results = append(results, fail("identities: validate struct", fmt.Sprintf("%s: %v", id.Handle, valErr)))
			idFails++
		}
		for _, w := range id.Warnings {
			results = append(results, fail("identities: referential integrity", fmt.Sprintf("%s: %s", id.Handle, w)))
			idFails++
		}
	}
	if idFails == 0 {
		results = append(results, pass(fmt.Sprintf("identities: validate struct (%d identities)", nIdentities)))
	}

	// Check 5: agent file path resolution.
	repoRoot := resolve.FindRepoRoot()
	agentFails := 0
	for _, idRef := range listResult.Identities {
		if idRef.Agent == "" {
			continue
		}
		if repoRoot == "" {
			results = append(results, fail("identities: agent file resolution", fmt.Sprintf("%s: cannot determine repo root", idRef.Handle)))
			agentFails++
			continue
		}
		agentPath := filepath.Join(repoRoot, idRef.Agent)
		if _, err := os.Stat(agentPath); err != nil {
			results = append(results, fail("identities: agent file resolution", fmt.Sprintf("%s: %s not found", idRef.Handle, idRef.Agent)))
			agentFails++
		}
	}
	if agentFails == 0 {
		results = append(results, pass("identities: agent file resolution"))
	}

	// Checks 6 & 8: attribute slug validation and non-empty content.
	attrKinds := []attribute.Kind{attribute.Personalities, attribute.WritingStyles, attribute.Talents}
	totalAttrs := 0
	attrFails := 0
	for _, kind := range attrKinds {
		stores := []*attribute.Store{attribute.NewStore(ethosRoot, kind)}
		if hasGlobal {
			stores = append(stores, attribute.NewStore(globalRoot, kind))
		}
		for _, s := range stores {
			listRes, listErr := s.List()
			if listErr != nil {
				results = append(results, fail(fmt.Sprintf("attributes(%s): list", kind.DirName), listErr.Error()))
				attrFails++
				continue
			}
			for _, w := range listRes.Warnings {
				results = append(results, fail(fmt.Sprintf("attributes(%s): load failure", kind.DirName), w))
				attrFails++
			}
			for _, a := range listRes.Attributes {
				totalAttrs++
				if err := attribute.ValidateSlug(a.Slug); err != nil {
					results = append(results, fail(fmt.Sprintf("attributes(%s): invalid slug", kind.DirName), fmt.Sprintf("%q: %v", a.Slug, err)))
					attrFails++
				}
				if strings.TrimSpace(a.Content) == "" {
					results = append(results, fail(fmt.Sprintf("attributes(%s): empty content", kind.DirName), fmt.Sprintf("%q", a.Slug)))
					attrFails++
				}
			}
		}
	}
	if attrFails == 0 {
		results = append(results, pass(fmt.Sprintf("attributes: slug and content validation (%d attributes)", totalAttrs)))
	}

	// Check 3: team validation.
	teamStore := team.NewLayeredStore(ethosRoot, globalRoot)
	roleRepo := role.NewStore(ethosRoot)
	var roleGlobal *role.Store
	if hasGlobal {
		roleGlobal = role.NewStore(globalRoot)
	}
	roleExists := func(n string) bool {
		if roleRepo.Exists(n) {
			return true
		}
		return roleGlobal != nil && roleGlobal.Exists(n)
	}
	identityExists := func(h string) bool {
		return layeredID.Exists(h)
	}

	teamNames, err := teamStore.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate-content: listing teams: %v\n", err)
		os.Exit(1)
	}
	nTeams := len(teamNames)
	teamFails := 0
	for _, name := range teamNames {
		t, loadErr := teamStore.Load(name)
		if loadErr != nil {
			results = append(results, fail("teams: load", fmt.Sprintf("%s: %v", name, loadErr)))
			teamFails++
			continue
		}
		if valErr := team.Validate(t, identityExists, roleExists); valErr != nil {
			results = append(results, fail("teams: validate", fmt.Sprintf("%s: %v", name, valErr)))
			teamFails++
		}
	}
	if teamFails == 0 {
		results = append(results, pass(fmt.Sprintf("teams: structural validation (%d teams)", nTeams)))
	}

	// Print all results.
	nFail := 0
	for _, r := range results {
		if r.pass {
			fmt.Printf("PASS  %s\n", r.label)
		} else {
			fmt.Printf("FAIL  %s: %s\n", r.label, r.detail)
			nFail++
		}
	}

	if nFail == 0 {
		fmt.Printf("all checks passed (%d identities, %d teams, %d attributes)\n", nIdentities, nTeams, totalAttrs)
		os.Exit(0)
	}
	fmt.Printf("%d failure(s)\n", nFail)
	os.Exit(1)
}

// validate-content checks all ethos content files against their package validators.
// It is CI infrastructure: it exercises validators that already exist and reports
// all failures in a single pass before exiting.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/bundle"
	"github.com/punt-labs/ethos/internal/doctor"
	"github.com/punt-labs/ethos/internal/enable"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/schema"
	"github.com/punt-labs/ethos/internal/seed"
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

// checkReadmeTable compares the fields table embedded in a seeded README
// against the registry's MarkdownTable for that entity. It reads the
// embedded content — the bytes that actually ship — so the check cannot be
// fooled by an on-disk copy that differs from the build.
func checkReadmeTable(path string, e schema.Entity) result {
	label := fmt.Sprintf("readme: %s fields table", e.Wire)
	data, err := seed.Readmes.ReadFile(path)
	if err != nil {
		return fail(label, fmt.Sprintf("reading %s: %v", path, err))
	}
	got := tableBlock(string(data))
	want := strings.TrimRight(e.MarkdownTable(), "\n")
	if got != want {
		return fail(label, fmt.Sprintf("%s drifted from schema.%s.MarkdownTable(); update the README table to match the registry in internal/schema", path, e.Name))
	}
	return pass(label)
}

// checkSetupSync compares docs/ETHOS-SETUP.md — the DES-071 tier-C source of
// truth — against the embedded copy that ships in the binary
// (internal/enable/setup/ETHOS-SETUP.md, exposed as enable.Setup). The ADR
// requires the two stay byte-identical; `make sync-embed` closes any drift
// this check finds.
func checkSetupSync(repoRoot string) result {
	label := "enable: ETHOS-SETUP.md embed sync"
	path := filepath.Join(repoRoot, "docs", "ETHOS-SETUP.md")
	onDisk, err := os.ReadFile(path)
	if err != nil {
		return fail(label, fmt.Sprintf("reading %s: %v", path, err))
	}
	if !bytes.Equal(onDisk, enable.Setup) {
		return fail(label, "docs/ETHOS-SETUP.md and internal/enable/setup/ETHOS-SETUP.md have drifted — run `make sync-embed`")
	}
	return pass(label)
}

// tableBlock returns the contiguous run of markdown table rows in s: every
// line whose first non-space rune is a pipe. The seeded READMEs carry one
// such table (the fields block), so the run is unambiguous.
func tableBlock(s string) string {
	var rows []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			rows = append(rows, line)
		}
	}
	return strings.Join(rows, "\n")
}

// report is everything main needs to print and decide the exit code.
// Building it separately from main lets tests drive the checks against a
// fixture tree without going through flag.Parse or os.Exit.
type report struct {
	results     []result
	nIdentities int
	nTeams      int
	totalAttrs  int
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

	rep, err := run(ethosRoot, globalRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate-content: %v\n", err)
		os.Exit(1)
	}

	nFail := 0
	for _, r := range rep.results {
		if r.pass {
			fmt.Printf("PASS  %s\n", r.label)
		} else {
			fmt.Printf("FAIL  %s: %s\n", r.label, r.detail)
			nFail++
		}
	}

	if nFail == 0 {
		fmt.Printf("all checks passed (%d identities, %d teams, %d attributes)\n", rep.nIdentities, rep.nTeams, rep.totalAttrs)
		os.Exit(0)
	}
	fmt.Printf("%d failure(s)\n", nFail)
	os.Exit(1)
}

// run performs every content check against ethosRoot and globalRoot and
// returns the report, or an error for a hard failure that stops before any
// check can produce a result: a bad root, an unreadable listing, or a
// resolution: repo-only repo that cannot be honored.
//
// It resolves the repo's active bundle and DES-057 resolution mode the same
// way cmd/ethos does — through bundle.ResolveRoot and bundle.VerifyRepoOnly,
// the shared internal/bundle logic cmd/ethos's identityStore() also calls —
// so a repo-local bundle supplying an identity's talent is visible here too,
// and a repo-only misconfiguration the real CLI would refuse to start under
// fails this check loud rather than silently validating a config nothing
// would actually run under (ethos-ccjz).
func run(ethosRoot, globalRoot string) (report, error) {
	if ethosRoot == "" {
		return report{}, fmt.Errorf("ethos root not found")
	}
	if info, err := os.Stat(ethosRoot); err != nil {
		return report{}, fmt.Errorf("ethos root not found: %s", ethosRoot)
	} else if !info.IsDir() {
		return report{}, fmt.Errorf("ethos root is not a directory: %s", ethosRoot)
	}

	hasGlobal := false
	if info, err := os.Stat(globalRoot); err == nil {
		hasGlobal = info.IsDir()
	} else if !os.IsNotExist(err) {
		return report{}, fmt.Errorf("checking global root %s: %w", globalRoot, err)
	}

	// ethosRoot is always <repo>/.punt-labs/ethos — derive repo root
	// structurally rather than re-walking .git, matching the DES-057
	// consumers this file mirrors.
	repoRoot := filepath.Dir(filepath.Dir(ethosRoot))

	bundleRoot, active, err := bundle.ResolveRoot(repoRoot, globalRoot)
	if err != nil {
		return report{}, fmt.Errorf("bundle resolution failed: %w", err)
	}
	mode, err := resolve.ResolveResolution(repoRoot)
	if err != nil {
		return report{}, err
	}
	repoAuthoritative := mode == resolve.ResolutionRepoOnly
	if repoAuthoritative {
		if err := bundle.VerifyRepoOnly(ethosRoot, bundleRoot, active); err != nil {
			return report{}, err
		}
	}

	var results []result

	// Build identity stores.
	repoIDStore := identity.NewStore(ethosRoot)
	var bundleIDStore *identity.Store
	if bundleRoot != "" {
		bundleIDStore = identity.NewStore(bundleRoot)
	}
	globalIDStore := identity.NewStore(globalRoot)
	layeredID := identity.NewLayeredStoreWithBundle(repoIDStore, bundleIDStore, globalIDStore, repoAuthoritative)

	// List identities once. LayeredStore.List deduplicates by handle.
	listResult, err := layeredID.List()
	if err != nil {
		return report{}, fmt.Errorf("listing identities: %w", err)
	}

	// Load-level warnings from List() are failures (referential or parse errors).
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
	structFails := 0
	refFails := 0
	for _, id := range listResult.Identities {
		if valErr := id.Validate(); valErr != nil {
			results = append(results, fail("identities: validate struct", fmt.Sprintf("%s: %v", id.Handle, valErr)))
			structFails++
		}
		if refErr := layeredID.ValidateRefs(id); refErr != nil {
			results = append(results, fail("identities: referential integrity", fmt.Sprintf("%s: %v", id.Handle, refErr)))
			refFails++
		}
	}
	if structFails == 0 {
		results = append(results, pass(fmt.Sprintf("identities: validate struct (%d identities)", nIdentities)))
	}
	if refFails == 0 {
		results = append(results, pass("identities: referential integrity"))
	}

	// Check 5: agent file path resolution.
	results = append(results, checkSetupSync(repoRoot))
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
		if hasGlobal && !repoAuthoritative {
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
	teamStore := team.NewLayeredStoreWithBundle(ethosRoot, bundleRoot, globalRoot, repoAuthoritative)
	roleRepo := role.NewStore(ethosRoot)
	roleLayered := role.NewLayeredStoreWithBundle(ethosRoot, bundleRoot, globalRoot, repoAuthoritative)
	identityExists := func(h string) bool {
		return layeredID.Exists(h)
	}

	teamNames, err := teamStore.List()
	if err != nil {
		return report{}, fmt.Errorf("listing teams: %w", err)
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
		if valErr := team.Validate(t, identityExists, roleLayered.Exists); valErr != nil {
			results = append(results, fail("teams: validate", fmt.Sprintf("%s: %v", name, valErr)))
			teamFails++
		}
	}
	if teamFails == 0 {
		results = append(results, pass(fmt.Sprintf("teams: structural validation (%d teams)", nTeams)))
	}

	// DES-057: the repo-only completeness gate — the same predicate
	// `ethos vendor` runs on its own output, and `ethos doctor` runs on the
	// live tree — now runs on every push/PR via this binary rather than
	// nowhere in CI. PASSes with "not applicable" in layered mode.
	results = append(results, fromDoctor(doctor.CheckRepoSetComplete(layeredID, repoRoot)))

	// DES-069 R2: every mcp__ tool a role grants must be classified. Scoped
	// to this repo's own roles, not the bundle or global layers — those
	// belong to whichever repo owns them, and are validated there.
	roleTools := make(map[string][]string)
	roleNames, err := roleRepo.List()
	if err != nil {
		return report{}, fmt.Errorf("listing roles: %w", err)
	}
	for _, name := range roleNames {
		r, loadErr := roleRepo.Load(name)
		if loadErr != nil {
			results = append(results, fail("roles: load", fmt.Sprintf("%s: %v", name, loadErr)))
			continue
		}
		roleTools[name] = r.Tools
	}
	results = append(results, classifyMCPTools(roleTools)...)

	// Seeded README fields tables must equal the registry MarkdownTable().
	// This is a comparison, not a generation: a stale committed README fails
	// the build the instant the registry changes.
	readmeChecks := []struct {
		path   string
		entity schema.Entity
	}{
		{"sidecar/identities/README.md", schema.Identity},
		{"sidecar/roles/README.md", schema.Role},
		{"sidecar/teams/README.md", schema.Team},
	}
	for _, rc := range readmeChecks {
		results = append(results, checkReadmeTable(rc.path, rc.entity))
	}

	return report{results: results, nIdentities: nIdentities, nTeams: nTeams, totalAttrs: totalAttrs}, nil
}

// fromDoctor adapts a doctor.Result — which has a third WARN state
// alongside PASS/FAIL — onto validate-content's binary pass/fail result.
// WARN counts as pass here, matching doctor.Result.Passed(): it is an
// advisory (e.g. a hand-authored set whose extension completeness cannot
// be judged), not a build-breaking fault.
func fromDoctor(r doctor.Result) result {
	label := "doctor: " + r.Name
	if r.Passed() {
		if r.Status == "WARN" {
			label = fmt.Sprintf("%s (WARN: %s)", label, r.Detail)
		}
		return pass(label)
	}
	return fail(label, r.Detail)
}

// Package doctor provides shared health-check logic for the ethos CLI
// and MCP server.
package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/punt-labs/ethos/internal/team"
)

// Result holds the outcome of a single health check.
type Result struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Passed returns true when the check did not fail.
func (r Result) Passed() bool {
	return r.Status == "PASS"
}

// RunAll executes every standard health check and returns the results.
func RunAll(s identity.IdentityStore, ss *session.Store, repoRoot string, teams *team.LayeredStore) []Result {
	checks := []struct {
		name string
		fn   func(identity.IdentityStore, *session.Store) (string, bool)
	}{
		{"Identity directory", CheckIdentityDir},
		{"Human identity", CheckHumanIdentity},
		{"Default agent", CheckDefaultAgent},
		{"Duplicate fields", CheckDuplicateFields},
	}

	results := make([]Result, 0, len(checks)+1)
	for _, c := range checks {
		detail, ok := c.fn(s, ss)
		status := "PASS"
		if !ok {
			status = "FAIL"
		}
		results = append(results, Result{Name: c.name, Status: status, Detail: detail})
	}

	results = append(results, CheckOrphanedAgentFiles(repoRoot, teams))
	return results
}

// AllPassed returns true when every result passed.
func AllPassed(results []Result) bool {
	for _, r := range results {
		if !r.Passed() {
			return false
		}
	}
	return true
}

// PassedCount returns the number of passed results.
func PassedCount(results []Result) int {
	n := 0
	for _, r := range results {
		if r.Passed() {
			n++
		}
	}
	return n
}

// CheckOrphanedAgentFiles flags agent files in .claude/agents/ whose
// handle is not a member of any configured team.
func CheckOrphanedAgentFiles(repoRoot string, teams *team.LayeredStore) Result {
	name := "Orphaned agent files"

	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	pattern := filepath.Join(repoRoot, ".claude", "agents", "*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("could not glob agents: %s", err)}
	}
	if len(matches) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "no agent files"}
	}

	teamName, err := resolve.ResolveTeam(repoRoot)
	if err != nil {
		return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("could not load repo config: %s", err)}
	}
	if teamName == "" {
		return Result{Name: name, Status: "PASS", Detail: "no team configured"}
	}
	if teams == nil {
		return Result{Name: name, Status: "PASS", Detail: "no team store available"}
	}

	t, err := teams.Load(teamName)
	if err != nil {
		return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("could not load team %q: %s", teamName, err)}
	}

	members := make(map[string]bool, len(t.Members))
	for _, m := range t.Members {
		members[m.Identity] = true
	}

	var orphaned []string
	for _, path := range matches {
		handle := strings.TrimSuffix(filepath.Base(path), ".md")
		if !members[handle] {
			orphaned = append(orphaned, handle)
		}
	}

	if len(orphaned) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "no orphaned agent files"}
	}
	sort.Strings(orphaned)
	return Result{Name: name, Status: "FAIL", Detail: "orphaned agent files (not on any team): " + strings.Join(orphaned, ", ")}
}

// CheckIdentityDir verifies the identity directory exists.
func CheckIdentityDir(s identity.IdentityStore, _ *session.Store) (string, bool) {
	dir := s.IdentitiesDir()
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("not found: %s", dir), false
		}
		return fmt.Sprintf("error: %v", err), false
	}
	return dir, true
}

// CheckHumanIdentity resolves and loads the current human identity.
func CheckHumanIdentity(s identity.IdentityStore, ss *session.Store) (string, bool) {
	handle, err := resolve.Resolve(s, ss)
	if err != nil {
		return fmt.Sprintf("no match — %v", err), false
	}
	id, err := s.Load(handle, identity.Reference(true))
	if err != nil {
		return fmt.Sprintf("handle %q not loadable: %v", handle, err), false
	}
	return fmt.Sprintf("%s (%s)", id.Name, id.Handle), true
}

// CheckDefaultAgent checks whether a default agent is configured for the
// current repository. Three states: not-in-a-repo and not-configured
// are both "OK" (empty repos and repos without an agent field are
// legitimate). A ResolveAgent error — unreadable or malformed
// `.punt-labs/ethos.yaml` — is a diagnostic failure the user needs to
// see. The detail string is the raw error text with no "error: " prefix
// — doctor's output already prints a FAIL status column derived from
// the returned bool, so prepending "error: " would double-label.
func CheckDefaultAgent(s identity.IdentityStore, _ *session.Store) (string, bool) {
	repoRoot := resolve.FindRepoRoot()
	if repoRoot == "" {
		return "not in a git repo", true
	}
	handle, err := resolve.ResolveAgent(repoRoot)
	if err != nil {
		return err.Error(), false
	}
	if handle == "" {
		return "not configured", true
	}
	return handle, true
}

// CheckDuplicateFields scans all identities for duplicate github or email
// bindings.
func CheckDuplicateFields(s identity.IdentityStore, _ *session.Store) (string, bool) {
	result, err := s.List()
	if err != nil {
		return fmt.Sprintf("error: %v", err), false
	}
	var dupes []string
	seen := map[string]map[string]string{
		"github": {},
		"email":  {},
	}
	for _, id := range result.Identities {
		for field, values := range seen {
			var val string
			switch field {
			case "github":
				val = id.GitHub
			case "email":
				val = id.Email
			}
			if val == "" {
				continue
			}
			if prev, ok := values[val]; ok {
				dupes = append(dupes, fmt.Sprintf("%s %q: %s and %s", field, val, prev, id.Handle))
			} else {
				values[val] = id.Handle
			}
		}
	}
	if len(dupes) > 0 {
		return "duplicates found: " + strings.Join(dupes, "; "), false
	}
	return "no duplicates", true
}

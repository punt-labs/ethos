// Package doctor provides shared health-check logic for the ethos CLI
// and MCP server.
package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/githook"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/punt-labs/ethos/internal/team"
	"github.com/punt-labs/ethos/internal/textscan"
)

// Result holds the outcome of a single health check.
type Result struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Passed reports whether the check did not fail. It is explicit about the
// three valid statuses: PASS and WARN return true (WARN is advisory — the
// gated-but-unenabled state), FAIL returns false. Any other value (empty or a
// typo like "PAS") returns false, so a malformed status surfaces as a failure
// in summaries rather than being silently counted as passed. Only AnyFailed
// gates a non-zero exit; callers that want to surface WARN distinctly read
// Status, which renders verbatim in the CLI table and the MCP summary.
func (r Result) Passed() bool {
	return r.Status == "PASS" || r.Status == "WARN"
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
	results = append(results, CheckSealHook(repoRoot))
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

// AnyFailed returns true when any result is an outright FAIL. A WARN is
// advisory and does not count — doctor's exit status gates on this, not on
// AllPassed, so a gated-but-unenabled repo (WARN) does not fail the command.
func AnyFailed(results []Result) bool {
	for _, r := range results {
		if r.Status == "FAIL" {
			return true
		}
	}
	return false
}

// WarnCount returns the number of advisory WARN results, so a summary can
// surface them distinctly rather than folding them into "passed".
func WarnCount(results []Result) int {
	n := 0
	for _, r := range results {
		if r.Status == "WARN" {
			n++
		}
	}
	return n
}

// PassedCount returns the number of strictly-PASS results. WARN is excluded
// so a summary that also reports WarnCount does not count a warned check
// twice (as both passed and a warning); the total is PassedCount + WarnCount +
// failures. Advisory gating (Passed/AllPassed treating WARN as not-failed) is
// unchanged — this is a counting distinction, not a status one.
func PassedCount(results []Result) int {
	n := 0
	for _, r := range results {
		if r.Status == "PASS" {
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

// CheckSealHook reports on the DES-058 audit-seal pre-commit hook, keyed on
// the enabled marker (§2.11). Four states:
//
//   - Enabled (marker present): FAIL when the seal hook is missing or
//     inactive; PASS when it carries an active seal call.
//   - Dormant / Absent (marker absent, no ethos hook): PASS "not enabled
//     here" — a never-enabled or disabled repo must not fail.
//   - Gated-but-unenabled (marker absent, hook chained): WARN — the chained
//     hook is inert behind its own marker gate, so a PASS would hide it and a
//     FAIL would over-report a repo awaiting convergence.
func CheckSealHook(repoRoot string) Result {
	name := "Audit seal hook"
	const remedy = " — run `ethos enable`"

	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	// A marker stat error that is not "does not exist" must not collapse to
	// "not enabled" — a doubly-broken repo (unreadable .punt-labs/ethos plus a
	// lost hook) would then read PASS while commits flow unsealed. Surface it
	// as a FAIL instead of guessing dormancy (S4).
	markerPresent := false
	if _, err := os.Stat(filepath.Join(repoRoot, ".punt-labs", "ethos", "enabled")); err == nil {
		markerPresent = true
	} else if !os.IsNotExist(err) {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot determine enablement here: %v", err)}
	}
	dir, _ := githook.HooksDir(repoRoot)
	hook := filepath.Join(dir, "pre-commit")

	info, statErr := os.Stat(hook)
	var body string
	if statErr == nil {
		data, err := os.ReadFile(hook)
		if err != nil {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot read %s: %v%s", hook, err, remedy)}
		}
		body = string(data)
	}
	// A commented-out call, a string-literal mention, or a dead branch must
	// not read as active, or the silent-absence state recurs behind a green
	// check. "Chained" for the gate check is the section marker OR an active
	// call — a stale section still counts as present.
	active := statErr == nil && hasActiveSealCall(body)
	chained := statErr == nil && (active || hasSealMarker(body))

	if !markerPresent {
		if chained {
			return Result{Name: name, Status: "WARN", Detail: "seal hook chained but ethos not enabled here" + remedy + " to converge, or remove the stale hook"}
		}
		return Result{Name: name, Status: "PASS", Detail: "not enabled here"}
	}

	// Enabled: the seal must be present and active.
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return Result{Name: name, Status: "FAIL", Detail: "enabled here but no pre-commit hook" + remedy}
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot stat %s: %v%s", hook, statErr, remedy)}
	}
	if !active {
		if strings.Contains(body, "DES-058") {
			return Result{Name: name, Status: "FAIL", Detail: "seal section present but no active 'audit seal' call (stale)" + remedy}
		}
		return Result{Name: name, Status: "FAIL", Detail: "enabled here but the seal hook is not chained" + remedy}
	}
	if !textscan.IsShellHook([]byte(body)) {
		return Result{Name: name, Status: "FAIL", Detail: "seal call present but the hook's shebang is not a shell — git runs it under another interpreter" + remedy}
	}
	if info.Mode().Perm()&0o111 == 0 {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("seal hook present but not executable — run: chmod +x %s", hook)}
	}
	if hasSealMarker(body) {
		return Result{Name: name, Status: "PASS", Detail: "chained seal section active"}
	}
	return Result{Name: name, Status: "PASS", Detail: "standalone seal hook active"}
}

// hasSealMarker reports whether body carries the DES-058 seal BEGIN marker on
// a real (non-heredoc) line. It consults the same textscan heredoc mask as
// hasActiveSealCall and githook, so a foreign hook that merely documents the
// marker text inside a heredoc is not misread as a chained section.
func hasSealMarker(body string) bool {
	data := []byte(body)
	lines := textscan.SplitKeepEnds(data)
	mask := textscan.HeredocMask(data)
	for i, raw := range lines {
		if !mask[i] && strings.HasPrefix(textscan.StripTerminator(raw), "# --- BEGIN ETHOS DES-058 SEAL") {
			return true
		}
	}
	return false
}

// sealInvocation matches an `audit seal` call in command position: the ethos
// binary (bare `ethos` or the hook's "$ethos_bin" variable) followed by
// `audit seal`. Command position means the token begins the line (after only
// indentation) or follows a statement separator (`;`, `&`, `|`, `(`, `!`) and
// optional whitespace — not merely any whitespace, so the phrase passed as
// ARGUMENTS to another command (`echo ethos audit seal`) does not match, and
// neither does a string-literal mention (`echo "audit seal"`).
var sealInvocation = regexp.MustCompile(`(^[\t ]*|[;&|(!][\t ]*)("?\$\{?ethos_bin\}?"?|ethos)[\t ]+audit[\t ]+seal([\s;&|)]|$)`)

// hasActiveSealCall reports whether body invokes `ethos audit seal` on a
// non-comment, non-heredoc line. The check is lexical, not a shell parser: it
// drops full-line and inline comments and skips here-document bodies (so a
// seal mention in usage text quoted via `cat <<EOF ... EOF` is not read as a
// real call), but it cannot see through dynamic dispatch (eval, an aliased
// wrapper) — such a hook FAILs the check, the safe direction.
func hasActiveSealCall(body string) bool {
	data := []byte(body)
	lines := textscan.SplitKeepEnds(data)
	mask := textscan.HeredocMask(data)
	for i, raw := range lines {
		if mask[i] {
			continue // heredoc body — opaque, never a command position
		}
		code := stripInlineComment(textscan.StripTerminator(raw))
		if strings.TrimSpace(code) == "" {
			continue
		}
		if sealInvocation.MatchString(code) {
			return true
		}
	}
	return false
}

// stripInlineComment drops a shell comment from a line: everything from a '#'
// that starts the line or follows a word-break character. Shell begins a
// comment wherever a word could begin, so `;`, `&`, `|`, and `(` start one
// just as whitespace does (`cmd;# note`). It does not track quoting, so a '#'
// inside a string literal is also cut — acceptable for this lexical check,
// which errs toward FAIL.
func stripInlineComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			continue
		}
		if i == 0 {
			return line[:i]
		}
		switch line[i-1] {
		case ' ', '\t', ';', '&', '|', '(':
			return line[:i]
		}
	}
	return line
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

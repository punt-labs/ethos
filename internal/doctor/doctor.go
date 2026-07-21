// Package doctor provides shared health-check logic for the ethos CLI
// and MCP server.
package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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

// CheckSealHook verifies the current repo's pre-commit hook carries an
// active DES-058 audit-seal invocation — either the ethos marker section
// chained into a host hook or the standalone ethos hook. The seal is the
// live audit write path's primary trigger; a repo missing it commits work
// without sealing the pending audit lines that document it.
func CheckSealHook(repoRoot string) Result {
	name := "Audit seal hook"
	const remedy = " — re-run install.sh from the repo root"

	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	hook := filepath.Join(gitHooksDir(repoRoot), "pre-commit")
	info, err := os.Stat(hook)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Name: name, Status: "FAIL", Detail: "no pre-commit hook (missing)" + remedy}
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot stat %s: %v%s", hook, err, remedy)}
	}
	data, err := os.ReadFile(hook)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot read %s: %v%s", hook, err, remedy)}
	}

	body := string(data)
	// Require an actual seal invocation, not the substring: a commented-out
	// call, a string-literal mention (echo/printf), or a dead branch must not
	// read as active, or the silent-absence state recurs behind a green check.
	if hasActiveSealCall(body) {
		// A shell section pasted into a non-shell hook (Python/Node) can never
		// run as sh, so the call text is meaningless there.
		if !isShellHook(body) {
			return Result{Name: name, Status: "FAIL", Detail: "seal call present but the hook's shebang is not a shell — git runs it under another interpreter" + remedy}
		}
		// Git skips a hook without the executable bit, so a valid-looking
		// but non-executable hook never fires.
		if info.Mode().Perm()&0o111 == 0 {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("seal hook present but not executable — run: chmod +x %s", hook)}
		}
		if strings.Contains(body, "# --- BEGIN ETHOS DES-058 SEAL") {
			return Result{Name: name, Status: "PASS", Detail: "chained seal section active"}
		}
		return Result{Name: name, Status: "PASS", Detail: "standalone seal hook active"}
	}
	if strings.Contains(body, "DES-058") {
		return Result{Name: name, Status: "FAIL", Detail: "seal section present but no active 'audit seal' call (stale)" + remedy}
	}
	return Result{Name: name, Status: "FAIL", Detail: "seal hook not installed (missing)" + remedy}
}

// sealInvocation matches an `audit seal` call in command position: the ethos
// binary (bare `ethos` or the hook's "$ethos_bin" variable) followed by
// `audit seal`, at a line/statement boundary. It deliberately rejects a
// string-literal mention like `echo "audit seal"`, whose `audit seal` is not
// preceded by the ethos command token.
var sealInvocation = regexp.MustCompile(`(^|[\s;&|(!])("?\$\{?ethos_bin\}?"?|ethos)[\t ]+audit[\t ]+seal([\s;&|)]|$)`)

// hasActiveSealCall reports whether body invokes `ethos audit seal` on a
// non-comment line. The check is lexical, not semantic: it drops full-line and
// inline comments so a call named in a comment cannot pass, but it cannot see
// through dynamic dispatch (eval, an aliased wrapper) — such a hook FAILs the
// check, the safe direction.
func hasActiveSealCall(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		code := stripInlineComment(line)
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
// that starts the line or follows whitespace. It does not track quoting, so a
// '#' inside a string literal is also cut — acceptable for this lexical check,
// which errs toward FAIL.
func stripInlineComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}

// isShellHook reports whether the hook body's shebang names a shell-family
// interpreter, or there is no shebang (git runs such a hook via sh). A
// non-shell shebang (Python/Node/binary) means a pasted shell seal call
// cannot run.
func isShellHook(body string) bool {
	first := body
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		first = body[:nl]
	}
	first = strings.TrimRight(first, "\r")
	if !strings.HasPrefix(first, "#!") {
		return true
	}
	fields := strings.Fields(first[2:])
	if len(fields) == 0 {
		return true
	}
	interp := path.Base(fields[0])
	if interp == "env" && len(fields) > 1 {
		interp = path.Base(fields[1])
	}
	switch interp {
	case "sh", "bash", "dash", "ksh", "zsh", "mksh", "ash":
		return true
	}
	return false
}

// gitHooksDir returns the hooks directory git runs for the repo at repoRoot.
// It asks git directly (`git rev-parse --git-path hooks`), which is the one
// source of truth the installer also uses: git honors core.hooksPath and
// resolves a worktree's commondir. When git is unavailable it falls back to
// resolving the ".git" gitdir file and its "commondir" by hand, so a worktree
// still lands on the common ".git/hooks" git actually runs (ethos-2ol1).
func gitHooksDir(repoRoot string) string {
	if p := gitHooksPath(repoRoot); p != "" {
		return p
	}
	dot := filepath.Join(repoRoot, ".git")
	gd := dot
	if info, err := os.Stat(dot); err != nil || !info.IsDir() {
		if data, err := os.ReadFile(dot); err == nil {
			line := strings.TrimSpace(string(data))
			if target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:")); target != line && target != "" {
				gd = target
				if !filepath.IsAbs(gd) {
					gd = filepath.Join(repoRoot, gd)
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(gd, "commondir")); err == nil {
		if common := strings.TrimSpace(string(data)); common != "" {
			if !filepath.IsAbs(common) {
				common = filepath.Join(gd, common)
			}
			gd = common
		}
	}
	return filepath.Join(gd, "hooks")
}

// gitHooksPath returns git's own resolution of the hooks directory, or "" if
// git is unavailable or repoRoot is not itself a git work tree root. The
// work-tree-root anchor matters: without it, git would walk up from a
// non-repo repoRoot and resolve an ancestor repo's hooks — reporting on the
// wrong repository. A relative result is resolved against repoRoot, matching
// git's `-C` semantics.
func gitHooksPath(repoRoot string) string {
	top, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--show-toplevel").Output()
	if err != nil || !samePath(strings.TrimSpace(string(top)), repoRoot) {
		return ""
	}
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--git-path", "hooks").Output()
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoRoot, p)
	}
	return p
}

// samePath reports whether a and b name the same location, tolerating the
// symlinked temp roots (macOS /tmp → /private/tmp) that show up in tests.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
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

package enable

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// gitignoreMarker heads the block enable appends so the runtime patterns are
// identifiable as ethos-managed.
const gitignoreMarker = "# ethos runtime (never track) — DES-058 live zone + mission locks"

// gitignorePatterns are the runtime zones a consumer repo must never track: the
// DES-058 live-session files (audit + lock) and the mission runtime locks. The
// seal hook rewrites these continuously while a session is live, so tracking
// them deadlocks git checkout/pull and leaks them into release PRs.
var gitignorePatterns = []string{
	".punt-labs/**/local/**",
	".punt-labs/ethos/missions/**/*.lock",
}

// ensureGitignore makes the repo .gitignore cover ethos's runtime zones. It
// appends a marked block with only the patterns not already present, so
// re-enable adds nothing (idempotent) and the operator's existing .gitignore is
// never rewritten or reordered — the block is appended, existing lines are left
// untouched. A missing .gitignore is created. It returns the step action
// ("added" or "already") and a detail line for the report.
func ensureGitignore(repoRoot string) (action, detail string, err error) {
	path := filepath.Join(repoRoot, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", "", fmt.Errorf("reading .gitignore: %w", err)
	}

	present := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, p := range gitignorePatterns {
		if !present[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return "already", ".gitignore already ignores ethos runtime zones", nil
	}

	var buf strings.Builder
	buf.Write(data)
	// Separate the appended block from existing content with a blank line, and
	// terminate an existing final line that lacks its newline.
	if len(data) > 0 {
		if !strings.HasSuffix(string(data), "\n") {
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(gitignoreMarker)
	buf.WriteByte('\n')
	buf.WriteString(strings.Join(missing, "\n"))
	buf.WriteByte('\n')

	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		return "", "", fmt.Errorf("writing .gitignore: %w", err)
	}
	return "added", "ignored " + strings.Join(missing, ", "), nil
}

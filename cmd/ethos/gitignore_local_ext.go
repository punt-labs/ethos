package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/doctor"
)

// localExtIgnoreMarker heads the block this emits. It is deliberately
// separate from the block `ethos enable` writes: that one covers runtime
// files that churn and must never be committed for mechanical reasons,
// this one covers the half of an extension namespace that may hold
// secrets. Folding them together would mislabel one of them for whoever
// reads the .gitignore next.
const localExtIgnoreMarker = "# ethos: the .local half of an extension namespace — may hold secrets, never track"

// ensureLocalExtIgnored adds DES-057 Part C's git-exclusion rule to the
// repo's .gitignore if it is not already covered, and reports whether it
// wrote anything.
//
// The rule is the boundary. `.local.yaml` is a git-exclusion mechanism,
// not a vault: the merged view still reaches the model at runtime. What
// it guarantees is that the file never enters git — and only if the rule
// is there before the first `ethos ext set --local`.
//
// Matching is exact on a trimmed line, so an existing rule counts and a
// re-run adds nothing. It never rewrites or reorders existing lines.
func ensureLocalExtIgnored(repoRoot string) (added bool, err error) {
	if repoRoot == "" {
		return false, nil
	}
	path := filepath.Join(repoRoot, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimRight(line, "\r") == doctor.GitignoreRule {
			return false, nil
		}
	}

	var b strings.Builder
	b.Write(data)
	if len(data) > 0 {
		if !strings.HasSuffix(string(data), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(localExtIgnoreMarker + "\n")
	b.WriteString(doctor.GitignoreRule + "\n")

	mode := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(b.String()), mode); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

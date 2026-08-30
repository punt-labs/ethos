package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/enable"
)

// ensureLocalExtIgnored adds DES-057 Part C's git-exclusion rule to the
// repo's .gitignore if the repo does not already exclude machine-local
// files, and reports whether it wrote anything.
//
// The rule is the boundary. `.local.yaml` is a git-exclusion mechanism,
// not a vault: the merged view still reaches the model at runtime. What
// it guarantees is that the file never enters git — and only if the rule
// is there before the first `ethos ext set --local`.
//
// Coverage is enable.LocalIgnored — git's own answer for a representative
// `.local` path — so any rule that already excludes such a file counts
// and a re-run adds nothing. It never rewrites or reorders existing
// lines. The note above the rule is deliberately not the marker `ethos
// enable` uses for the runtime zones: those churn and must stay untracked
// for mechanical reasons, this one may hold secrets, and one label for
// both would mislead whoever reads the .gitignore next.
func ensureLocalExtIgnored(repoRoot string) (added bool, err error) {
	if repoRoot == "" {
		return false, nil
	}
	covered, err := enable.LocalIgnored(repoRoot)
	if err != nil {
		return false, err
	}
	if covered {
		return false, nil
	}

	path := filepath.Join(repoRoot, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}

	var b strings.Builder
	b.Write(data)
	if len(data) > 0 {
		if !strings.HasSuffix(string(data), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(enable.LocalIgnoreNote + "\n")
	b.WriteString(enable.LocalIgnoreRule + "\n")

	mode := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(b.String()), mode); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

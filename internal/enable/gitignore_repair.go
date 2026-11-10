package enable

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// RepairGitignore converts the repository .gitignore at repoRoot to the single
// shared-zone rule: it retires the exact block earlier releases wrote and
// ensures LocalIgnoreRule is present and honoured by git. Lines outside that
// block are never touched. Unlike enable it does not refuse while old paths
// exist: the caller has already moved or deleted that data, and the shared-zone
// rule is the boundary that now covers what remains. It reports whether the
// file was written, even when confirming the result then failed.
func RepairGitignore(repoRoot string, p layout.Policy) (changed bool, kept []KeptRule, err error) {
	return repairGitignoreWithIO(repoRoot, p, defaultGitignoreIO())
}

// KeptRule is a legacy ignore pattern RepairGitignore left in the file because
// the file at Path, outside the shared machine-local zone, still depends on it.
// Retiring it would make that file, which may hold secrets, stageable.
type KeptRule struct {
	Pattern string
	Path    string
}

func repairGitignoreWithIO(repoRoot string, p layout.Policy, io gitignoreIO) (bool, []KeptRule, error) {
	path := filepath.Join(repoRoot, ".gitignore")
	original, err := readIgnoreSource(repoRoot, ".gitignore")
	if err != nil && !os.IsNotExist(err) {
		return false, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	_, retiring := removeLegacyManagedRules(original)
	keep := make(map[string]bool)
	var kept []KeptRule
	for _, pattern := range retiring {
		if keep[pattern] {
			continue
		}
		deps, err := ignoreDependents(repoRoot, []string{pattern})
		if err != nil {
			return false, nil, err
		}
		if len(deps) > 0 {
			keep[pattern] = true
			kept = append(kept, KeptRule{Pattern: pattern, Path: deps[0]})
		}
	}
	data, _ := retireLegacyManagedRules(original, keep)
	if !presentLines(data)[LocalIgnoreRule] {
		data = insertLocalRule(data)
	}
	if bytes.Equal(original, data) {
		return false, kept, verifyLocalProtection(repoRoot, p, io)
	}
	if err := writeGitignoreWithIO(path, data, p, io); err != nil {
		return false, kept, err
	}
	return true, kept, verifyLocalProtection(repoRoot, p, io)
}

// insertLocalRule adds LocalIgnoreRule under the ethos marker when the file
// has one, and in a new marked block at the end otherwise, keeping the file's
// line ending.
func insertLocalRule(data []byte) []byte {
	eol := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "\r\n"
	}
	lines := strings.SplitAfter(string(data), "\n")
	for i, line := range lines {
		if strings.TrimRight(line, "\r\n") != gitignoreMarker {
			continue
		}
		head := strings.Join(lines[:i+1], "")
		if !strings.HasSuffix(head, "\n") {
			head += eol
		}
		return []byte(head + LocalIgnoreRule + eol + strings.Join(lines[i+1:], ""))
	}
	var out strings.Builder
	out.Write(data)
	if len(data) > 0 {
		if !bytes.HasSuffix(data, []byte("\n")) {
			out.WriteString(eol)
		}
		out.WriteString(eol)
	}
	out.WriteString(LocalIgnoreNote + eol + LocalIgnoreRule + eol)
	return []byte(out.String())
}

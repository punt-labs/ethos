//go:build linux || darwin

package test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreCommitSmokeSelectionsMatchTests(t *testing.T) {
	data, err := os.ReadFile("../scripts/test-smoke.tsv")
	require.NoError(t, err)
	var packages []string
	selections := make(map[string][]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		pkg, names, ok := strings.Cut(line, "\t")
		require.True(t, ok, "invalid smoke selection %q", line)
		require.NotContains(t, selections, pkg)
		selections[pkg] = strings.Split(names, "|")
		packages = append(packages, pkg)
	}
	args := append([]string{"list", "-race", "-json"}, packages...)
	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Dir = ".."
	out, err := cmd.Output()
	require.NoError(t, err)
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var pkg struct {
			Dir                       string
			TestGoFiles, XTestGoFiles []string
		}
		require.NoError(t, dec.Decode(&pkg))
		rel, err := filepath.Rel(root, pkg.Dir)
		require.NoError(t, err)
		key := "./" + filepath.ToSlash(rel)
		require.Contains(t, selections, key)
		names := make([]string, 0)
		fset := token.NewFileSet()
		for _, file := range append(pkg.TestGoFiles, pkg.XTestGoFiles...) {
			parsed, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, file), nil, parser.SkipObjectResolution)
			require.NoError(t, err)
			for _, decl := range parsed.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
					names = append(names, fn.Name.Name)
				}
			}
		}
		for _, pattern := range selections[key] {
			matcher, err := regexp.Compile("^Test(" + pattern + ")$")
			require.NoError(t, err)
			require.True(t, slices.ContainsFunc(names, matcher.MatchString), "%s selector %q matches no tests", key, pattern)
		}
		delete(selections, key)
	}
	require.Empty(t, selections)
}

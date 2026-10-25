package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Without git, the hand rules stop at GIT_CEILING_DIRECTORIES exactly where
// git does: a ceiling that is a proper ancestor of the (physical) working
// directory is never examined, nor is anything above it; an entry equal to
// the working directory, a relative entry and an unresolvable entry are
// ignored; entries are resolved to real paths unless an empty entry came
// before them. Each case is checked against git itself.
func TestHandRulesHonourCeilingLikeGit(t *testing.T) {
	requireGit(t)
	cases := map[string]struct {
		ceiling func(base, outer, start, link string) string
		start   func(outer, link string) string
	}{
		"ceiling below the repository":     {ceiling: func(_, outer, _, _ string) string { return filepath.Join(outer, "a") }},
		"ceiling at the repository":        {ceiling: func(_, outer, _, _ string) string { return outer }},
		"ceiling above the repository":     {ceiling: func(base, _, _, _ string) string { return base }},
		"ceiling equal to the start":       {ceiling: func(_, _, start, _ string) string { return start }},
		"trailing slash":                   {ceiling: func(_, outer, _, _ string) string { return outer + "/" }},
		"dot-dot in the entry":             {ceiling: func(_, outer, _, _ string) string { return filepath.Join(outer, "a") + "/.." }},
		"symlink entry is resolved":        {ceiling: func(_, _, _, link string) string { return link }},
		"after an empty entry, kept as is": {ceiling: func(_, _, _, link string) string { return ":" + link }},
		"relative entry ignored":           {ceiling: func(_, _, _, _ string) string { return "outer" }},
		"nonexistent entry ignored":        {ceiling: func(base, _, _, _ string) string { return filepath.Join(base, "nope") }},
		"root ceiling":                     {ceiling: func(_, _, _, _ string) string { return "/" }},
		"several entries, longest wins": {ceiling: func(base, outer, _, _ string) string {
			return base + string(os.PathListSeparator) + filepath.Join(outer, "a")
		}},
		"start through a symlink": {
			ceiling: func(_, outer, _, _ string) string { return outer },
			start:   func(_, link string) string { return filepath.Join(link, "a", "b") },
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			outer := filepath.Join(base, "outer")
			makeGitDirAt(t, filepath.Join(outer, ".git"))
			start := filepath.Join(outer, "a", "b")
			require.NoError(t, os.MkdirAll(start, 0o755))
			link := filepath.Join(base, "link")
			if err := os.Symlink(outer, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if c.start != nil {
				start = c.start(outer, link)
			}
			t.Setenv("GIT_CEILING_DIRECTORIES", c.ceiling(base, outer, start, link))
			t.Chdir(start)

			want, gitErr := gitTopLevel(t, start)
			hideGit(t)
			var got string
			var err error
			captureStderr(t, func() { got, err = locateRepoRoot() })
			if gitErr != nil {
				assert.ErrorIs(t, err, ErrNotInRepo, "git stops at the ceiling, so must the hand rules (got %q)", got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, realpath(t, want), realpath(t, got))
		})
	}
}

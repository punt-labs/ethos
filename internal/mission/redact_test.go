package mission

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPathRedactorText covers the substitution rules the audit lines
// and the delegation records share: HOME to ~, repoRoot to <repo>,
// repo winning when it is nested inside home, and pass-through for a
// path under neither prefix.
func TestPathRedactorText(t *testing.T) {
	const (
		home = "/Users/jfreeman"
		repo = "/Users/jfreeman/Coding/punt-labs/ethos"
	)

	tests := []struct {
		name string
		r    PathRedactor
		in   string
		want string
	}{
		{
			name: "home becomes tilde",
			r:    PathRedactor{Home: home},
			in:   "/Users/jfreeman/.claude/settings.json",
			want: "~/.claude/settings.json",
		},
		{
			name: "repo becomes token",
			r:    PathRedactor{Repo: repo},
			in:   "/Users/jfreeman/Coding/punt-labs/ethos/internal/mission/redact.go",
			want: "<repo>/internal/mission/redact.go",
		},
		{
			name: "repo wins over home when nested",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   "/Users/jfreeman/Coding/punt-labs/ethos/Makefile",
			want: "<repo>/Makefile",
		},
		{
			name: "every occurrence in one string",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   "cp /Users/jfreeman/a.txt /Users/jfreeman/Coding/punt-labs/ethos/b.txt",
			want: "cp ~/a.txt <repo>/b.txt",
		},
		{
			name: "bare root with no trailing slash",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   "cd /Users/jfreeman/Coding/punt-labs/ethos",
			want: "cd <repo>",
		},
		{
			name: "path under neither prefix passes through",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   "/etc/hosts",
			want: "/etc/hosts",
		},
		{
			name: "empty prefixes disable substitution",
			r:    PathRedactor{},
			in:   "/Users/jfreeman/a.txt",
			want: "/Users/jfreeman/a.txt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.r.Text(tt.in))
		})
	}
}

// TestPathRedactorValue asserts the recursion reaches strings nested
// under maps and slices, leaves non-string scalars alone, and does not
// mutate the input.
func TestPathRedactorValue(t *testing.T) {
	r := PathRedactor{Home: "/Users/jfreeman"}

	t.Run("recurses through maps and slices", func(t *testing.T) {
		in := map[string]any{
			"prompt": "see /Users/jfreeman/notes.md",
			"meta": map[string]any{
				"cwd": "/Users/jfreeman/Coding",
			},
			"args":  []any{"/Users/jfreeman/x", 7, true},
			"count": 3,
		}
		got := r.Map(in)
		assert.Equal(t, "see ~/notes.md", got["prompt"])
		assert.Equal(t, "~/Coding", got["meta"].(map[string]any)["cwd"])
		assert.Equal(t, []any{"~/x", 7, true}, got["args"])
		assert.Equal(t, 3, got["count"])
	})

	t.Run("input is not mutated", func(t *testing.T) {
		in := map[string]any{"file_path": "/Users/jfreeman/a.txt"}
		_ = r.Map(in)
		assert.Equal(t, "/Users/jfreeman/a.txt", in["file_path"])
	})

	t.Run("nil map stays nil", func(t *testing.T) {
		assert.Nil(t, r.Map(nil))
	})

	t.Run("nil body stays nil", func(t *testing.T) {
		assert.Nil(t, r.Body(nil))
	})
}

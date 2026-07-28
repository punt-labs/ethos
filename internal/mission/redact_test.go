package mission

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestNewPathRedactor covers the usability guard on the home prefix.
// os.UserHomeDir hands back whatever HOME holds without checking it,
// and returns "/" rather than an error on ios — a prefix of "/" would
// rewrite the leading separator of every absolute path, so it is
// refused rather than used.
func TestNewPathRedactor(t *testing.T) {
	homes := []struct {
		name    string
		home    string
		wantErr string
	}{
		{name: "an absolute home is usable", home: "/Users/jfreeman"},
		{name: "empty home is refused", home: "", wantErr: "home directory"},
		{name: "root home is refused", home: "/", wantErr: "unusable"},
		{name: "root with trailing slashes is refused", home: "//", wantErr: "unusable"},
		{name: "relative home is refused", home: "relative/path", wantErr: "unusable"},
	}
	for _, tt := range homes {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", tt.home)
			r, err := NewPathRedactor("/repo")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Equal(t, PathRedactor{}, r,
					"a refused prefix must not yield a partially-usable redactor")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.home, r.Home)
			assert.Equal(t, "/repo", r.Repo)
		})
	}

	// repoRoot is optional, so an unusable one disables the <repo>
	// token rather than failing the write. The home substitution, which
	// is what carries the username, still applies.
	repos := []struct {
		name string
		repo string
		want string
	}{
		{name: "absolute repo is kept", repo: "/w/ethos", want: "/w/ethos"},
		{name: "trailing slash is normalized", repo: "/w/ethos/", want: "/w/ethos"},
		{name: "redundant elements are normalized", repo: "/w/x/../ethos", want: "/w/ethos"},
		{name: "empty repo disables the token", repo: "", want: ""},
		{name: "root repo disables the token", repo: "/", want: ""},
		{name: "relative repo disables the token", repo: "ethos", want: ""},
	}
	for _, tt := range repos {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", "/Users/jfreeman")
			r, err := NewPathRedactor(tt.repo)
			require.NoError(t, err)
			assert.Equal(t, tt.want, r.Repo)
			assert.Equal(t, "/Users/jfreeman", r.Home,
				"an unusable repoRoot must not cost the home substitution")
		})
	}

	t.Run("a normalized repo prefix still matches", func(t *testing.T) {
		t.Setenv("HOME", "/Users/jfreeman")
		r, err := NewPathRedactor("/Users/jfreeman/Coding/ethos/")
		require.NoError(t, err)
		assert.Equal(t, "<repo>/Makefile",
			r.Text("/Users/jfreeman/Coding/ethos/Makefile"),
			"a trailing slash on repoRoot must not silently defeat the token")
	})
}

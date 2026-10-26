package mission

import (
	"strings"
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
		home = "/Users/user"
		repo = "/Users/user/Coding/punt-labs/ethos"
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
			in:   "/Users/user/.claude/settings.json",
			want: "~/.claude/settings.json",
		},
		{
			name: "repo becomes token",
			r:    PathRedactor{Repo: repo},
			in:   "/Users/user/Coding/punt-labs/ethos/internal/mission/redact.go",
			want: "<repo>/internal/mission/redact.go",
		},
		{
			name: "repo wins over home when nested",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   "/Users/user/Coding/punt-labs/ethos/Makefile",
			want: "<repo>/Makefile",
		},
		{
			name: "every occurrence in one string",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   "cp /Users/user/a.txt /Users/user/Coding/punt-labs/ethos/b.txt",
			want: "cp ~/a.txt <repo>/b.txt",
		},
		{
			name: "bare root with no trailing slash",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   "cd /Users/user/Coding/punt-labs/ethos",
			want: "cd <repo>",
		},
		{
			name: "path under neither prefix passes through",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   "/etc/hosts",
			want: "/etc/hosts",
		},
		{
			name: "a sibling sharing the repo prefix is not corrupted",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   "/Users/user/Coding/punt-labs/ethos-docs/README.md",
			want: "~/Coding/punt-labs/ethos-docs/README.md",
		},
		{
			name: "a sibling sharing the home prefix keeps its own name",
			r:    PathRedactor{Home: home},
			in:   "/Users/user2/notes.md",
			want: "/Users/user2/notes.md",
		},
		{
			name: "a repo path ending a sentence is still redacted",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   "fix /Users/user/Coding/punt-labs/ethos.",
			want: "fix <repo>.",
		},
		{
			name: "a quoted repo path is redacted",
			r:    PathRedactor{Home: home, Repo: repo},
			in:   `cd "/Users/user/Coding/punt-labs/ethos" && make`,
			want: `cd "<repo>" && make`,
		},
		{
			name: "one string mixing a match and a near-match",
			r:    PathRedactor{Repo: "/w/repo"},
			in:   "cp /w/repo2/a.txt /w/repo/b.txt",
			want: "cp /w/repo2/a.txt <repo>/b.txt",
		},
		{
			name: "empty prefixes disable substitution",
			r:    PathRedactor{},
			in:   "/Users/user/a.txt",
			want: "/Users/user/a.txt",
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
	r := PathRedactor{Home: "/Users/user"}

	t.Run("recurses through maps and slices", func(t *testing.T) {
		in := map[string]any{
			"prompt": "see /Users/user/notes.md",
			"meta": map[string]any{
				"cwd": "/Users/user/Coding",
			},
			"args":  []any{"/Users/user/x", 7, true},
			"count": 3,
		}
		got := r.Map(in)
		assert.Equal(t, "see ~/notes.md", got["prompt"])
		assert.Equal(t, "~/Coding", got["meta"].(map[string]any)["cwd"])
		assert.Equal(t, []any{"~/x", 7, true}, got["args"])
		assert.Equal(t, 3, got["count"])
	})

	t.Run("input is not mutated", func(t *testing.T) {
		in := map[string]any{"file_path": "/Users/user/a.txt"}
		_ = r.Map(in)
		assert.Equal(t, "/Users/user/a.txt", in["file_path"])
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
		{name: "an absolute home is usable", home: "/Users/user"},
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
			t.Setenv("HOME", "/Users/user")
			r, err := NewPathRedactor(tt.repo)
			require.NoError(t, err)
			assert.Equal(t, tt.want, r.Repo)
			assert.Equal(t, "/Users/user", r.Home,
				"an unusable repoRoot must not cost the home substitution")
		})
	}

	t.Run("a normalized repo prefix still matches", func(t *testing.T) {
		t.Setenv("HOME", "/Users/user")
		r, err := NewPathRedactor("/Users/user/Coding/ethos/")
		require.NoError(t, err)
		assert.Equal(t, "<repo>/Makefile",
			r.Text("/Users/user/Coding/ethos/Makefile"),
			"a trailing slash on repoRoot must not silently defeat the token")
	})
}

func TestPathRedactor_DashEncodedHome(t *testing.T) {
	r := PathRedactor{Home: "/var/home/zed", Repo: "/var/home/zed/work/ethos"}
	tests := []struct{ in, want string }{
		{"~/.claude/projects/-var-home-zed-work-ethos/memory/MEMORY.md", "~/.claude/projects/-~-work-ethos/memory/MEMORY.md"},
		{"/p/-var-home-zed", "/p/-~"},
		{"/p/-var-home-zedd-x", "/p/-var-home-zedd-x"},
		{"/var/home/zed/x", "~/x"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, r.Text(tt.in), tt.in)
	}
	// The generic slug form needs no configured home.
	assert.Equal(t, "/p/-~-[redacted-slug]", r.Content("/p/-home-jfreeman-Coding"))
}

// TestPathRedactor_Path: a path-tagged value has the current repo root, home
// and dash-encoded home rewritten at every path boundary, never inside a
// relative segment. Another user's home gets a token that cannot expand.
func TestPathRedactor_Path(t *testing.T) {
	r := PathRedactor{Home: "/home/alice", Repo: "/home/alice/work/repo"}
	tests := []struct{ name, in, want string }{
		{"relative entry holding the home text", "testdata/home/alice/config.yaml", "testdata/home/alice/config.yaml"},
		{"relative entry holding the repo text", "x/home/alice/work/repo/a.go", "x/home/alice/work/repo/a.go"},
		{"absolute under home", "/home/alice/x", "~/x"},
		{"absolute under repo", "/home/alice/work/repo/a.go", "<repo>/a.go"},
		{"repo itself", "/home/alice/work/repo", "<repo>"},
		{"sibling of repo is not the repo", "/home/alice/work/repo2/a.go", "~/work/repo2/a.go"},
		{"foreign home", "/home/bob/x", "~[redacted-user]/x"},
		{"foreign macOS home", "/Users/bob/x", "~[redacted-user]/x"},
		{"mid-string foreign home", "testdata/home/bob/x", "testdata/home/bob/x"},
		{"no path", "internal/mission/", "internal/mission/"},
		{"grant", "Bash(gh auth token:*)", "Bash(gh auth token:*)"},
		{"grant home", "Read(/home/alice/x)", "Read(~/x)"},
		{"permission rule home", "Read(//home/alice/x)", "Read(/~/x)"},
		{"permission rule repo", "Edit(//home/alice/work/repo/x/**)", "Edit(/<repo>/x/**)"},
		{"permission rule foreign home", "Read(//home/bob/x)", "Read(/~[redacted-user]/x)"},
		{"grant naming the repo root", "Read(/home/alice/work/repo)", "Read(<repo>)"},
		{"grant with relative path", "Read(testdata/home/alice/x)", "Read(testdata/home/alice/x)"},
		{"path after a space", "Bash(cd /home/alice/x && make check)", "Bash(cd ~/x && make check)"},
		{"foreign path after a space", "Bash(cd /home/bob/x && make check)", "Bash(cd ~[redacted-user]/x && make check)"},
		{"path after =", "Bash(FOO=/home/alice/x make)", "Bash(FOO=~/x make)"},
		{"slug", "/home/alice/.claude/projects/-home-alice-Coding-x/memory/MEMORY.md", "~/.claude/projects/-~-Coding-x/memory/MEMORY.md"},
		{"relative slug path", "~/.claude/projects/-home-alice-Coding-x/m.md", "~/.claude/projects/-~-Coding-x/m.md"},
		{"foreign slug", "~/.claude/projects/-home-bob-Coding-x/m.md", "~/.claude/projects/-~[redacted-user]-/m.md"},
		{"slug text inside a name", "a-home-alice-b", "a-home-alice-b"},
		{"run of slashes", "///home/alice/x", "//~/x"},
		{"scheme", "file:///home/alice/x", "file://~/x"},
		{"foreign run of slashes", "///home/bob/.ssh/id", "//~[redacted-user]/.ssh/id"},
		{"own home before a comma", "/home/alice,bak", "~,bak"},
		{"own home before a bracket", "[/home/alice]", "[~]"},
		{"home glued to a flag is left for the writer to refuse", "-I/home/bob/inc", "-I/home/bob/inc"},
		{"home dir inside the repo", "/home/alice/work/repo/home/alice/x", "<repo>/home/alice/x"},
		{"users dir inside the home", "/home/alice/Users/bob/x", "~/Users/bob/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, r.Path(tt.in))
		})
	}
}

// TestPathRedactor_ExpandReversesPath: the local placeholders expand back to
// the local values, so a stored entry compares equal to what a tool recorded.
func TestPathRedactor_ExpandReversesPath(t *testing.T) {
	r := PathRedactor{Home: "/home/alice", Repo: "/home/alice/work/repo"}
	for _, in := range []string{
		"/home/alice/x",
		"/home/alice/work/repo/a.go",
		"/home/alice/work/repo",
		"Read(//home/alice/x)",
		"Edit(//home/alice/work/repo/x/**)",
		"Bash(cd /home/alice/x && make check)",
		"/home/alice/.claude/projects/-home-alice-Coding-x/memory/MEMORY.md",
		"testdata/home/alice/x",
	} {
		got, err := r.Expand(r.Path(in))
		require.NoError(t, err, in)
		assert.Equal(t, in, got)
	}

	foreign := r.Path("/home/bob/x")
	got, err := r.Expand(foreign)
	require.NoError(t, err)
	assert.Equal(t, foreign, got, "another user's home never expands")
	assert.True(t, HasRedactedUser(foreign))
	assert.False(t, HasRedactedUser("~/x"))
}

func TestPathRedactor_ExpandNeedsItsRoot(t *testing.T) {
	_, err := PathRedactor{Home: "/home/alice"}.Expand("<repo>/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo")
	_, err = PathRedactor{Repo: "/r"}.Expand("~/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "home")
	got, err := PathRedactor{}.Expand("cfg/x")
	require.NoError(t, err)
	assert.Equal(t, "cfg/x", got)
}

func TestExpandPath_HomeUnknownIsAnError(t *testing.T) {
	t.Setenv("HOME", "")
	_, err := ExpandPath("~/x", "/r")
	require.Error(t, err)
	got, err := ExpandPath("<repo>/x", "/r")
	require.NoError(t, err, "no home is needed when the value has no ~")
	assert.Equal(t, "/r/x", got)
}

// TestRedactRecord_PathFieldsLeakNoHome: the inputs that leaked the username
// when only a prefix was rewritten.
func TestRedactRecord_PathFieldsLeakNoHome(t *testing.T) {
	const home = "/home/zed"
	t.Setenv("HOME", home)
	c := Contract{Tools: []string{
		"Bash(cd /home/zed/x && make check)",
		"/home/zed/.claude/projects/-home-zed-Coding/memory/MEMORY.md",
		"Read(/home/zed)",
	}}
	got, err := RedactRecord(t.TempDir(), c)
	require.NoError(t, err)
	for _, tool := range got.Tools {
		assert.NotContains(t, tool, "zed", tool)
	}
	assert.Equal(t, "Read(~)", got.Tools[2])
}

// TestRedactRecord_RefusesAResidualHome: a home that the pass cannot place at
// a path boundary is refused rather than written.
func TestRedactRecord_RefusesAResidualHome(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	c := Contract{Tools: []string{"Bash(gcc -I/home/zed/inc)"}}
	_, err := RedactRecord(t.TempDir(), c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Tools[0]")
	assert.NotContains(t, err.Error(), "zed", "the error must not repeat the home")
}

// TestRedactRecord_RefusesARelativeEntryHoldingAHome: Path leaves a relative
// entry alone, because rewriting it would change what it authorizes. The
// writer's broad check refuses it, because it also reads as a home path. The
// error names the field and never the value.
func TestRedactRecord_RefusesARelativeEntryHoldingAHome(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	// A path is a home only where it begins: a relative home/ directory is a
	// directory like any other, and a write set naming one is stored as is.
	out, err := RedactRecord(t.TempDir(), Contract{WriteSet: []string{"app/home/page.tsx", "testdata/home/bob/config.yaml"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"app/home/page.tsx", "testdata/home/bob/config.yaml"}, out.WriteSet)

	_, err = RedactRecord(t.TempDir(), Contract{WriteSet: []string{"-I/home/zed/config"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WriteSet[0]")
	assert.NotContains(t, err.Error(), "zed")
}

// TestRedactRecord_RefusesWhatThePassesMissed: values the boundary rewrite
// cannot place, in a path field and in prose, are refused.
func TestRedactRecord_RefusesWhatThePassesMissed(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, in := range []string{
		"%2Fhome%2Fbob%2Fx",
	} {
		_, err := RedactRecord(t.TempDir(), Contract{Tools: []string{in}})
		require.Error(t, err, in)
		assert.Contains(t, err.Error(), "Tools[0]")
		assert.NotContains(t, err.Error(), "zed")
		assert.NotContains(t, err.Error(), "bob")

		if in != "%2Fhome%2Fbob%2Fx" {
			continue // prose rewrites the others; only a path field is left alone
		}
		_, err = RedactRecord(t.TempDir(), Contract{Context: "see " + in})
		require.Error(t, err, in)
		assert.Contains(t, err.Error(), "Context")
	}
}

// TestPathRedactor_PathIsIdempotent: a second pass over what the first wrote
// changes nothing, so a store rewrite or a scrub cannot corrupt an entry.
func TestPathRedactor_PathIsIdempotent(t *testing.T) {
	r := PathRedactor{Home: "/home/alice", Repo: "/home/alice/work/repo"}
	for _, in := range []string{
		"/home/alice/work/repo/home/alice/x",
		"/home/alice/Users/bob/x",
		"/home/alice//home/bob/x",
		"///home/alice/x",
		"file:///home/alice/x",
		"/home/bob/x",
		"Read(//home/bob/x)",
		"/home/alice,bak",
		"~/x",
		"<repo>/home/bob/x",
	} {
		once := r.Path(in)
		assert.Equal(t, once, r.Path(once), in)
	}
	assert.Equal(t, "<repo>/home/alice/x", r.Path("/home/alice/work/repo/home/alice/x"))
	assert.Equal(t, "~/Users/bob/x", r.Path("/home/alice/Users/bob/x"))
}

// TestPath_DashHomeSlugFollowedBySlashKeepsItsMeaning: -~ is a placeholder
// before any byte that ends a path, a slash included, so the slash does not
// open a new absolute path and Path never rewrites /home/zed after a slug into
// ~. The value stays one the leak check refuses, as at base, and what Expand
// returns is what went in.
func TestPath_DashHomeSlugFollowedBySlashKeepsItsMeaning(t *testing.T) {
	for _, c := range []struct{ home, in string }{
		{"/home/zed", "/srv/mirror/-home-zed/home/zed/notes.md"},
		{"/root", "/srv/mirror/-root/root/notes.md"},
		{"/home/zed", "/srv/mirror/-home-zed/home/bob/notes.md"},
		{"/home/zed", "x -home-zed/home/bob"},
	} {
		r := PathRedactor{Home: c.home, Repo: "/work/repo"}
		got := r.Path(c.in)
		back, err := r.Expand(got)
		require.NoError(t, err)
		assert.Equal(t, c.in, back, "Expand(Path(%q)) = %q via %q", c.in, back, got)
		assert.NotEmpty(t, r.LeakRule(got), "%q (from %q) must still be refused", got, c.in)
	}
}

// TestCreate_RefusesAnOwnHomeSlugBeforeAnAbsolutePath: the contract writer
// refuses the value, as at base, instead of storing a spelling that Expand
// reads back as a different path.
func TestCreate_RefusesAnOwnHomeSlugBeforeAnAbsolutePath(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-141")
	c.Inputs.Files = []string{"/srv/mirror/-home-zed/home/zed/notes.md"}
	assert.Error(t, s.Create(c))
}

// TestText_TwoDashHomesInARowAreAFixedPoint: the corpus case from fuzzing,
// -home-zed-home-zed under /home/zed.
func TestText_TwoDashHomesInARowAreAFixedPoint(t *testing.T) {
	r := PathRedactor{Home: "/home/zed", Repo: "/work/repo"}
	once := r.Text("-home-zed-home-zed")
	assert.Equal(t, once, r.Text(once))
	assert.NotContains(t, once, "zed")
}

// TestDashHome_BeforeAnyTerminator: the own-home slug becomes -~ before any
// terminator, as at base, and the result expands back to the input.
func TestDashHome_BeforeAnyTerminator(t *testing.T) {
	for _, home := range []string{"/root", "/home/u"} {
		r := PathRedactor{Home: home, Repo: "/work/repo"}
		dash := strings.ReplaceAll(home, "/", "-")
		for _, c := range []struct{ in, want string }{
			{home + "/.claude/projects/" + dash + "/abc.jsonl", "~/.claude/projects/-~/abc.jsonl"},
			{"(" + dash + ")", "(-~)"},
			{dash + ":", "-~:"},
			{"cd " + dash + " && ls", "cd -~ && ls"},
		} {
			got := r.Path(c.in)
			assert.Equal(t, c.want, got, "Path(%q) under %s", c.in, home)
			assert.Equal(t, got, r.Path(got), "Path is a fixed point on %q", c.in)
			back, err := r.Expand(got)
			require.NoError(t, err)
			assert.Equal(t, c.in, back, "Expand(Path(%q))", c.in)
			assert.Contains(t, r.Text(c.in), "-~", "Text(%q)", c.in)
		}
	}
}

// TestPath_FixedPointOnTheReviewedInputs: Path applied twice is Path applied
// once, for the inputs found by review, under both homes.
func TestPath_FixedPointOnTheReviewedInputs(t *testing.T) {
	for _, home := range []string{"/root", "/home/jfreeman"} {
		r := PathRedactor{Home: home, Repo: "/work/repo"}
		for _, in := range []string{"-home-bob(/root/root", "-home-jfreeman:/home/jfreeman-proj/home/jfreeman."} {
			once := r.Path(in)
			assert.Equal(t, once, r.Path(once), "Path(Path(%q)) under %s", in, home)
			assert.Equal(t, r.Content(in), r.Content(r.Content(in)))
		}
	}
}

// TestDashHome_CreateAndAuditKeepTheCommand: a Context and a Bash command that
// hold the own-home slug are stored with -~, not refused or dropped.
func TestDashHome_CreateAndAuditKeepTheCommand(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-140")
	c.Context = "cd -home-zed && ls"
	require.NoError(t, s.Create(c))
	got, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, "cd -~ && ls", got.Context)
}

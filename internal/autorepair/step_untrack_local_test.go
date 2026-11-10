package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// recLog collects what a step logs.
type recLog struct{ actions []string }

func (l *recLog) Logf(step, action string, paths []string, err error) {
	l.actions = append(l.actions, step+" "+action+" "+strings.Join(paths, ","))
}

// gitIn runs git in dir with an isolated environment.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	env := append(gitenv.Parsed(),
		"HOME="+t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	out, err := gitenv.OutputEnv(dir, "git", env, args...)
	require.NoError(t, err, "git %v", args)
	return strings.TrimSpace(string(out))
}

func untrackEnv(dir string, l Logger) Env {
	return Env{Roots: resolve.Located{Store: dir, Checkout: dir}, Log: l}
}

func runUntrack(t *testing.T, env Env) Outcome {
	t.Helper()
	out, err := untrackLocal(context.Background(), env)
	require.NoError(t, err)
	return out
}

func stagedDeletions(t *testing.T, dir string) []string {
	t.Helper()
	out := gitIn(t, dir, "diff", "--cached", "--name-only", "--diff-filter=D")
	if out == "" {
		return nil
	}
	got := strings.Split(out, "\n")
	sort.Strings(got)
	return got
}

// corpusRepo restores the version's repository into a temp dir.
func corpusRepo(t *testing.T, version string) string {
	t.Helper()
	bundle, err := filepath.Abs(filepath.Join("testdata", "corpus", version, "git", "repo.bundle"))
	require.NoError(t, err)
	parent := t.TempDir()
	gitIn(t, parent, "clone", "-q", bundle, "repo")
	return filepath.Join(parent, "repo")
}

// damageRepo builds a repository from damage/<name>: the fixture tree committed
// with every path in tracked-files.txt force-added.
func damageRepo(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", "damage", name)
	dir := t.TempDir()
	require.NoError(t, os.CopyFS(dir, os.DirFS(filepath.Join(src, "repo"))))
	list, err := os.ReadFile(filepath.Join(src, "tracked-files.txt"))
	require.NoError(t, err)
	gitIn(t, dir, "init", "-q", "-b", "main")
	for _, rel := range strings.Fields(string(list)) {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err != nil {
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o644))
		}
		gitIn(t, dir, "add", "-f", rel)
	}
	gitIn(t, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")
	return dir
}

func TestUntrackLocalRepositories(t *testing.T) {
	const sessions = ".punt-labs/ethos/sessions/"
	for _, tc := range []struct {
		name  string
		build func(*testing.T) string
		want  []string // paths staged as deletions
	}{
		{"v4.17.0", func(t *testing.T) string { return corpusRepo(t, "v4.17.0") }, []string{
			".punt-labs/ethos/missions/m-2026-11-09-001/log-sess-corpus-0001-1794265363950522000-1794265364118479000.jsonl",
			".punt-labs/ethos/missions/m-2026-11-09-001/reflections.yaml",
			".punt-labs/ethos/missions/m-2026-11-09-002/log-sess-corpus-0001-1794265363992598000-1794265363992598000.jsonl",
			sessions + "2026-11-09-sess-corpus-0001/audit-1794265364258830000-1794265364296484000.jsonl",
		}},
		{"v4.20.4", func(t *testing.T) string { return corpusRepo(t, "v4.20.4") }, []string{
			".punt-labs/ethos/missions.jsonl.lock",
			".punt-labs/ethos/missions/m-2026-11-09-001/log-sess-corpus-0001-1794265376489859000-1794265377197495000.jsonl",
			".punt-labs/ethos/missions/m-2026-11-09-001/reflections.yaml",
			".punt-labs/ethos/missions/m-2026-11-09-002/delegations/d-2026-11-09-001/prompt.md",
			".punt-labs/ethos/missions/m-2026-11-09-002/log-sess-corpus-0001-1794265376596142000-1794265376596142000.jsonl",
			sessions + "2026-11-09-sess-corpus-0001/audit-1794265378032965000-1794265378297700000.jsonl",
		}},
		{"v4.21.0", func(t *testing.T) string { return corpusRepo(t, "v4.21.0") }, nil},
		{"v4.22.1", func(t *testing.T) string { return corpusRepo(t, "v4.22.1") }, nil},
		{"tracked-trace-under-sessions", func(t *testing.T) string { return damageRepo(t, "tracked-trace-under-sessions") }, []string{
			sessions + "sess-damage-0001.audit.jsonl",
			sessions + "sess-damage-0001/audit.jsonl",
		}},
		{"force-added-local-file", func(t *testing.T) string { return damageRepo(t, "force-added-local-file") }, []string{
			".punt-labs/local/ethos/sessions/sess-damage-0001.audit.jsonl",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.build(t)
			l := &recLog{}
			env := untrackEnv(dir, l)

			out := runUntrack(t, env)
			assert.Equal(t, len(tc.want), out.Items)
			assert.False(t, out.Deferred)
			assert.Equal(t, tc.want, stagedDeletions(t, dir))
			assert.Empty(t, gitIn(t, dir, "ls-files", ".punt-labs/local"))
			for _, rel := range tc.want {
				assert.FileExists(t, filepath.Join(dir, rel), "files stay on disk")
			}
			if len(tc.want) > 0 {
				assert.NotEmpty(t, l.actions, "every change is logged")
			}

			before := gitIn(t, dir, "ls-files", "-s")
			out = runUntrack(t, env)
			assert.Zero(t, out.Items, "second run changes nothing")
			assert.Equal(t, before, gitIn(t, dir, "ls-files", "-s"))
		})
	}
}

func TestUntrackLocalKeepsExportsPublicationsAndArtifacts(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	files := map[string]string{
		".punt-labs/ethos/missions/m-1.contract.yaml":            "id: m-1\n",
		".punt-labs/ethos/missions/m-1.result.yaml":              "x: 1\n",
		".punt-labs/ethos/missions/m-2/artifacts/out.jsonl":      "{}\n",
		".punt-labs/ethos/missions/m-2/artifacts/audit.jsonl":    "{}\n",
		".punt-labs/ethos/missions/m-3/contract.yaml":            "id: m-3\nround_decisions: []\n",
		".punt-labs/ethos/missions/m-3/reflections.yaml":         "r: 1\n",
		".punt-labs/ethos/missions/m-3/log-s-1-2.jsonl":          "{\"record\":\"full\"}\n",
		".punt-labs/ethos/missions/m-4/contract.yaml":            "id: m-4\n",
		".punt-labs/ethos/missions/m-4/log-s-1-2.jsonl":          "{\"record\":\"full\"}\n",
		".punt-labs/ethos/missions/m-4/log-s-3-4.jsonl":          "{\"record\":\"blame\"}\n",
		".punt-labs/ethos/sessions/2026-11-09-s/audit-1-2.jsonl": "{\"record\":\"full\"}\n",
		".punt-labs/ethos/sessions/2026-11-09-s/audit-3-4.jsonl": "{\"record\":\"blame\"}\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		gitIn(t, dir, "add", "-f", rel)
	}
	gitIn(t, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "x")

	out := runUntrack(t, untrackEnv(dir, &recLog{}))
	assert.Equal(t, []string{
		".punt-labs/ethos/missions/m-4/log-s-3-4.jsonl",
		".punt-labs/ethos/sessions/2026-11-09-s/audit-3-4.jsonl",
	}, stagedDeletions(t, dir))
	assert.Equal(t, 2, out.Items)
}

func TestUntrackLocalDefersOnBusyIndex(t *testing.T) {
	dir := damageRepo(t, "force-added-local-file")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "index.lock"), nil, 0o644))
	out := runUntrack(t, untrackEnv(dir, &recLog{}))
	assert.True(t, out.Deferred)
	assert.Zero(t, out.Items)
}

func TestUntrackLocalOutsideRepository(t *testing.T) {
	out, err := untrackLocal(context.Background(), Env{})
	require.NoError(t, err)
	assert.Equal(t, Outcome{}, out)
}

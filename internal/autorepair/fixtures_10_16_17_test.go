package autorepair

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

type recLog struct {
	mu      sync.Mutex
	Entries []recEntry
}

type recEntry struct {
	Step, Action string
	Paths        []string
}

func (l *recLog) Logf(step, action string, paths []string, _ error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Entries = append(l.Entries, recEntry{step, action, paths})
}

func copyFixtureTree(t *testing.T, src, dst string) {
	t.Helper()
	if _, err := os.Stat(src); err != nil {
		return
	}
	require.NoError(t, filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm()|0o600)
	}))
}

// fixtureEnv copies the snapshot at testdata/<dir> into a fresh git
// repository and home, installs the snapshot's git/hooks, and returns an Env
// over them.
func fixtureEnv(t *testing.T, dir string) Env {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	require.NoError(t, os.Unsetenv("GIT_INDEX_FILE"))
	base := t.TempDir()
	repo, home := filepath.Join(base, "repo"), filepath.Join(base, "home")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	require.NoError(t, os.MkdirAll(home, 0o700))
	src := filepath.Join("testdata", dir)
	copyFixtureTree(t, filepath.Join(src, "repo"), repo)
	copyFixtureTree(t, filepath.Join(src, "home"), home)
	t.Setenv("HOME", home)
	_, err := gitenv.Output(repo, "git", "init", "-q")
	require.NoError(t, err)
	copyFixtureTree(t, filepath.Join(src, "git", "hooks"), filepath.Join(repo, ".git", "hooks"))
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	return Env{
		Roots:  resolve.Located{Store: repo, Ethos: filepath.Join(repo, ".punt-labs", "ethos"), Checkout: repo},
		Policy: p,
		Global: filepath.Join(home, ".punt-labs", "ethos"),
		Now:    time.Now(),
		Log:    &recLog{},
	}
}

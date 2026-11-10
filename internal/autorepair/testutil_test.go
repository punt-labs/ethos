//go:build linux || darwin

package autorepair

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

var corpusVersions = []string{"v3.11.0", "v3.12.0", "v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0", "v4.20.4", "v4.21.0", "v4.22.1"}

type logEntry struct {
	Step, Action string
	Paths        []string
	Err          error
}

type memLog struct {
	mu      sync.Mutex
	Entries []logEntry
}

func (l *memLog) Logf(step, action string, paths []string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Entries = append(l.Entries, logEntry{step, action, paths, err})
}

// copyTree copies src into dst, creating dst. A missing src is not an error.
func copyTree(t *testing.T, src, dst string) {
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
		return os.WriteFile(target, b, 0o600)
	}))
}

// corpusEnv copies a corpus snapshot into a temp repository and home and
// returns an Env over them. HOME is the copied home, so redaction sees it.
func corpusEnv(t *testing.T, version string) Env {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	require.NoError(t, os.Unsetenv("GIT_INDEX_FILE"))
	base := t.TempDir()
	repo, home := filepath.Join(base, "repo"), filepath.Join(base, "home")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	require.NoError(t, os.MkdirAll(home, 0o700))
	src := filepath.Join("testdata", "corpus", version)
	copyTree(t, filepath.Join(src, "repo"), repo)
	copyTree(t, filepath.Join(src, "home"), home)
	t.Setenv("HOME", home)
	_, err := gitenv.Output(repo, "git", "init", "-q")
	require.NoError(t, err)
	return repoEnv(t, repo, home)
}

func repoEnv(t *testing.T, repo, home string) Env {
	t.Helper()
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	return Env{
		Roots:  resolve.Located{Store: repo, Ethos: filepath.Join(repo, ".punt-labs", "ethos"), Checkout: repo},
		Policy: p,
		Global: filepath.Join(home, ".punt-labs", "ethos"),
		Now:    time.Now(),
		Log:    &memLog{},
	}
}

// listFiles returns the relative paths of every file under root, sorted.
func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// hashFiles maps every file under the given roots (relative path, prefixed by
// the root's index) to the sha256 of its bytes, so a test can assert a run
// changed nothing.
func hashFiles(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i, root := range roots {
		for _, f := range listFiles(t, root) {
			b, err := os.ReadFile(filepath.Join(root, f))
			require.NoError(t, err)
			sum := sha256.Sum256(b)
			out[fmt.Sprintf("%d:%s", i, f)] = hex.EncodeToString(sum[:])
		}
	}
	return out
}

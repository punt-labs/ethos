package autorepair

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// recLog records every Logf call so tests can assert a step logged.
type recLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *recLog) Logf(step, action string, paths []string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = append(l.steps, step+":"+action)
}

// copyTree copies dir src to dst, creating dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
	require.NoError(t, err)
}

// corpusEnv copies one corpus snapshot into a temp dir and returns an Env
// whose repository is the snapshot's repo/ and whose global root is its
// home/.punt-labs/ethos. HOME is set to a distinct temp dir.
func corpusEnv(t *testing.T, version string) (Env, *recLog) {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join("testdata", "corpus", version)
	repo := filepath.Join(base, "repo")
	home := filepath.Join(base, "home")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	require.NoError(t, os.MkdirAll(home, 0o700))
	copyTree(t, filepath.Join(src, "repo"), repo)
	copyTree(t, filepath.Join(src, "home"), home)
	t.Setenv("HOME", home)
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	log := &recLog{}
	return Env{
		Roots:  resolve.Located{Store: repo, Ethos: filepath.Join(repo, ".punt-labs", "ethos"), Checkout: repo},
		Policy: p,
		Global: filepath.Join(home, ".punt-labs", "ethos"),
		Now:    time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
		Log:    log,
	}, log
}

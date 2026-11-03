package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

const ignoreSessions = ".punt-labs/ethos/sessions/\n"

// A damaged chunk in a gitignored session is local only: the seal skips that
// session with one line naming the chunk and the recovery command, and seals
// the others.
func TestSealRepo_DamagedChunkInIgnoredSessionIsSkipped(t *testing.T) {
	now := time.Now().UTC()
	cases := map[string]func(t *testing.T, dir string) string{
		"corrupt chunk": func(t *testing.T, dir string) string {
			// Last ts 150 disagrees with the filename's 200.
			name := audit.SessionChunkFile(100, 200)
			writeChunkFile(t, dir, name, 100, 150)
			return name
		},
		"orphan corrupt": func(t *testing.T, dir string) string {
			name := audit.SessionChunkFile(100, 200)
			writeChunkFile(t, dir, name+".corrupt", 100, 200)
			return name
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			initGitRepo(t, repo)
			require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(ignoreSessions), 0o600))
			badDir, _ := resolveRepoSessionDir(repo, "sess-bad", now)
			chunk := damage(t, badDir)
			writeLiveLines(t, repo, "sess-bad", 300)
			writeLiveLines(t, repo, "sess-good", 100, 200)

			var res SealResult
			var err error
			stderr := captureStderr(t, func() { res, err = SealRepo(repo, now, SealOptions{}) })

			require.NoError(t, err)
			assert.Equal(t, 1, res.SessionsSealed, "only the undamaged session seals")
			goodDir, _ := resolveRepoSessionDir(repo, "sess-good", now)
			assert.FileExists(t, filepath.Join(goodDir, audit.SessionChunkFile(100, 200)))
			lines := strings.Split(strings.TrimSpace(stderr), "\n")
			require.Len(t, lines, 1, "stderr: %q", stderr)
			assert.Contains(t, lines[0], "ethos audit quarantine "+filepath.Join(badDir, chunk))
			assert.Contains(t, lines[0], "sess-bad")
		})
	}
}

// The same damage in a tracked session still blocks the commit.
func TestSealRepo_DamagedChunkInTrackedSessionStillBlocks(t *testing.T) {
	now := time.Now().UTC()
	repo := t.TempDir()
	initGitRepo(t, repo)
	badDir, _ := resolveRepoSessionDir(repo, "sess-bad", now)
	writeChunkFile(t, badDir, audit.SessionChunkFile(100, 200), 100, 150)
	writeLiveLines(t, repo, "sess-bad", 300)

	_, err := SealRepo(repo, now, SealOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "corrupt chunk")
}

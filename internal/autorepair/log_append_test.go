package autorepair

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two commits in two worktrees share one global root. Every line from every
// writer survives, even past the rotation cap, because the append log never
// rotates.
func TestAppendLogsSharingOneFileLoseNoLines(t *testing.T) {
	global := t.TempDir()
	a := NewAppendLog(global, "/s1", "/c1", "v")
	b := NewAppendLog(global, "/s2", "/c2", "v")
	const n = 200
	var wg sync.WaitGroup
	for w, l := range []*FileLog{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n; i++ {
				l.Logf(fmt.Sprintf("w%d", w), fmt.Sprintf("line-%d", i), nil, nil)
			}
		}()
	}
	wg.Wait()
	f, err := os.Open(LogPath(global))
	require.NoError(t, err)
	defer f.Close()
	count := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		count++
	}
	assert.Equal(t, 2*n, count)
	_, err = os.Stat(filepath.Join(global, "logs", "repair.log.1"))
	assert.True(t, os.IsNotExist(err), "the append log never rotates")
}

func TestFileLogRedactsCredentialsAndAddresses(t *testing.T) {
	global := t.TempDir()
	l := NewAppendLog(global, "", "", "v")
	l.Logf("s", "failed", nil, fmt.Errorf("read: mail jim@punt-labs.com token ghp_abcdefghijklmnopqrstuvwxyz0123456789"))
	b, err := os.ReadFile(LogPath(global))
	require.NoError(t, err)
	assert.NotContains(t, string(b), "jim@punt-labs.com")
	assert.NotContains(t, string(b), "ghp_abcdefghijklmnopqrstuvwxyz0123456789")
}

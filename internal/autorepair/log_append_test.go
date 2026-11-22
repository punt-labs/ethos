package autorepair

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Hook repair adapters share the injected sink without dropping actions.
func TestAppendLogsSharingOneFileLoseNoLines(t *testing.T) {
	global := t.TempDir()
	logger := captureRepairLog(global)
	a := NewAppendLog("/s1", "/c1", "v", logger)
	b := NewAppendLog("/s2", "/c2", "v", logger)
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
		if strings.Contains(sc.Text(), "line-") {
			count++
		}
	}
	assert.Equal(t, 2*n, count)
}

func TestFileLogRedactsCredentialsAndAddresses(t *testing.T) {
	global := t.TempDir()
	l := NewAppendLog("", "", "v", captureRepairLog(global))
	l.Logf("s", "failed", nil, fmt.Errorf("read: mail jim@punt-labs.com token ghp_abcdefghijklmnopqrstuvwxyz0123456789"))
	b, err := os.ReadFile(LogPath(global))
	require.NoError(t, err)
	assert.NotContains(t, string(b), "jim@punt-labs.com")
	assert.NotContains(t, string(b), "ghp_abcdefghijklmnopqrstuvwxyz0123456789")
}

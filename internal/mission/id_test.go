//go:build !windows

package mission

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewID_FirstCounter(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	id, err := NewID(root, now)
	require.NoError(t, err)
	assert.Equal(t, "m-2026-04-07-001", id)
}

func TestNewID_Monotonic(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	const n = 100
	seen := make(map[string]bool, n)
	for i := 1; i <= n; i++ {
		id, err := NewID(root, now)
		require.NoError(t, err)
		expected := fmt.Sprintf("m-2026-04-07-%03d", i)
		assert.Equal(t, expected, id)
		assert.False(t, seen[id], "duplicate ID %q", id)
		seen[id] = true
	}
	assert.Len(t, seen, n)
}

func TestNewID_Concurrent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	const goroutines = 10
	const perGoroutine = 10
	const total = goroutines * perGoroutine

	results := make(chan string, total)
	errs := make(chan error, total)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				id, err := NewID(root, now)
				if err != nil {
					errs <- err
					return
				}
				results <- id
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	seen := make(map[string]bool, total)
	for id := range results {
		assert.False(t, seen[id], "duplicate ID %q observed under concurrent NewID", id)
		seen[id] = true
	}
	assert.Len(t, seen, total, "expected %d distinct IDs from %d goroutines", total, goroutines)
}

func TestNewID_DailyRollover(t *testing.T) {
	root := t.TempDir()
	day1 := time.Date(2026, 4, 7, 23, 59, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 8, 0, 0, 1, 0, time.UTC)

	// Two on day 1.
	id1, err := NewID(root, day1)
	require.NoError(t, err)
	assert.Equal(t, "m-2026-04-07-001", id1)

	id2, err := NewID(root, day1)
	require.NoError(t, err)
	assert.Equal(t, "m-2026-04-07-002", id2)

	// Day 2 starts fresh.
	id3, err := NewID(root, day2)
	require.NoError(t, err)
	assert.Equal(t, "m-2026-04-08-001", id3)

	// Day 1 continues from where it left off.
	id4, err := NewID(root, day1)
	require.NoError(t, err)
	assert.Equal(t, "m-2026-04-07-003", id4)
}

func TestNewID_PadsZeroes(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	for i := 1; i <= 12; i++ {
		id, err := NewID(root, now)
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("m-2026-04-07-%03d", i), id)
	}
}

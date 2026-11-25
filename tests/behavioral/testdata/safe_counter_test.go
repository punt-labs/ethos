//go:build behavioral_race_probe

package counter

import (
	"sync"
	"testing"
)

func TestSafeCounterConcurrentIncrement(t *testing.T) {
	const workers, increments = 8, 128
	var c SafeCounter
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < increments; j++ {
				c.Increment()
				c.Value()
			}
		}()
	}
	wg.Wait()
	if got, want := c.Value(), workers*increments; got != want {
		t.Fatalf("counter value = %d, want %d", got, want)
	}
}

package sequence

import (
	"sync"
	"testing"
)

func TestNextIncrements(t *testing.T) {
	c := NewCounters(4)
	for want := uint64(0); want < 10; want++ {
		got := c.Next(0)
		if got != want {
			t.Fatalf("Next(0) = %d, want %d", got, want)
		}
	}
}

func TestShardsIndependent(t *testing.T) {
	c := NewCounters(3)
	c.Next(0)
	c.Next(0)
	c.Next(1)
	if got := c.Next(0); got != 2 {
		t.Errorf("shard 0 counter = %d, want 2", got)
	}
	if got := c.Next(1); got != 1 {
		t.Errorf("shard 1 counter = %d, want 1", got)
	}
	if got := c.Next(2); got != 0 {
		t.Errorf("shard 2 counter = %d, want 0 (untouched)", got)
	}
}

func TestConcurrentNext(t *testing.T) {
	const goroutines = 16
	const callsEach = 1000
	c := NewCounters(1)

	var wg sync.WaitGroup
	results := make(chan uint64, goroutines*callsEach)

	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range callsEach {
				results <- c.Next(0)
			}
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[uint64]bool, goroutines*callsEach)
	for v := range results {
		if seen[v] {
			t.Errorf("duplicate sequence number %d", v)
		}
		seen[v] = true
	}
	if got := uint64(len(seen)); got != goroutines*callsEach {
		t.Errorf("got %d distinct values, want %d", got, goroutines*callsEach)
	}
}

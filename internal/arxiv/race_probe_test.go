package arxiv

import (
	"sync"
	"testing"
)

// TestRaceProbe is a deliberate data race (OPS-H15 AC2). Temporary: reverted in the next commit.
func TestRaceProbe(t *testing.T) {
	var n int
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				n++
			}
		}()
	}
	wg.Wait()
	t.Logf("n=%d", n)
}

package client

import (
	"fmt"
	"testing"
	"time"

	"github.com/cyber-godzilla/praetor/internal/types"
)

// A large server burst must not lose game text. The consumer here is briefly
// busy (one slow render tick) before it starts draining, which is what lets the
// event channel fill up in the real GUI.
func TestLargeBurstDeliversEveryLine(t *testing.T) {
	for _, n := range []int{200, 500} {
		t.Run(fmt.Sprintf("lines=%d", n), func(t *testing.T) {
			c := newDiscTestClient(t)
			defer c.Engine.Close()

			got := make(chan int, 1)
			go func() {
				time.Sleep(100 * time.Millisecond) // consumer busy rendering
				count := 0
				for ev := range c.events {
					if tev, ok := ev.(types.GameTextEvent); ok && tev.Text != "" {
						count++
					}
				}
				got <- count
			}()

			for i := 0; i < n; i++ {
				c.processLine(fmt.Sprintf("The guard %d swings a sword at you.", i))
			}
			close(c.events)

			if delivered := <-got; delivered != n {
				t.Fatalf("delivered %d of %d game-text lines — %d silently dropped",
					delivered, n, n-delivered)
			}
		})
	}
}

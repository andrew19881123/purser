package reconciler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/reconciler"
)

// TestReconcilerStatus_NoConcurrentDataRace verifies that calling Reconcile
// and Status concurrently does not produce a data race. Run with:
//
//	go test -race ./reconciler/...
func TestReconcilerStatus_NoConcurrentDataRace(t *testing.T) {
	reg := openReg(t)
	cfg := reconciler.DefaultConfig()
	// Use a threshold of 1 and zero hysteresis so the loop acts faster.
	cfg.FailureThreshold = 1
	cfg.Hysteresis = 0
	rc := reconciler.New(reg, nil, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	const workers = 8
	var wg sync.WaitGroup

	// Half the goroutines call Reconcile, the other half call Status.
	for i := 0; i < workers; i++ {
		wg.Add(1)
		if i%2 == 0 {
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					default:
						_, _ = rc.Reconcile(ctx)
					}
				}
			}()
		} else {
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					default:
						_ = rc.Status()
					}
				}
			}()
		}
	}

	wg.Wait()
}

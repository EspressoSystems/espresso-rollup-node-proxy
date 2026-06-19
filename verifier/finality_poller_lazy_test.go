package verifier_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/verifier"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// TestFinalityPollerLazy_ReturnsSnapshot that the finality poller function
// does indeed get invoked as expected, and return the value as expected.
func TestFinalityPollerLazy_ReturnsSnapshot(t *testing.T) {
	require := require.New(t)
	var invokedCount int
	poller := verifier.NewFinalityPollerLazy(
		verifier.WithFinalityPollFunc[uint64](
			func(_ context.Context) (uint64, error) {
				invokedCount++
				return 42, nil
			}),
		verifier.WithLogger[uint64](
			log.New(),
		),
		verifier.WithInterval[uint64](
			time.Hour,
		),
	)

	snapshot, ok := poller.LastSnapshot()
	require.True(ok, "LastSnapshot should return true, upon successful snapshot retrieval")
	require.Equal(uint64(42), snapshot, "LastSnapshot should return the expected snapshot value")
	require.Equal(1, invokedCount, "Finality poll function should have been invoked exactly once")
}

// TestFinalityPollerLazy_ThreadSafeLatestSnapshotCalls verifies that multiple
// goroutine calls to the method [verifier.FinalityPollerLazy.LastSnapshot] are
// thread safe, and that only one of them will actually trigger the call, yet
// all will result in the same response.
func TestFinalityPollerLazy_ThreadSafeLatestSnapshotCalls(t *testing.T) {
	require := require.New(t)
	var invokedCount atomic.Int32
	poller := verifier.NewFinalityPollerLazy(
		verifier.WithFinalityPollFunc[uint64](
			func(_ context.Context) (uint64, error) {
				invokedCount.Add(1)
				return 42, nil
			}),
		verifier.WithLogger[uint64](
			log.New(),
		),
		verifier.WithInterval[uint64](
			time.Hour,
		),
	)

	var wg sync.WaitGroup

	const N = 10
	for range N {
		wg.Go(func() {
			snapshot, ok := poller.LastSnapshot()
			require.True(ok, "LastSnapshot should return true, upon successful snapshot retrieval")
			require.Equal(uint64(42), snapshot, "LastSnapshot should return the expected snapshot value")
		})
	}

	wg.Wait()

	require.Equal(int32(1), invokedCount.Load(), "Finality poll function should have been invoked exactly once")
}

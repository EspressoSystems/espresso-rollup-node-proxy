package verifier

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// TestFinalityPoller_LastSnapshotBeforeStart verifies that a newly created
// FinalityPoller reports no snapshot available until Start has been called and
// the first poll has completed.
func TestFinalityPoller_LastSnapshotBeforeStart(t *testing.T) {
	poller := NewFinalityPoller(
		func(_ context.Context) (uint64, error) { return 42, nil },
		log.New(),
		time.Hour,
	)

	_, ok := poller.LastSnapshot()
	require.False(t, ok, "LastSnapshot should return false before Start is called")
}

// TestFinalityPoller_PollsImmediatelyOnStart verifies that the poller executes
// its poll function immediately when Start is called, without waiting for the
// first ticker interval to elapse. After Start returns the snapshot should
// become available well within a short deadline.
func TestFinalityPoller_PollsImmediatelyOnStart(t *testing.T) {
	ready := make(chan struct{})
	poller := NewFinalityPoller(
		func(_ context.Context) (uint64, error) {
			select {
			case <-ready:
			default:
				close(ready)
			}
			return 42, nil
		},
		log.New(),
		time.Hour, // long interval — only the immediate poll should fire during this test
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop()

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not poll immediately on Start")
	}

	snap, ok := poller.LastSnapshot()
	require.True(t, ok)
	require.Equal(t, uint64(42), snap)
}

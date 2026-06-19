package verifier

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// TestFinalityPoller_LastSnapshotBeforeStart verifies that a newly created
// FinalityPoller reports no snapshot available until Start has been called and
// the first poll has completed.
func TestFinalityPoller_LastSnapshotBeforeStart(t *testing.T) {
	poller := NewFinalityPollerEagerLegacy(
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
	poller := NewFinalityPollerEagerLegacy(
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

	ctx, cancel := context.WithCancel(t.Context())
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

// TestFinalityPoller_StartStop verifies the lifecycle of a FinalityPoller: Start
// launches a background goroutine that populates a snapshot, and Stop terminates
// it cleanly without blocking. Calling Stop on a poller that was never started
// must also be a no-op (no panic, no block).
func TestFinalityPoller_StartStop(t *testing.T) {
	poller := NewFinalityPollerEagerLegacy(
		func(_ context.Context) (uint64, error) { return 1, nil },
		log.New(),
		time.Millisecond,
	)

	// Stop before Start must not block or panic.
	poller.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)

	require.Eventually(t, func() bool {
		_, ok := poller.LastSnapshot()
		return ok
	}, 2*time.Second, 5*time.Millisecond, "snapshot should appear after Start")

	done := make(chan struct{})
	go func() { poller.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked unexpectedly")
	}
}

// TestFinalityPoller_DoubleStartIsNoOp verifies that calling Start on an already-
// running FinalityPoller is a no-op: no additional goroutine is spawned, and the
// subsequent Stop call returns promptly without deadlocking on an over-incremented
// WaitGroup.
func TestFinalityPoller_DoubleStartIsNoOp(t *testing.T) {
	poller := NewFinalityPollerEagerLegacy(
		func(_ context.Context) (uint64, error) { return 42, nil },
		log.New(),
		time.Hour,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)

	require.Eventually(t, func() bool {
		_, ok := poller.LastSnapshot()
		return ok
	}, 2*time.Second, 5*time.Millisecond)

	// Second Start while already running must be a no-op.
	poller.Start(ctx)

	done := make(chan struct{})
	go func() { poller.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked — double-Start likely incremented WaitGroup twice")
	}
}

// TestFinalityPoller_SnapshotUpdatesOnTick verifies that LastSnapshot reflects
// the value returned by the most recent invocation of the poll function, and
// that the poller continues to update the snapshot on subsequent ticks.
func TestFinalityPoller_SnapshotUpdatesOnTick(t *testing.T) {
	var val atomic.Uint64
	val.Store(1)

	poller := NewFinalityPollerEagerLegacy(
		func(_ context.Context) (uint64, error) { return val.Load(), nil },
		log.New(),
		5*time.Millisecond,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop()

	require.Eventually(t, func() bool {
		snap, ok := poller.LastSnapshot()
		return ok && snap == 1
	}, 2*time.Second, 5*time.Millisecond, "initial snapshot should reflect val=1")

	val.Store(99)
	require.Eventually(t, func() bool {
		snap, ok := poller.LastSnapshot()
		return ok && snap == 99
	}, 2*time.Second, 5*time.Millisecond, "snapshot should update to val=99 after next tick")
}

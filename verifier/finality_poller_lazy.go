package verifier

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

type snapshotState[T any] struct {
	state   T
	valid   bool
	updated time.Time
}

// EagerFinalityPoller is a [FinalityPollerEager] implementation that uses a
// background goroutine to poll for finality updates at a regular interval.
type lazyFinalityPoller[T any] struct {
	finalityPollFunc func(ctx context.Context) (T, error)
	logger           log.Logger
	interval         time.Duration
	finalitySnapshot atomic.Pointer[snapshotState[T]]
	wg               atomic.Pointer[sync.WaitGroup]
}

var _ FinalityPoller[any] = (*lazyFinalityPoller[any])(nil)

func NewFinalityPollerLazy[T any](options ...FinalityPollerOption[T]) FinalityPoller[T] {
	var config FinalityPollerConfig[T]
	for _, opt := range options {
		opt(&config)
	}

	if err := configValidation(&config); err != nil {
		panic(err)
	}

	return &lazyFinalityPoller[T]{
		finalityPollFunc: config.finalityPollFunc,
		logger:           config.logger,
		interval:         config.interval,
	}
}

// LastSnapshot returns the most recently polled snapshot. The bool is false if
// no snapshot has been fetched yet.
func (p *lazyFinalityPoller[T]) LastSnapshot() (T, bool) {
	currentState := p.finalitySnapshot.Load()
	if currentState == nil {
		return p.poll(snapshotState[T]{})
	}

	if !currentState.valid || time.Since(currentState.updated) > p.interval {
		return p.poll(*currentState)
	}

	return currentState.state, currentState.valid
}

// retrieveCurrentState is a convenience function that retrieves the current
// snapshot state from the atomic finalitySnapshot.
//
// This merely exists for code reuse.
func (p *lazyFinalityPoller[T]) retrieveCurrentState() (T, bool) {
	// This is a weird ege case that should not really be possible?
	currentState := p.finalitySnapshot.Load()
	if currentState == nil {
		// On man, everything is just working against us
		var empty T
		return empty, false
	}

	return currentState.state, currentState.valid
}

// poll is a function that is meant to make the determination of whether or
// not it should perform the poll itself.
//
// At the point of this call we've already made the call that we need to try
// and update our local snapshot, but we could be getting called from multiple
// goroutines / threads at once. We really don't want to have multiple
// triggered updates in flight at once, so we will try and see if we should
// be the one to perform the call or not.
func (p *lazyFinalityPoller[T]) poll(currentState snapshotState[T]) (T, bool) {
	var nextWg sync.WaitGroup
	// We add immediately here so that we automatically store a WaitGroup with
	// a value loaded, to prevent a potential Wait race.
	nextWg.Add(1)
	defer nextWg.Done()

	if !p.wg.CompareAndSwap(nil, &nextWg) {
		// We are not the ones polling, so we will need to wait for the current
		// polling in-progress to complete before returning.
		if wg := p.wg.Load(); wg != nil {
			// Alright, we can wait for the polling to finish, and then we can return
			// the new snapshot state
			wg.Wait()
		}

		// There is a weird edge case where we didn't succeed in being the ones
		// to poll, implying there was a poll in progress, yet when we check
		// and see that there is no WaitGroup stored. This must indicate
		// that the polling just happened to finish recently. In this edge-case
		// it just means we shouldn't need to wait.

		return p.retrieveCurrentState()
	}

	// We are the ones polling. Let's be sure to remove our waitgroup when we
	// complete the polling so that others are informed.
	defer p.wg.Store(nil)
	return p.performPoll(currentState)
}

// performPoll performs the actual polling for a new snapshot state. It will
// update the finalitySnapshot with the new snapshot state if the poll was
// successful. It returns
func (p *lazyFinalityPoller[T]) performPoll(currentState snapshotState[T]) (T, bool) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		finalityPollTimeout,
	)
	defer cancel()

	// Fetch a new snapshot state.
	nextSnapshot, err := p.finalityPollFunc(ctx)
	if err != nil {
		// We failed to retrieve the snapshot state, we'll continue with our
		// current snapshot state.
		p.logger.Error("failed to fetch finalized block", "error", err)

		// TODO: should we update the current snapshot state, so we wait the
		// full interval to attempt to perform this poll again, or should we
		// keep it as is and allow it to keep trying to determine a new state?
		return currentState.state, currentState.valid
	}

	p.logger.Debug("finality poller updating", "snapshot", nextSnapshot)
	// Update our State
	p.finalitySnapshot.Store(
		&snapshotState[T]{
			valid:   true,
			updated: time.Now(),
			state:   nextSnapshot,
		},
	)

	return nextSnapshot, true
}

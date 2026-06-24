package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

type Timestamped[T any] struct {
	Value     T
	UpdatedAt time.Time
}

func (t *Timestamped[T]) Update(value T) {
	*t = t.Replace(value)
}

func (t Timestamped[T]) Replace(value T) Timestamped[T] {
	return Timestamped[T]{
		Value:     value,
		UpdatedAt: time.Now(),
	}
}

type CurrentState[T any] struct {
	Value   Timestamped[StoreState[T]]
	Skipped Timestamped[uint64]
}

func (c *CurrentState[T]) Update(value StoreState[T]) {
	*c = c.Replace(value)
}

func (c CurrentState[T]) Replace(value StoreState[T]) CurrentState[T] {
	return CurrentState[T]{
		Value: Timestamped[StoreState[T]]{
			Value:     value,
			UpdatedAt: time.Now(),
		},
	}
}

// DebounceDecision is a bitmask that indicates the reason for a Debounce
// decision.
type DebounceDecision uint8

const (
	// ContinueAge is a [DebounceDecision] that indicates that a call should
	// be continue based on the grounds of the age since the last call.
	ContinueAge DebounceDecision = 1 << iota

	// ContinueSkipped is a [DebounceDecision] that indicates that a call
	// should continue based on the grounds of the number of skipped calls.
	//
	// This indicates that we don't want to get a value that is too stale.
	ContinueSkipped

	// ContinueSnapshot is a [DebounceDecision] that indicates that a call
	// should continue due to needing a snapshot / checkpoint.
	ContinueSnapshot

	// ContinueUnspecified is a [DebounceDecision] that indicates that a call
	// should continue, and the reason is not more specific than this unspecified
	// reason.
	ContinueUnspecified

	// ContinueDistance is a [DebounceDecision] that indicates that a call
	// should  continue based on the grounds of Distance.  This continue
	// is specific to block distance.
	ContinueDistance

	// Debounce indicates that a call should be debounced, and not continue.
	Debounce DebounceDecision = 0
)

// DebounceDecider is an interface that is utilizied to determine whether
// a call should be debounced or not.
type DebounceDecider[T any] interface {
	// DecideDebounce is a method that determines whether a call should be
	// debounced or not based on the [CurrentState] of the Debounce and
	// the incoming new state.
	DecideDebounce(currentState CurrentState[T], newState T) DebounceDecision
}

// debounceDeciderMaxAge is a [DebounceDecider] that debounces based on the
// [time.Duration] it wraps. It is utilized to ensure that the time between
// calls does not exceed the value specified.
type debounceDeciderMaxAge[T any] time.Duration

// DecideDebounce implements [DebounceDecider]
func (a debounceDeciderMaxAge[T]) DecideDebounce(currentState CurrentState[T], _ T) DebounceDecision {
	lastUpdated := currentState.Value.UpdatedAt

	// If the last updated timestamp is invalid, not populated, or if it's been
	// too long since the last value was stored, then we should store the value.
	if lastUpdated.IsZero() || time.Since(lastUpdated) >= time.Duration(a) {
		return ContinueAge
	}

	return Debounce
}

// DebounceMaxAge creates a [DebounceDecider] that debounces based on the
// the time since the last call was invoked.
func DebounceMaxAge[T any](maxAge time.Duration) DebounceDecider[T] {
	return debounceDeciderMaxAge[T](maxAge)
}

// debounceDeciderMultiple is a [DebounceDecider] that combines multiple
// [DebounceDecider]s into a single [DebounceDecider].
type debounceDeciderMultiple[T any] []DebounceDecider[T]

// DecideDebounce implements [DebounceDecider]
//
// It will evaluate the [CurrentState] and the incoming state against all
// conditions, and return a [DebounceDecisions] that is a bitwise or of all
// of the underlying decisions.
func (s debounceDeciderMultiple[T]) DecideDebounce(currentState CurrentState[T], newState T) DebounceDecision {
	decision := Debounce

	for _, decider := range s {
		decision |= decider.DecideDebounce(currentState, newState)
	}

	return decision
}

// DebounceMultiple creates a [DebounceDecider] that combines multiple
// [DebounceDecider]s into a single [DebounceDecider].
func DebounceMultiple[T any](deciders ...DebounceDecider[T]) DebounceDecider[T] {
	return debounceDeciderMultiple[T](deciders)
}

// debounceDeciderDistance is a [DebounceDecider] that debounces based on the
// relative distance between the [CurrentState] and the incoming state.
type debounceDeciderDistance[T any] struct {
	distance        uint64
	heightExtractor func(T) uint64
}

// DecideDebounce implements [DebounceDecider]
//
// This decision is based on the distance between the incoming new state and
// the [CurrentState]'s apparent height.
func (d debounceDeciderDistance[T]) DecideDebounce(currentState CurrentState[T], newState T) DebounceDecision {
	newStateHeight := d.heightExtractor(newState)
	currentStateHeight := d.heightExtractor(currentState.Value.Value.State)

	if currentState.Value.Value.Status != Valid || newStateHeight-currentStateHeight >= d.distance {
		return ContinueDistance
	}

	return Debounce
}

// DebounceMaxDistance creates a [DebounceDecider] that debounces based on the
// distance between the incmoing state and the [CurrentState] utilizing the
// given heightExtrator function to determine the height of each of the state
// pieces.
func DebounceMaxDistance[T any](maxDistance uint64, heightExtractor func(T) uint64) DebounceDecider[T] {
	return debounceDeciderDistance[T]{
		distance:        maxDistance,
		heightExtractor: heightExtractor,
	}
}

// debounceDeciderMaxSkipped is a [DebounceDecider] that debounces based on
// the number of times something has been debounced.
type debounceDeciderMaxSkipped[T any] uint64

// DecideDebounce implements [DebounceDecider]
//
// This decision examples how many times a call has been debounced and if it
// exceeds the threshold, it will continue.
func (s debounceDeciderMaxSkipped[T]) DecideDebounce(currentState CurrentState[T], _ T) DebounceDecision {
	if currentState.Skipped.Value >= uint64(s) {
		return ContinueSkipped
	}

	return Debounce
}

// DebounceMaxSkipped creates a [DebounceDecider] that debounces based on the
// the given maxSkipped parameter.
func DebounceMaxSkipped[T any](maxSkipped uint64) DebounceDecider[T] {
	return debounceDeciderMaxSkipped[T](maxSkipped)
}

// debounceStoragedRunningState is a struct that represents the running state
// of a [DebouncingStorage]'s runloop.
type debounceStoragedRunningState[T any] struct {
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	ch      chan T
	store   Storage[T]
	decider DebounceDecider[T]
	state   CurrentState[T]
}

// runloop is the main running loop of the [debounceStoragedRunningState].
// It will continually wait for new store requests and evaluating them
// based on the internally stored [DebounceDecider].
func (r *debounceStoragedRunningState[T]) runloop() {
	defer r.cancel()
	for {
		select {
		default:
		case <-r.ctx.Done():
			r.drainAndStore()
			return
		}
		r.eval()
	}
}

// drainAndStore will attempt to drain the channel of any remaining
// requests, and store only the last one (if there are any)
func (r *debounceStoragedRunningState[T]) drainAndStore() {
	var toStore StoreState[T]

	for state := range r.ch {
		toStore = StoreState[T]{
			State:  state,
			Status: Valid,
		}
	}

	if toStore.Status != Valid {
		// Nothing to do
		return
	}

	r.store.Store(context.Background(), toStore.State)
}

// eval performs the logic of the internal loop. It waits for an incoming
// request, and will evaluate it against the [DebounceDecider] to determine
// whether to store the result or not.
func (r *debounceStoragedRunningState[T]) eval() {
	decider := r.decider

	select {
	case <-r.ctx.Done():
	case state, ok := <-r.ch:
		if !ok {
			// The channel has been closed, we should exit the loop.
			// We'll also cancel the context to ensure that we don't
			// end back up here again immediately.
			r.cancel()
			return
		}

		// Evaluate the criteria
		decision := decider.DecideDebounce(r.state, state)
		if decision == Debounce {
			// We're skipping this entry
			r.state.Skipped = r.state.Skipped.Replace(r.state.Skipped.Value + 1)
			return
		}

		// We're storing the new value
		r.store.Store(r.ctx, state)
		r.state = r.state.Replace(StoreState[T]{
			Status: Valid,
			State:  state,
		})
	}
}

// DebouncingStorage represents a [Storage] implementation that will
// orchestrate [Storage.Store] calls via a spawned goroutine utilizing
// a [DebounceDecider] to determine when to actually store the incoming
// calls rather than discarding them.
type DebouncingStorage[T any] struct {
	store   Storage[T]
	running atomic.Pointer[debounceStoragedRunningState[T]]
}

// Load implements [Storage]
func (s *DebouncingStorage[T]) Load(ctx context.Context) StoreState[T] {
	return s.store.Load(ctx)
}

// Store implements [Storage], and forwards all Store requests to the
// spawned goroutine waiting for requests.
//
// NOTE: it is an error to call this function before calling
// [DebouncingStorage.Start].  If no active gortouine is running, then this
// function will panic.
func (s *DebouncingStorage[T]) Store(ctx context.Context, newState T) {
	runState := s.running.Load()
	if runState == nil {
		// We cannot store anything right now.
		panic(errors.New("go goroutine running, please call Start before calling Store"))
	}

	select {
	case <-ctx.Done():
		// We are being cancelled, we cannot store anything right now.
		return
	case runState.ch <- newState:
		// As expected
	}
}

// Start will attempt to spawn a new goroutine that will continually wait
// for new store requests.  The given [DebounceDecider] will be utilitized
// to determine how often requests are stored.
func (s *DebouncingStorage[T]) Start(ctx context.Context, decider DebounceDecider[T]) error {
	ctx, cancel := context.WithCancel(ctx)
	runState := debounceStoragedRunningState[T]{
		ctx:     ctx,
		cancel:  cancel,
		ch:      make(chan T, 32),
		store:   s.store,
		decider: decider,
	}

	if !s.running.CompareAndSwap(nil, &runState) {
		// Already running
		return errors.New("already running")
	}

	runState.wg.Go(runState.runloop)

	return nil
}

// Stop will attempt to stop the running goroutine that is waiting for new
// Store requests.  Upon stopping, the Debouncer will close the channel to
// indicate that no new Store requests will be incoming.
//
// This will wait for the goroutine to finish before returning.
func (s *DebouncingStorage[T]) Stop(ctx context.Context) error {
	runState := s.running.Swap(nil)
	if runState == nil {
		return errors.New("not running")
	}

	// Alright, we have the running state, let's cancel it and wait for it to
	// finish.

	close(runState.ch)
	runState.cancel()
	runState.wg.Wait()

	return nil
}

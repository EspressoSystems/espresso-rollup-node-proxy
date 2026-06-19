package verifier

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

type goroutineContext struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// EagerFinalityPoller is a [FinalityPollerEager] implementation that uses a
// background goroutine to poll for finality updates at a regular interval.
type eagerFinalityPoller[T any] struct {
	finalityPollFunc func(ctx context.Context) (T, error)
	logger           log.Logger
	interval         time.Duration
	finalitySnapshot atomic.Pointer[T]

	threadCtx atomic.Pointer[goroutineContext]
}

// Compile-time assertion that *FinalityPoller[T] implements
// FinalityPollerInterface[T].
var _ FinalityPollerEager[any] = (*eagerFinalityPoller[any])(nil)

func NewFinalityPollerEager[T any](options ...FinalityPollerOption[T]) FinalityPollerEager[T] {
	var config FinalityPollerConfig[T]
	for _, opt := range options {
		opt(&config)
	}

	if err := configValidation(&config); err != nil {
		panic(err)
	}

	return &eagerFinalityPoller[T]{
		finalityPollFunc: config.finalityPollFunc,
		logger:           config.logger,
		interval:         config.interval,
	}
}

func NewFinalityPollerEagerLegacy[T any](
	finalityPollFunc func(ctx context.Context) (T, error),
	logger log.Logger,
	interval time.Duration,
) FinalityPollerEager[T] {
	return NewFinalityPollerEager(

		WithFinalityPollFunc(finalityPollFunc),
		WithLogger[T](logger),
		WithInterval[T](interval),
	)
}

// LastSnapshot returns the most recently polled snapshot. The bool is false if
// no snapshot has been fetched yet.
func (p *eagerFinalityPoller[T]) LastSnapshot() (T, bool) {
	snapshot := p.finalitySnapshot.Load()
	if snapshot == nil {
		var empty T
		return empty, false
	}
	return *snapshot, true
}

func (p *eagerFinalityPoller[T]) Start(ctx context.Context) {
	nextCtx, nextCancel := context.WithCancel(ctx)
	threadCtx := goroutineContext{
		ctx:    nextCtx,
		cancel: nextCancel,
	}

	if !p.threadCtx.CompareAndSwap(nil, &threadCtx) {
		nextCancel()
		p.logger.Warn("Finality poller is already running or starting")
		return
	}

	threadCtx.wg.Add(1)
	go p.run(&threadCtx)
}

func (p *eagerFinalityPoller[T]) Stop() {
	threadCtx := p.threadCtx.Swap(nil)
	if threadCtx == nil {
		p.logger.Warn("Finality poller is not running or is already stopping")
		return
	}

	p.logger.Info("Stopping Finality Poller")
	threadCtx.cancel()
	threadCtx.wg.Wait()
}

func (p *eagerFinalityPoller[T]) poll(ctx context.Context) {
	fetchCtx, cancel := context.WithTimeout(ctx, finalityPollTimeout)
	defer cancel()

	snapshot, err := p.finalityPollFunc(fetchCtx)
	if err != nil {
		p.logger.Error("failed to fetch finalized block", "error", err)
		return
	}
	p.logger.Debug("finality poller updating", "snapshot", snapshot)
	p.finalitySnapshot.Store(&snapshot)
}

func (p *eagerFinalityPoller[T]) run(c *goroutineContext) {
	ctx := c.ctx
	defer c.wg.Done()
	p.poll(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

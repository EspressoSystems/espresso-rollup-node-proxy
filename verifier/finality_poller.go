package verifier

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

const (
	defaultFinalityPollInterval = time.Second

	finalityPollTimeout = 5 * time.Second
)

// FinalityPollerInterface is the finality poller as consumed by a verifier. T is
// the verifier-specific snapshot type: the OP verifier uses a struct carrying the
// finalized L2 and L1 blocks, while the Nitro verifier uses a plain uint64 block
// number.
type FinalityPollerInterface[T any] interface {
	LastSnapshot() (T, bool)
	Start(ctx context.Context)
	Stop()
}

type FinalityPoller[T any] struct {
	finalityPollFunc func(ctx context.Context) (T, error)
	logger           log.Logger
	interval         time.Duration
	finalitySnapshot atomic.Pointer[T]
	running          atomic.Bool
	cancel           context.CancelFunc
	wg               sync.WaitGroup
}

// Compile-time assertion that *FinalityPoller[T] implements
// FinalityPollerInterface[T].
var _ FinalityPollerInterface[any] = (*FinalityPoller[any])(nil)

func NewFinalityPoller[T any](
	finalityPollFunc func(ctx context.Context) (T, error),
	logger log.Logger,
	interval time.Duration,
) *FinalityPoller[T] {
	if interval == 0 {
		interval = defaultFinalityPollInterval
	}
	return &FinalityPoller[T]{
		finalityPollFunc: finalityPollFunc,
		logger:           logger,
		interval:         interval,
	}
}

// LastSnapshot returns the most recently polled snapshot. The bool is false if
// no snapshot has been fetched yet.
func (p *FinalityPoller[T]) LastSnapshot() (T, bool) {
	snapshot := p.finalitySnapshot.Load()
	if snapshot == nil {
		var empty T
		return empty, false
	}
	return *snapshot, true
}

func (p *FinalityPoller[T]) Start(ctx context.Context) {
	if !p.running.CompareAndSwap(false, true) {
		p.logger.Warn("Finality poller is already running or starting")
		return
	}
	ctx, p.cancel = context.WithCancel(ctx)
	p.wg.Add(1)
	go p.run(ctx)
}

func (p *FinalityPoller[T]) Stop() {
	if !p.running.CompareAndSwap(true, false) {
		p.logger.Warn("Finality poller is not running or is already stopping")
		return
	}
	p.logger.Info("Stopping Finality Poller")
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

func (p *FinalityPoller[T]) poll(ctx context.Context) {
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

func (p *FinalityPoller[T]) run(ctx context.Context) {
	defer p.wg.Done()
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

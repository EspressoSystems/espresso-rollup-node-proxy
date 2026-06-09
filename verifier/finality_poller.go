package verifier

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

const DefaultFinalityPollInterval = time.Second

type LatestSnapshot interface {
	FinalizedL2() uint64
}

type FinalityPollerInterface interface {
	LastSnapshot() LatestSnapshot
	Start(ctx context.Context)
	Stop()
}

// FinalityPoller periodically calls fetch and caches the latest snapshot so
// callers can read finality
type FinalityPoller struct {
	finalityPollFunc func(ctx context.Context) (LatestSnapshot, error)
	logger           log.Logger
	interval         time.Duration
	finalitySnapshot atomic.Value
	running          atomic.Bool
	cancel           context.CancelFunc
	wg               sync.WaitGroup
}

func NewFinalityPoller(
	finalityPollFunc func(ctx context.Context) (LatestSnapshot, error),
	logger log.Logger,
	interval time.Duration,
) *FinalityPoller {
	if interval == 0 {
		interval = DefaultFinalityPollInterval
	}
	return &FinalityPoller{
		finalityPollFunc: finalityPollFunc,
		logger:           logger,
		interval:         interval,
	}
}

// LastSnapshot returns the most recently polled snapshot, or nil if none has been
// fetched yet.
func (p *FinalityPoller) LastSnapshot() LatestSnapshot {
	snapshot, _ := p.finalitySnapshot.Load().(LatestSnapshot)
	return snapshot
}

func (p *FinalityPoller) Start(ctx context.Context) {
	if !p.running.CompareAndSwap(false, true) {
		p.logger.Warn("Finality poller is already running or starting")
		return
	}
	ctx, p.cancel = context.WithCancel(ctx)
	p.wg.Add(1)
	go p.run(ctx)
}

func (p *FinalityPoller) Stop() {
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

func (p *FinalityPoller) run(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot, err := p.finalityPollFunc(ctx)
			if err != nil {
				p.logger.Error("failed to fetch finalized block", "error", err)
				continue
			}
			if snapshot == nil {
				p.logger.Error("fetched snapshot is nil")
				continue
			}
			p.logger.Debug("finality poller updating", "block_num", snapshot.FinalizedL2())
			p.finalitySnapshot.Store(snapshot)
		}
	}
}

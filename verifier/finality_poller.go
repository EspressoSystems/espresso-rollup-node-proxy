package verifier

import (
	"context"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

const DefaultFinalityPollInterval = time.Second

type FinalityPollerInterface interface {
	LastFinalized() uint64
	Start(ctx context.Context)
	Stop()
}

type FinalityPoller struct {
	client   *ethclient.Client
	logger   log.Logger
	interval time.Duration
	last     atomic.Uint64
	running  atomic.Bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func NewFinalityPoller(
	client *ethclient.Client,
	logger log.Logger,
	interval time.Duration,
) *FinalityPoller {
	if interval == 0 {
		interval = DefaultFinalityPollInterval
	}
	return &FinalityPoller{
		client:   client,
		logger:   logger,
		interval: interval,
	}
}

func (p *FinalityPoller) LastFinalized() uint64 {
	return p.last.Load()
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
	p.client.Close()
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
			header, err := p.client.HeaderByNumber(ctx, big.NewInt(rpc.FinalizedBlockNumber.Int64()))
			if err != nil {
				p.logger.Error("failed to fetch finalized block", "error", err)
				continue
			}
			p.logger.Debug("finality poller updating", "block_num", header.Number.Uint64())
			p.last.Store(header.Number.Uint64())
		}
	}
}

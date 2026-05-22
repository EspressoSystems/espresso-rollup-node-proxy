package verifier

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

const DefaultFinalityPollInterval = time.Second

type FinalityPoller struct {
	client *ethclient.Client
	logger log.Logger
	last   atomic.Uint64
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewFinalityPoller(
	client *ethclient.Client,
	logger log.Logger,
) *FinalityPoller {
	return &FinalityPoller{
		client: client,
		logger: logger,
	}
}

func (p *FinalityPoller) LastFinalized() uint64 {
	return p.last.Load()
}

func (p *FinalityPoller) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	p.wg.Add(1)
	go p.run(ctx)
}

func (p *FinalityPoller) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.client.Close()
	p.wg.Wait()
}

func (p *FinalityPoller) run(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(DefaultFinalityPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			header, err := p.client.HeaderByNumber(ctx, big.NewInt(int64(rpc.FinalizedBlockNumber)))
			if err != nil {
				p.logger.Error("failed to fetch finalized block", "error", err)
				continue
			}
			if header == nil {
				p.logger.Error("failed to fetch finalized block", "error", fmt.Errorf("header is nil"))
				continue
			}
			p.logger.Debug("finality poller updating", "num", header.Number.Uint64())
			p.last.Store(header.Number.Uint64())
		}
	}
}

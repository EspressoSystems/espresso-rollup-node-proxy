package op

import (
	"context"
	"math/big"
	"time"

	espressostore "proxy/espresso_store"
	opstreamer "proxy/streamer/op"

	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
)

// Runner polls the OP BatchStreamer and advances the EspressoStore as
// batches are confirmed from Espresso.
type Runner struct {
	streamer *opstreamer.BatchStreamer[opstreamer.EspressoBatch]
	store    *espressostore.EspressoStore
	l1       *ethclient.Client
	interval time.Duration
}

func NewRunner(
	streamer *opstreamer.BatchStreamer[opstreamer.EspressoBatch],
	store *espressostore.EspressoStore,
	l1 *ethclient.Client,
	interval time.Duration,
) *Runner {
	return &Runner{
		streamer: streamer,
		store:    store,
		l1:       l1,
		interval: interval,
	}
}

func (r *Runner) Start(ctx context.Context) {
	go r.run(ctx)
}

func (r *Runner) run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.poll(ctx)
		}
	}
}

func (r *Runner) poll(ctx context.Context) {
	// Fetch the current finalized L1 block and update the streamer before polling.
	finalizedL1, err := r.fetchFinalizedL1(ctx)
	if err != nil {
		log.Warn("failed to fetch finalized L1 block", "err", err)
		return
	}
	r.streamer.FinalizedL1 = finalizedL1

	if err := r.streamer.Update(ctx); err != nil {
		log.Warn("op streamer update failed", "err", err)
		return
	}

	for r.streamer.HasNext(ctx) {
		batch := r.streamer.Next(ctx)
		if batch == nil {
			continue
		}
		b := *batch
		if err := r.store.Update(b.Number(), r.streamer.HotShotPos()); err != nil {
			log.Warn("failed to update espresso store", "err", err)
		}
		log.Info("op batch confirmed", "number", b.Number(), "hotshotPos", r.streamer.HotShotPos())
	}
}

func (r *Runner) fetchFinalizedL1(ctx context.Context) (eth.L1BlockRef, error) {
	header, err := r.l1.HeaderByNumber(ctx, big.NewInt(-3)) // -3 = "finalized"
	if err != nil {
		return eth.L1BlockRef{}, err
	}
	return eth.L1BlockRef{
		Hash:   header.Hash(),
		Number: header.Number.Uint64(),
		Time:   header.Time,
	}, nil
}

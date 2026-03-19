package verifier

import (
	"bytes"
	"context"
	"math/big"
	espressostore "proxy/espresso_store"
	op "proxy/streamer/op"
	"time"

	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-service/dial"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

type Verifier struct {
	streamer         op.EspressoStreamer[op.EspressoBatch]
	espressoStore    *espressostore.EspressoStore
	interval         time.Duration
	endpointProvider dial.L2EndpointProvider
	rollupConfig     *rollup.Config
	logger           log.Logger
}

func NewVerifier(ctx context.Context, streamer op.EspressoStreamer[op.EspressoBatch],
	logger log.Logger,
	espressoStore *espressostore.EspressoStore,
	fullNodeExecutionRPC string,
	fullNodeConsensusRPC string,
	interval time.Duration) *Verifier {
	endpointProvider, err := dial.NewStaticL2EndpointProvider(ctx, logger, fullNodeExecutionRPC, fullNodeConsensusRPC)
	if err != nil {
		logger.Crit("failed to build  L2 endpoint provider: %w", err)
	}
	// Get rollup config from consensus client
	consensusClient, err := endpointProvider.RollupClient(ctx)
	if err != nil {
		logger.Crit("failed to get consensus client", "error", err)
	}
	rollupConfig, err := consensusClient.RollupConfig(ctx)
	if err != nil {
		logger.Crit("failed to get rollup config from consensus client", "error", err)
	}
	if rollupConfig == nil {
		logger.Crit("rollup config is nil")
	}

	return &Verifier{
		streamer:         streamer,
		espressoStore:    espressoStore,
		interval:         interval,
		endpointProvider: endpointProvider,
		rollupConfig:     rollupConfig,
		logger:           logger,
	}
}

func (v *Verifier) Start(ctx context.Context) {
	go v.run(ctx)
}

func (v *Verifier) run(ctx context.Context) {
	ticker := time.NewTicker(v.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			v.verify(ctx)
		}
	}
}

// verify fetches the next batch from the streamer, gets the corresponding block from the execution client,
// converts it to an EspressoBatch, and compares the two batches.
// If they match, it updates the espresso store with the batch number and the earliest hotshot height.
func (v *Verifier) verify(ctx context.Context) {
	// First get the message from the streamer
	espressoBatch := v.streamer.Peek(ctx)
	if espressoBatch == nil {
		return
	}
	ethClient, err := v.endpointProvider.EthClient(ctx)
	if err != nil {
		v.logger.Error("failed to get eth client", "error", err)
		return
	}

	block, err := ethClient.BlockByNumber(ctx, new(big.Int).SetUint64((*espressoBatch).Number()))
	if err != nil {
		v.logger.Error("failed to fetch block from execution client", "error", err, "block number", (*espressoBatch).Number())
		return
	}

	// Convert block to EspressoBatch
	fullNodeEspressobatch, err := op.BlockToEspressoBatch(v.rollupConfig, block)
	if err != nil {
		v.logger.Error("failed to convert block to espresso batch", "error", err, "block number", block.NumberU64())
		return
	}
	// Encode both batches to compare them
	espressoBatchBuf := new(bytes.Buffer)
	err = rlp.Encode(espressoBatchBuf, espressoBatch)
	if err != nil {
		v.logger.Error("failed to encode espresso batch", "error", err, "batch number", (*espressoBatch).Number())
		return
	}

	fullNodeEspressobatchBuf := new(bytes.Buffer)
	err = rlp.Encode(fullNodeEspressobatchBuf, fullNodeEspressobatch)
	if err != nil {
		v.logger.Error("failed to encode full node espresso batch", "error", err, "batch number", fullNodeEspressobatch.Number())
		return
	}

	if !bytes.Equal(espressoBatchBuf.Bytes(), fullNodeEspressobatchBuf.Bytes()) {
		v.logger.Error("batch mismatch", "espressoBatch", espressoBatch, "fullNodeEspressobatch", fullNodeEspressobatch, "batch number", (*espressoBatch).Number())
	}

	// Update the espresso store with the batch number and the earliest hotshot height
	// TODO: we should think about if we should add mutexes to streamer code from OP because multiple threads
	// are accesing values now
	if err := v.espressoStore.Update((*espressoBatch).Number(), v.streamer.GetFallbackHotShotPos()); err != nil {
		v.logger.Error("failed to store batch in espresso store", "error", err, "batch number", (*espressoBatch).Number())
		return
	}
	// we will advance the streamer now to next pos
	v.streamer.Next(ctx)
}

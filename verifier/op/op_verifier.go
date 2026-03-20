package verifier

import (
	"bytes"
	"context"
	"math/big"
	espressoStore "proxy/store"
	opStreamer "proxy/streamer/op"
	"time"

	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-service/dial"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	espressoLightClient "github.com/EspressoSystems/espresso-network/sdks/go/light-client"
)

type OpVerifierConfig struct {
	L1RPC                string        `json:"l1_rpc"`
	FullNodeExecutionRPC string        `json:"full_node_execution_rpc"`
	FullNodeConsensusRPC string        `json:"full_node_consensus_rpc"`
	VerificationInterval time.Duration `json:"verification_interval"`
	QueryServiceURL      string        `json:"query_service_url"`
	LightClientAddress   string        `json:"light_client_address"`
	BatcherAddress       string        `json:"batcher_address"`
}

type OpVerifier struct {
	streamer         opStreamer.EspressoStreamer[opStreamer.EspressoBatch]
	espressoStore    *espressoStore.EspressoStore
	opVerifierConfig *OpVerifierConfig
	endpointProvider dial.L2EndpointProvider
	rollupConfig     *rollup.Config
	logger           log.Logger
	l1Client         *ethclient.Client
	cancel           context.CancelFunc
	done             chan struct{}
}

func NewVerifier(ctx context.Context, logger log.Logger, store *espressoStore.EspressoStore, opVerifierConfig *OpVerifierConfig) *OpVerifier {
	if opVerifierConfig == nil {
		logger.Crit("OP Verifier config is nil")
		return nil
	}
	// Create the endpoint provider for the OP node
	endpointProvider, err := dial.NewStaticL2EndpointProvider(ctx, logger,
		opVerifierConfig.FullNodeExecutionRPC, opVerifierConfig.FullNodeConsensusRPC)
	if err != nil {
		logger.Crit("failed to create endpoint provider", "error", err)
		return nil
	}

	// Read the rollup config from the OP node
	consensusClient, err := endpointProvider.RollupClient(ctx)
	if err != nil {
		logger.Crit("failed to create consensus client", "error", err)
		return nil
	}
	defer consensusClient.Close()
	rollupConfig, err := consensusClient.RollupConfig(ctx)
	if err != nil {
		logger.Crit("failed to read rollup config", "error", err)
		return nil
	}
	if rollupConfig == nil {
		logger.Crit("Rollup config is nil")
		return nil
	}

	// Create an L1 client
	l1Client, err := ethclient.DialContext(ctx, opVerifierConfig.L1RPC)
	if err != nil {
		logger.Crit("failed to create L1 client", "error", err)
		return nil
	}

	// Create an espresso client
	espressoClient := espressoClient.NewClient(opVerifierConfig.QueryServiceURL)
	if espressoClient == nil {
		logger.Crit("failed to create Espresso client")
		return nil
	}
	// Create light client interface
	lightClientAddr := common.HexToAddress(opVerifierConfig.LightClientAddress)
	espressoLightClient, err := espressoLightClient.NewLightclientCaller(lightClientAddr, l1Client)
	if err != nil || espressoLightClient == nil {
		logger.Crit("failed to create light client")
		return nil
	}

	batcherAddr := common.HexToAddress(opVerifierConfig.BatcherAddress)
	espressoState, err := store.GetState()
	if err != nil {
		logger.Crit("failed to get state from store", "error", err)
		return nil
	}
	// Create the OP streamer
	streamer := opStreamer.NewEspressoStreamer(rollupConfig.L2ChainID.Uint64(),
		NewAdaptL1BlockRefClient(l1Client),
		NewAdaptL1BlockRefClient(l1Client),
		espressoClient,
		espressoLightClient,
		logger,
		opStreamer.CreateEspressoBatchUnmarshaler(batcherAddr),
		espressoState.L2BlockNumber,
		espressoState.FallbackHotshotHeight,
	)

	return &OpVerifier{
		streamer:         streamer,
		espressoStore:    store,
		opVerifierConfig: opVerifierConfig,
		endpointProvider: endpointProvider,
		rollupConfig:     rollupConfig,
		logger:           logger,
		l1Client:         l1Client,
	}
}

func (v *OpVerifier) Start(ctx context.Context) {
	v.logger.Info("Starting OP Verifier")
	ctx, cancel := context.WithCancel(ctx)
	v.cancel = cancel
	v.done = make(chan struct{})
	go v.run(ctx)
}

func (v *OpVerifier) run(ctx context.Context) {
	defer close(v.done)
	ticker := time.NewTicker(v.opVerifierConfig.VerificationInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			v.verify(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (v *OpVerifier) verify(ctx context.Context) {
	v.logger.Info("Starting OP batch verification")

	// Peek the next batch from the OP streamer
	espressoBatch, err := v.peekNextBatch(ctx)
	if err != nil {
		v.logger.Error("failed to peek next batch", "error", err)
		return
	}
	if espressoBatch == nil {
		v.logger.Info("No new batches to verify")
		return
	}

	ethClient, err := v.endpointProvider.EthClient(ctx)
	if err != nil {
		v.logger.Error("failed to create eth client", "error", err)
		return
	}
	defer ethClient.Close()

	block, err := ethClient.BlockByNumber(ctx, new(big.Int).SetUint64((*espressoBatch).Number()))
	if err != nil {
		v.logger.Error("failed to get block by number", "error", err, "block_number", (*espressoBatch).Number())
		return
	}

	// Convert block to EspressoBatch
	fullNodeEspressoBatch, err := opStreamer.BlockToEspressoBatch(v.rollupConfig, block)
	if err != nil {
		v.logger.Error("failed to convert block to EspressoBatch", "error", err, "block_number", (*espressoBatch).Number())
		return
	}

	// Encode both batches to compare them
	espressoBatchBuf := new(bytes.Buffer)
	err = rlp.Encode(espressoBatchBuf, espressoBatch)
	if err != nil {
		v.logger.Error("failed to encode espresso batch", "error", err, "batch number", (*espressoBatch).Number())
		return
	}

	fullNodeBatchBuf := new(bytes.Buffer)
	err = rlp.Encode(fullNodeBatchBuf, fullNodeEspressoBatch)
	if err != nil {
		v.logger.Error("failed to encode full node batch", "error", err, "batch number", (*espressoBatch).Number())
		return
	}

	if !bytes.Equal(espressoBatchBuf.Bytes(), fullNodeBatchBuf.Bytes()) {
		v.logger.Error("Batch verification failed: OP batch does not match full node batch", "batch_number", (*espressoBatch).Number(), "block_hash", block.Hash())
		return
	}

	// If they match then advance the streamer and update the espresso state in the store
	err = v.advanceStreamerAndEspressoState(ctx, (*espressoBatch).Number())
	if err != nil {
		v.logger.Error("failed to advance streamer and espresso state", "error", err, "batch_number", (*espressoBatch).Number())
		return
	}

	v.logger.Info("Successfully verified OP batch", "batch_number", (*espressoBatch).Number(), "block_hash", block.Hash())

}

// peekNextBatch follows the pattern  getSyncStatus -> refresh -> Update -> Peek
// It doesnt call Next because Proxy only calls Next if the full node block matches
// what Espresso has finalized, otherwise it remains stuck on the same batch until the OP node catches up.
func (v *OpVerifier) peekNextBatch(ctx context.Context) (*opStreamer.EspressoBatch, error) {
	// Get the latest L2 block ref from the OP node
	rollupClient, err := v.endpointProvider.RollupClient(ctx)
	if err != nil {
		v.logger.Error("failed to create consensus client", "error", err)
		return nil, err
	}
	defer rollupClient.Close()
	syncStatus, err := rollupClient.SyncStatus(ctx)
	if err != nil {
		v.logger.Error("failed to get L2 head block", "error", err)
		return nil, err
	}

	err = v.streamer.Refresh(ctx, syncStatus.FinalizedL1, syncStatus.SafeL2.Number, syncStatus.SafeL2.L1Origin)
	if err != nil {
		v.logger.Error("failed to refresh OP streamer", "error", err)
		return nil, err
	}

	if !v.streamer.HasNext(ctx) {
		err := v.streamer.Update(ctx)
		if err != nil {
			v.logger.Error("failed to update OP streamer", "error", err)
			return nil, err
		}
	}

	// Now we Peek the next batch and return it for verification
	espressoBatchStreamer := v.streamer.Peek(ctx)

	return espressoBatchStreamer, nil
}

// advanceStreamerAndEspressoState advances the OP streamer to the next batch
// and updates the espresso state in the store to reflect the new batch number.
// This is called after a successful verification to move on to the next batch.
func (v *OpVerifier) advanceStreamerAndEspressoState(ctx context.Context, blockNumber uint64) error {
	hotshotFallbackPos := v.streamer.GetFallbackHotshotPos()

	// Update the espresso state in the store to reflect the new batch number
	err := v.espressoStore.Update(blockNumber, hotshotFallbackPos)
	if err != nil {
		v.logger.Error("failed to update espresso state in store", "error", err)
		return err
	}

	// Advance the streamer to the next batch
	v.streamer.Next(ctx)

	return nil
}

func (v *OpVerifier) Stop() {
	v.logger.Info("Stopping OP Verifier")
	v.cancel()
	<-v.done

	v.endpointProvider.Close()
	v.l1Client.Close()
	v.logger.Info("OP Verifier stopped")
}

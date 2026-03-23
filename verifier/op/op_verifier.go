package verifier

import (
	"bytes"
	"context"
	"math/big"
	espressoStore "proxy/store"
	opStreamer "proxy/streamer/op"
	"sync"
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

type OPEspressoBatchVerifierConfig struct {
	L1RPC                string        `json:"l1_rpc"`
	FullNodeExecutionRPC string        `json:"full_node_execution_rpc"`
	FullNodeConsensusRPC string        `json:"full_node_consensus_rpc"`
	VerificationInterval time.Duration `json:"verification_interval"`
	QueryServiceURL      string        `json:"query_service_url"`
	LightClientAddress   string        `json:"light_client_address"`
	BatcherAddress       string        `json:"batcher_address"`
}

// OPEspressoBatchVerifier is responsible for verifying that the batches produced by the OP full node match what the OP streamer has in its buffer.
// It does this by periodically peeking the next batch from the OP streamer, fetching the corresponding block from the OP node,
// converting it to an EspressoBatch and comparing the two.
// If they match, it advances the OP streamer and updates the espresso state in the store to reflect the new block number relative to the espresso tag.
// If they dont match, it logs an error and tries again on the next interval. Eventually the tag will be advanced after
// a batch is posted to Ethereum and it finalizes because Ethereum will only finalize data that matches the data finalized by Espresso.
type OPEspressoBatchVerifier struct {
	streamer         opStreamer.EspressoStreamer[opStreamer.EspressoBatch]
	espressoStore    *espressoStore.EspressoStore
	config           *OPEspressoBatchVerifierConfig
	endpointProvider dial.L2EndpointProvider
	rollupConfig     *rollup.Config
	logger           log.Logger
	l1Client         *ethclient.Client
	cancel           context.CancelFunc
	runWg            sync.WaitGroup
	running          bool
}

func NewOPEspressoBatchVerifier(ctx context.Context, logger log.Logger, store *espressoStore.EspressoStore, opVerifierConfig *OPEspressoBatchVerifierConfig) *OPEspressoBatchVerifier {
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
		espressoState.FallbackHotshotHeight,
		espressoState.L2BlockNumber,
	)

	return &OPEspressoBatchVerifier{
		streamer:         streamer,
		espressoStore:    store,
		config:           opVerifierConfig,
		endpointProvider: endpointProvider,
		rollupConfig:     rollupConfig,
		logger:           logger,
		l1Client:         l1Client,
	}
}

func (v *OPEspressoBatchVerifier) Start(ctx context.Context) {
	if v.running {
		v.logger.Warn("OP Verifier is already running")
		return
	}
	v.logger.Info("Starting OP Verifier")
	v.running = true
	ctx, cancel := context.WithCancel(ctx)
	v.cancel = cancel
	v.runWg.Add(1)
	go v.run(ctx)
}

func (v *OPEspressoBatchVerifier) run(ctx context.Context) {
	defer v.runWg.Done()
	ticker := time.NewTicker(v.config.VerificationInterval)
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

// verify performs a  verification cycle:
// 1. Peeks the next batch from the OP streamer
// 2. Fetches the corresponding block from the OP node and converts it to an EspressoBatch
// 3. Compares the two batches by RLP-encoding them and checking for byte-for-byte equality
// 4. If they match, advances the OP streamer and updates the espresso state in the store to reflect the new batch number
// 5. If they dont match, logs an error and tries again on the next interval
func (v *OPEspressoBatchVerifier) verify(ctx context.Context) {
	v.logger.Info("Starting OP batch verification")

	// Get next batch from Espresso
	espressoBatch, err := v.peekNextBatch(ctx)
	if err != nil {
		v.logger.Error("failed to peek next batch", "error", err)
		return
	}
	if espressoBatch == nil {
		v.logger.Info("No new batches to verify")
		return
	}

	// Get the corresponding batch from the L2 full node
	fullNodeBatch, err := v.getFullNodeBatch(ctx, (*espressoBatch).Number())
	if err != nil {
		v.logger.Error("failed to get full node batch", "error", err, "block_number", (*espressoBatch).Number())
		return
	}

	// Compare the two batches
	match, err := batchesMatch(espressoBatch, fullNodeBatch)
	if err != nil {
		v.logger.Error("failed to compare batches", "error", err, "batch_number", (*espressoBatch).Number())
		return
	}
	if !match {
		v.logger.Error("Batch verification failed: Espresso batch does not match full node batch", "batch_number", (*espressoBatch).Number())
		return
	}

	// Advance on success
	err = v.advanceStreamerAndEspressoState(ctx, (*espressoBatch).Number())
	if err != nil {
		v.logger.Error("failed to advance streamer and espresso state", "error", err, "batch_number", (*espressoBatch).Number())
		return
	}

	v.logger.Info("Successfully verified OP batch", "batch_number", (*espressoBatch).Number())
}

// getFullNodeBatch fetches the block at the given number from the L2 full node
// and converts it to an EspressoBatch for comparison.
func (v *OPEspressoBatchVerifier) getFullNodeBatch(ctx context.Context, blockNumber uint64) (*opStreamer.EspressoBatch, error) {
	ethClient, err := v.endpointProvider.EthClient(ctx)
	if err != nil {
		return nil, err
	}
	defer ethClient.Close()

	block, err := ethClient.BlockByNumber(ctx, new(big.Int).SetUint64(blockNumber))
	if err != nil {
		return nil, err
	}

	batch, err := opStreamer.BlockToEspressoBatch(v.rollupConfig, block)
	if err != nil {
		return nil, err
	}

	return batch, nil
}

// batchesMatch RLP-encodes both batches and compares them byte-for-byte.
func batchesMatch(a, b *opStreamer.EspressoBatch) (bool, error) {
	aBuf := new(bytes.Buffer)
	if err := rlp.Encode(aBuf, a); err != nil {
		return false, err
	}

	bBuf := new(bytes.Buffer)
	if err := rlp.Encode(bBuf, b); err != nil {
		return false, err
	}

	return bytes.Equal(aBuf.Bytes(), bBuf.Bytes()), nil
}

// peekNextBatch follows the pattern  getSyncStatus -> refresh -> Update -> Peek
// It doesnt call Next because Proxy only calls Next if the full node block matches
// what Espresso has finalized, otherwise it remains stuck on the same batch until the OP node catches up.
func (v *OPEspressoBatchVerifier) peekNextBatch(ctx context.Context) (*opStreamer.EspressoBatch, error) {
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
func (v *OPEspressoBatchVerifier) advanceStreamerAndEspressoState(ctx context.Context, blockNumber uint64) error {
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

func (v *OPEspressoBatchVerifier) Stop() {
	if !v.running {
		v.logger.Warn("OP Verifier is not running")
		return
	}
	v.logger.Info("Stopping OP Verifier")
	v.cancel()
	v.runWg.Wait()
	v.running = false

	v.endpointProvider.Close()
	v.l1Client.Close()
	v.logger.Info("OP Verifier stopped")
}

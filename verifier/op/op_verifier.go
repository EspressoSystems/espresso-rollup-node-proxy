package verifier

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	espressoStore "proxy/store"
	"strings"
	"sync"
	"time"

	opStreamer "github.com/EspressoSystems/espresso-streamers/op"
	"github.com/EspressoSystems/espresso-streamers/op/derivation"

	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-service/dial"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
)

type OPEspressoBatchVerifierConfig struct {
	L1RPC                     string        `json:"l1_rpc"`
	FullNodeExecutionRPC      string        `json:"full_node_execution_rpc"`
	FullNodeConsensusRPC      string        `json:"full_node_consensus_rpc"`
	VerificationInterval      time.Duration `json:"verification_interval"`
	QueryServiceURL           string        `json:"query_service_url"`
	BatcherAddress            string        `json:"batcher_address"`
	BatchAuthenticatorAddress string        `json:"batch_authenticator_address"`
	TrackBatchLatency         bool          `json:"track_batch_latency"`
}

// OPEspressoBatchVerifier is responsible for verifying that the batches produced by the OP full node match what the OP streamer has in its buffer.
// It does this by periodically peeking the next batch from the OP streamer, fetching the corresponding block from the OP node,
// converting it to an EspressoBatch and comparing the two.
// If they match, it advances the OP streamer and updates the espresso state in the store to reflect the new block number relative to the espresso tag.
// If they dont match, it logs an error and tries again on the next interval. Eventually the tag will be advanced after
// a batch is posted to Ethereum and it finalizes because Ethereum will only finalize data that matches the data finalized by Espresso.
type OPEspressoBatchVerifier struct {
	streamer          opStreamer.EspressoStreamer[derivation.EspressoBatch]
	espressoStore     *espressoStore.EspressoStore
	config            *OPEspressoBatchVerifierConfig
	endpointProvider  dial.L2EndpointProvider
	rollupConfig      *rollup.Config
	logger            log.Logger
	l1Client          *ethclient.Client
	cancel            context.CancelFunc
	runWg             sync.WaitGroup
	running           bool
	totalBatchLatency time.Duration
	batchCount        uint64
}

func NewOPEspressoBatchVerifier(ctx context.Context, logger log.Logger, store *espressoStore.EspressoStore, l1Client *ethclient.Client, espressoLightClient opStreamer.LightClientCallerInterface, opVerifierConfig *OPEspressoBatchVerifierConfig) *OPEspressoBatchVerifier {
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

	// Create an espresso client
	espressoClient := espressoClient.NewClient(opVerifierConfig.QueryServiceURL)
	if espressoClient == nil {
		logger.Crit("failed to create Espresso client")
		return nil
	}

	espressoState := store.GetState()

	batchAuthenticatorAddr := common.HexToAddress(opVerifierConfig.BatchAuthenticatorAddress)
	l1Adapter := NewAdaptL1BlockRefClient(l1Client)

	// Create the OP streamer
	streamer, err := opStreamer.NewEspressoStreamer(
		rollupConfig.L2ChainID.Uint64(),
		l1Adapter,
		l1Adapter,
		espressoClient,
		espressoLightClient,
		logger,
		derivation.CreateEspressoBatchUnmarshaler(),
		espressoState.FallbackHotshotHeight,
		espressoState.L2BlockNumber,
		batchAuthenticatorAddr,
		opVerifierConfig.TrackBatchLatency,
	)

	if err != nil {
		logger.Crit("failed to create OP streamer", "error", err)
		return nil
	}

	bufferedStreamer := opStreamer.NewBufferedEspressoStreamer(streamer)

	return &OPEspressoBatchVerifier{
		streamer:         bufferedStreamer,
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
	espressoState := v.espressoStore.GetState()

	v.logger.Info("Starting OP Verifier", "start block number", espressoState.L2BlockNumber, "starting fallback_hotshot_height", espressoState.FallbackHotshotHeight)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			v.verifyAndAdvance(ctx)
		}
	}
}

// verifyAndAdvance calls VerifyNextBatch to peek the next batch from the OP streamer and verify it against the OP node.
// If verification succeeds, it advances the OP streamer and updates the espresso state in the store to reflect the new batch number.
// If verification fails, it logs an error and will try again on the next interval.
func (v *OPEspressoBatchVerifier) verifyAndAdvance(ctx context.Context) {
	v.logger.Debug("Starting OP batch verification")

	ethFinalizedBlockNumber, err := v.getEthereumFinalizedBlockNumber(ctx)
	if err != nil {
		v.logger.Error("failed to fetch ethereum finalized block for verifier", "error", err)
		return
	}

	var espressoBatch *derivation.EspressoBatch
	if espressoBatch, err = v.VerifyNextBatch(ctx); err != nil {
		if err.Error() == "not found" {
			v.logger.Debug("batch not found on OP node yet, will try again on next interval")
			return
		} else if strings.Contains(err.Error(), "retryable") {
			v.logger.Debug("espresso has not finalized the batch yet", "error", err)
			return
		} else {
			v.logger.Error("batch verification failed", "error", err)
		}
		return
	}
	if espressoBatch == nil {
		if err := v.syncEspressoStateWithEthereumFinality(ethFinalizedBlockNumber); err != nil {
			v.logger.Error("failed to update espresso state to ethereum finalized block", "error", err, "eth_finalized_block", ethFinalizedBlockNumber)
		}
		v.logger.Debug("no new batches to verify")
		return
	}

	batchNumber := espressoBatch.Number()

	var hotshotTimestamp uint64
	var hashTimestamp bool
	if v.config.TrackBatchLatency {
		hotshotTimestamp, hashTimestamp = v.streamer.GetBatchFinalizationTimestamp(espressoBatch.Hash())
	}

	if err := v.advanceStreamerAndEspressoState(ctx, batchNumber, ethFinalizedBlockNumber); err != nil {
		v.logger.Debug("failed to advance streamer and espresso state", "error", err, "batch_number", batchNumber)
		return
	}

	if v.config.TrackBatchLatency && hashTimestamp {
		latency := time.Since(time.Unix(int64(hotshotTimestamp), 0))
		v.totalBatchLatency += latency
		v.batchCount++
		averageLatency := v.totalBatchLatency / time.Duration(v.batchCount)
		v.logger.Info("Batch latency", "batch_number", batchNumber, "latency", latency, "average_latency", averageLatency, "total batches", v.batchCount, "hotshot_timestamp", hotshotTimestamp, "batch_hash", espressoBatch.Hash())
	}

	v.logger.Info("Successfully verified and advanced OP batch", "batch_number", batchNumber)
}

func (v *OPEspressoBatchVerifier) getEthereumFinalizedBlockNumber(ctx context.Context) (uint64, error) {
	ethClient, err := v.endpointProvider.EthClient(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get eth client for finalized block check: %w", err)
	}
	defer ethClient.Close()

	ethFinalizedBlock, err := ethClient.BlockByNumber(ctx, big.NewInt(int64(rpc.FinalizedBlockNumber)))
	if err != nil {
		return 0, fmt.Errorf("failed to fetch ethereum finalized block: %w", err)
	}
	if ethFinalizedBlock == nil {
		return 0, fmt.Errorf("ethereum finalized block not found")
	}

	return ethFinalizedBlock.NumberU64(), nil
}

func (v *OPEspressoBatchVerifier) syncEspressoStateWithEthereumFinality(ethFinalizedBlockNumber uint64) error {
	espressoState := v.espressoStore.GetState()
	blockNumberToStore := v.blockNumberToStore(espressoState.L2BlockNumber, ethFinalizedBlockNumber)
	if blockNumberToStore <= espressoState.L2BlockNumber {
		return nil
	}

	return v.espressoStore.Update(blockNumberToStore, v.streamer.GetFallbackHotshotPos())
}

func (v *OPEspressoBatchVerifier) blockNumberToStore(espressoFinalizedBlockNumber uint64, ethFinalizedBlockNumber uint64) uint64 {
	blockNumberToStore := espressoFinalizedBlockNumber
	if ethFinalizedBlockNumber > espressoFinalizedBlockNumber {
		v.logger.Error("ethereum finalized block is ahead of espresso finalized block",
			"eth_finalized_block", ethFinalizedBlockNumber,
			"espresso_finalized_block", espressoFinalizedBlockNumber)
		blockNumberToStore = ethFinalizedBlockNumber
	}

	return blockNumberToStore
}

// VerifyNextBatch peeks the next batch from the OP streamer, fetches the corresponding block from the OP node,
// converts it to an EspressoBatch and compares the two. If they match, it returns the batch for further processing (advancing streamer and updating state).
// If they dont match, it returns an error.
func (v *OPEspressoBatchVerifier) VerifyNextBatch(ctx context.Context) (*derivation.EspressoBatch, error) {
	// Peek the next batch from the OP streamer without advancing it
	espressoBatch, err := v.peekNextBatch(ctx)
	if err != nil {
		return nil, err
	}
	// No new batch to verify, just return
	if espressoBatch == nil {
		return nil, nil
	}
	batchNumber := espressoBatch.Number()
	// Fetch the corresponding block from the OP node and convert it to an EspressoBatch
	fullNodeBatch, err := v.getFullNodeBatch(ctx, batchNumber)
	if err != nil {
		return nil, err
	}
	// Compare the two batches by RLP-encoding them and checking for byte-for-byte equality
	if err = ensureBatchesMatch(espressoBatch, fullNodeBatch); err != nil {
		return nil, fmt.Errorf("batch verification failed for batch number %d: %w", batchNumber, err)
	}
	return espressoBatch, nil
}

// getFullNodeBatch fetches the block at the given number from the L2 full node
// and converts it to an EspressoBatch for comparison.
func (v *OPEspressoBatchVerifier) getFullNodeBatch(ctx context.Context, blockNumber uint64) (*derivation.EspressoBatch, error) {
	ethClient, err := v.endpointProvider.EthClient(ctx)
	if err != nil {
		return nil, err
	}
	defer ethClient.Close()

	block, err := ethClient.BlockByNumber(ctx, new(big.Int).SetUint64(blockNumber))
	if err != nil {
		return nil, err
	}

	batch, err := derivation.BlockToEspressoBatch(v.rollupConfig, block)
	if err != nil {
		return nil, err
	}

	return batch, nil
}

// ensureBatchesMatch RLP-encodes both batches and compares them byte-for-byte.
// SignerAddress is zeroed on both sides before comparison because the streamer
// batch has it set from signature recovery while the full-node batch does not.
func ensureBatchesMatch(a, b *derivation.EspressoBatch) error {
	aCopy := *a
	bCopy := *b
	aCopy.SignerAddress = common.Address{}
	bCopy.SignerAddress = common.Address{}

	aBuf := new(bytes.Buffer)
	if err := rlp.Encode(aBuf, &aCopy); err != nil {
		return err
	}

	bBuf := new(bytes.Buffer)
	if err := rlp.Encode(bBuf, &bCopy); err != nil {
		return err
	}

	if !bytes.Equal(aBuf.Bytes(), bBuf.Bytes()) {
		return errors.New("espresso batch does not match full node batch")
	}
	return nil
}

// peekNextBatch follows the pattern  getSyncStatus -> refresh -> Update -> Peek
// It doesnt call Next because Proxy only calls Next if the full node block matches
// what Espresso has finalized, otherwise it remains stuck on the same batch until the OP node catches up.
func (v *OPEspressoBatchVerifier) peekNextBatch(ctx context.Context) (*derivation.EspressoBatch, error) {
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
func (v *OPEspressoBatchVerifier) advanceStreamerAndEspressoState(ctx context.Context, blockNumber uint64, ethFinalizedBlockNumber uint64) error {
	hotshotFallbackPos := v.streamer.GetFallbackHotshotPos()
	blockNumberToStore := v.blockNumberToStore(blockNumber, ethFinalizedBlockNumber)

	espressoState := v.espressoStore.GetState()
	if espressoState.L2BlockNumber >= blockNumberToStore {
		v.logger.Warn("not updating espresso state in store because block number is not greater than current block number in store",
			"current_block_number", espressoState.L2BlockNumber, "new_block_number", blockNumberToStore)
		v.streamer.Next(ctx)
		return nil
	}

	err := v.espressoStore.Update(blockNumberToStore, hotshotFallbackPos)
	if err != nil {
		v.logger.Error("failed to update espresso state in store", "error", err)
		return err
	}

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

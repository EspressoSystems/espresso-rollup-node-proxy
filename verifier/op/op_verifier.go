package verifier

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	espressoStore "github.com/EspressoSystems/espresso-rollup-node-proxy/store"

	opStreamer "github.com/EspressoSystems/espresso-streamers/op"
	"github.com/EspressoSystems/espresso-streamers/op/derivation"

	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"

	sharedVerifier "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
)

// ErrForkMismatch is returned by peekNextBatch when the next batch does not
// build on the last verified head (tip). The caller is expected to reposition
// the streamer to the proper head via SetProperHead.
var ErrForkMismatch = errors.New("head batch fork mismatch")

type OPEspressoBatchVerifierConfig struct {
	EthRPC                    string         `json:"eth_rpc"`
	FullNodeExecutionRPC      string         `json:"full_node_execution_rpc"`
	Namespace                 uint64         `json:"namespace"`
	VerificationInterval      time.Duration  `json:"verification_interval"`
	FinalityPollInterval      time.Duration  `json:"finality_poll_interval"`
	QueryServiceURL           string         `json:"query_service_url"`
	BatcherAddress            common.Address `json:"batcher_address"`
	BatchAuthenticatorAddress common.Address `json:"batch_authenticator_address"`
	TrackBatchLatency         bool           `json:"track_batch_latency"`
}

type ExecutionClient interface {
	BlockByNumber(ctx context.Context, number *big.Int) (*types.Block, error)
	Close()
}

// opFinalitySnapshot is the finality data the poller caches for the OP verifier.
// It is sourced from the L2 execution node (finalized L2 block) and the L1 RPC
// (finalized L1 block).
type opFinalitySnapshot struct {
	finalizedL2Number  uint64
	finalizedL2Hash    common.Hash
	finalizedEthOrigin eth.BlockID
	finalizedEth       eth.L1BlockRef
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
	l2Client          ExecutionClient
	logger            log.Logger
	l1Client          *ethclient.Client
	finalityPoller    sharedVerifier.FinalityPollerInterface[opFinalitySnapshot]
	cancel            context.CancelFunc
	runWg             sync.WaitGroup
	running           atomic.Bool
	totalBatchLatency time.Duration
	batchCount        uint64
	// tip is the header hash of the last successfully verified batch,
	tip       common.Hash
	ethOrigin eth.BlockID
}

func NewOPEspressoBatchVerifier(ctx context.Context, logger log.Logger, store *espressoStore.EspressoStore, l1Client *ethclient.Client, espressoLightClient opStreamer.LightClientCallerInterface, opVerifierConfig *OPEspressoBatchVerifierConfig) *OPEspressoBatchVerifier {
	if opVerifierConfig == nil {
		logger.Crit("OP Verifier config is nil")
		return nil
	}
	// Dial the L2 execution (op-geth) RPC. This is the only L2 endpoint the
	// verifier needs; the finalized L2/L1 blocks come from here and the L1 RPC.
	l2Client, err := ethclient.DialContext(ctx, opVerifierConfig.FullNodeExecutionRPC)
	if err != nil {
		logger.Crit("failed to dial L2 execution RPC", "error", err)
		return nil
	}

	// Create an espresso client
	espressoClient := espressoClient.NewClient(opVerifierConfig.QueryServiceURL)
	if espressoClient == nil {
		logger.Crit("failed to create Espresso client")
		return nil
	}

	l1Adapter := NewAdaptL1BlockRefClient(l1Client)

	v := &OPEspressoBatchVerifier{
		espressoStore: store,
		config:        opVerifierConfig,
		l2Client:      l2Client,
		logger:        logger,
		l1Client:      l1Client,
	}

	v.finalityPoller = sharedVerifier.NewFinalityPoller(
		v.fetchFinalitySnapshot,
		logger,
		opVerifierConfig.FinalityPollInterval,
	)

	espressoState := v.espressoStore.GetState()

	// Create the OP streamer
	streamer, err := opStreamer.NewEspressoStreamer(
		opVerifierConfig.Namespace,
		l1Adapter,
		l1Adapter,
		espressoClient,
		espressoLightClient,
		logger,
		derivation.CreateEspressoBatchUnmarshaler(),
		espressoState.FallbackHotshotHeight,
		espressoState.L2BlockNumber,
		opVerifierConfig.BatchAuthenticatorAddress,
		opVerifierConfig.TrackBatchLatency,
	)
	if err != nil {
		logger.Crit("failed to create OP streamer", "error", err)
		return nil
	}

	v.streamer = opStreamer.NewBufferedEspressoStreamer(streamer)

	return v
}

// fetchFinalitySnapshot reads the finalized L2 block from the execution node and
// the finalized L1 block from the L1 RPC for the finality poller.
func (v *OPEspressoBatchVerifier) fetchFinalitySnapshot(ctx context.Context) (opFinalitySnapshot, error) {
	finalized := big.NewInt(rpc.FinalizedBlockNumber.Int64())

	l2Block, err := v.l2Client.BlockByNumber(ctx, finalized)
	if err != nil {
		return opFinalitySnapshot{}, fmt.Errorf("failed to fetch finalized L2 block: %w", err)
	}
	if l2Block == nil {
		return opFinalitySnapshot{}, fmt.Errorf("finalized L2 block not found")
	}

	// The genesis block carries no L1-info deposit transaction, so it has no L1
	// origin to derive.
	// TODO: Maybe rethink, as this is probably only needed for integration tests
	var ethOrigin eth.BlockID
	if l2Block.NumberU64() != 0 {
		ethOrigin, err = l1OriginFromL2Block(l2Block)
		if err != nil {
			return opFinalitySnapshot{}, fmt.Errorf("failed to derive L1 origin from finalized L2 block: %w", err)
		}
	}

	l1Header, err := v.l1Client.HeaderByNumber(ctx, finalized)
	if err != nil {
		return opFinalitySnapshot{}, fmt.Errorf("failed to fetch finalized L1 block: %w", err)
	}
	if l1Header == nil {
		return opFinalitySnapshot{}, fmt.Errorf("finalized L1 block not found")
	}

	finalizedEth := eth.L1BlockRef{
		Number:     l1Header.Number.Uint64(),
		Hash:       l1Header.Hash(),
		ParentHash: l1Header.ParentHash,
		Time:       l1Header.Time,
	}

	if finalizedEth.Hash == (common.Hash{}) || l2Block.Hash() == (common.Hash{}) {
		return opFinalitySnapshot{}, fmt.Errorf("incomplete finality snapshot: finalized L1 %s, finalized L2 %s", finalizedEth.Hash.Hex(), l2Block.Hash().Hex())
	}

	return opFinalitySnapshot{
		finalizedL2Number:  l2Block.NumberU64(),
		finalizedL2Hash:    l2Block.Hash(),
		finalizedEthOrigin: ethOrigin,
		finalizedEth:       finalizedEth,
	}, nil
}

func (v *OPEspressoBatchVerifier) Start(ctx context.Context) {
	if !v.running.CompareAndSwap(false, true) {
		v.logger.Warn("OP Verifier is already running or starting")
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	v.cancel = cancel
	v.finalityPoller.Start(ctx)
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

func (v *OPEspressoBatchVerifier) drainAndVerifyBatches(ctx context.Context) *derivation.EspressoBatch {
	var verifiedBatch *derivation.EspressoBatch
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		espressoBatch, err := v.VerifyNextBatch(ctx)
		if err != nil {
			if errors.Is(err, ErrForkMismatch) {
				v.logger.Warn("seeking to proper head", "error", err)
				v.streamer.SetProperHead(v.tip)
			} else if err.Error() == "not found" {
				v.logger.Debug("batch not found on OP node yet, will try again on next interval")
			} else if strings.Contains(err.Error(), "retryable") {
				v.logger.Debug("espresso has not finalized the batch yet", "error", err)
			} else {
				v.logger.Error("batch verification failed", "error", err)
			}
			break
		}
		if espressoBatch == nil {
			v.logger.Debug("no new batches to verify")
			break
		}

		batchNumber := espressoBatch.Number()

		if v.config.TrackBatchLatency {
			hotshotTimestamp, hashTimestamp := v.streamer.GetBatchFinalizationTimestamp(espressoBatch.Hash())
			if hashTimestamp {
				latency := time.Since(time.Unix(int64(hotshotTimestamp), 0))
				v.totalBatchLatency += latency
				v.batchCount++
				averageLatency := v.totalBatchLatency / time.Duration(v.batchCount)
				v.logger.Info("Batch latency", "batch_number", batchNumber, "latency", latency, "average_latency", averageLatency, "total batches", v.batchCount, "hotshot_timestamp", hotshotTimestamp, "batch_hash", espressoBatch.Hash())
			}
		}

		v.streamer.Next(ctx)
		verifiedBatch = espressoBatch
		v.tip = espressoBatch.Header().Hash()
		v.ethOrigin = espressoBatch.L1Origin()
		v.logger.Info("Successfully verified OP batch", "batch_number", batchNumber)
	}
	return verifiedBatch
}

// verifyAndAdvance call drainAndVerifyBatches to drain all available verified batches from the streamer in a tight loop,
// then calls UpdateIfGreater once at the end to minimize disk writes.
func (v *OPEspressoBatchVerifier) verifyAndAdvance(ctx context.Context) {
	v.logger.Debug("Starting OP batch verification")

	if err := v.refresh(ctx); err != nil {
		v.logger.Error("failed to refresh OP streamer before verification", "error", err)
		return
	}

	verifiedBatch := v.drainAndVerifyBatches(ctx)
	if ctx.Err() != nil {
		return
	}

	if verifiedBatch == nil {
		v.logger.Debug("no verified batch found")
		return
	}

	hotshotFallbackPos := v.streamer.GetFallbackHotshotPos()
	updated, err := v.espressoStore.UpdateIfGreater(verifiedBatch.Number(), hotshotFallbackPos)
	if err != nil {
		v.logger.Error("failed to update espresso state in store", "error", err)
		return
	}
	if !updated {
		state := v.espressoStore.GetState()
		v.logger.Warn("not updating espresso state in store because block number is not greater than current block number in store",
			"new_block_number", verifiedBatch.Number(),
			"current_block_number", state.L2BlockNumber)
		return
	}
	v.logger.Info("Successfully verified and advanced OP batches", "last_batch_number", verifiedBatch.Number(), "hotshot_height", hotshotFallbackPos)
}

// syncEspressoStateWithEthereumFinality fast-forwards the store (and tip) to the
// Ethereum-finalized L2 block when it is ahead of espressoFinalizedBlockNumber.
// It returns the position the streamer should refresh from: the Ethereum-finalized
// block when it is ahead, otherwise espressoFinalizedBlockNumber unchanged.
func (v *OPEspressoBatchVerifier) syncEspressoStateWithEthereumFinality(snapshot opFinalitySnapshot, espressoFinalizedBlockNumber uint64) (uint64, error) {
	ethFinalizedBlockNumber := snapshot.finalizedL2Number
	if ethFinalizedBlockNumber <= espressoFinalizedBlockNumber {
		return espressoFinalizedBlockNumber, nil
	}

	v.logger.Error("ethereum finalized block is ahead of espresso finalized block",
		"eth_l2_finalized_block", ethFinalizedBlockNumber,
		"espresso_finalized_block", espressoFinalizedBlockNumber,
		"eth_finalized_num", snapshot.finalizedEthOrigin.Number,
		"eth_finalized_hash", snapshot.finalizedEthOrigin.Hash.Hex())

	// Update always advances here (eth > current store, single writer), so ignore the bool.
	if _, err := v.espressoStore.UpdateIfGreater(ethFinalizedBlockNumber, v.streamer.GetFallbackHotshotPos()); err != nil {
		return espressoFinalizedBlockNumber, err
	}

	// The store now sits at the eth-finalized l2 block, so the next batch must
	// chain onto it and refresh from there. Use that block's own L1 origin as
	// the L1 origin so the streamer will refresh there as well
	v.tip = snapshot.finalizedL2Hash
	v.ethOrigin = snapshot.finalizedEthOrigin
	return ethFinalizedBlockNumber, nil
}

// VerifyNextBatch peeks the next batch from the OP streamer, reconstructs the
// block Espresso finalized from it, and fetches the corresponding block from the
// full node. It then verifies the full node produced exactly the block Espresso
// finalized by comparing the two blocks. If they match, it returns the batch for
// further processing (advancing the streamer and updating state); if not, it
// returns an error.
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

	// Fetch the corresponding block from the full node first; we need its body
	// to complete the reconstructed Espresso block below.
	fullNodeBlock, err := v.getFullNodeBlock(ctx, batchNumber)
	if err != nil {
		return nil, err
	}

	// Reconstruct the block Espresso finalized from the batch.
	espressoBlock, err := espressoBatchToBlock(fullNodeBlock, espressoBatch)
	if err != nil {
		return nil, fmt.Errorf("failed to convert espresso batch %d to block: %w", batchNumber, err)
	}

	// The Espresso batch does not carry L1-derived user deposits (only the L1-info
	// deposit), so strip them from the full node block before comparison
	comparableBlock := filterUserDeposits(fullNodeBlock)

	if err := ensureBlocksMatch(espressoBlock, comparableBlock); err != nil {
		v.logger.Error("batch mismatch details",
			"batch_number", batchNumber,
			"espresso_hash", espressoBlock.Hash().Hex(),
			"espresso_parent", espressoBlock.ParentHash().Hex(),
			"fullnode_hash", fullNodeBlock.Hash().Hex(),
			"fullnode_parent", fullNodeBlock.ParentHash().Hex(),
			"espresso_tx_count", len(espressoBlock.Transactions()),
			"espresso_tx_types", txTypes(espressoBlock), // See txTypes for details on the type codes
			"comparable_tx_count", len(comparableBlock.Transactions()),
			"comparable_tx_types", txTypes(comparableBlock),
		)
		return nil, fmt.Errorf("batch verification failed for batch number %d: %w", batchNumber, err)
	}
	return espressoBatch, nil
}

// ensureBlocksMatch verifies the espresso-reconstructed block and the full node
// block are byte-for-byte identical.
func ensureBlocksMatch(espresso, fullNode *types.Block) error {
	if espresso.Hash() != fullNode.Hash() {
		return fmt.Errorf("block hash mismatch: espresso %s, full node %s", espresso.Hash(), fullNode.Hash())
	}

	espressoRLP, err := rlp.EncodeToBytes(espresso)
	if err != nil {
		return fmt.Errorf("failed to RLP-encode espresso block: %w", err)
	}
	fullNodeRLP, err := rlp.EncodeToBytes(fullNode)
	if err != nil {
		return fmt.Errorf("failed to RLP-encode full node block: %w", err)
	}
	if !bytes.Equal(espressoRLP, fullNodeRLP) {
		return fmt.Errorf("block mismatch at number %d: espresso and full node blocks differ", espresso.NumberU64())
	}
	return nil
}

// getFullNodeBlock fetches the block at the given number from the L2 full node.
func (v *OPEspressoBatchVerifier) getFullNodeBlock(ctx context.Context, blockNumber uint64) (*types.Block, error) {
	block, err := v.l2Client.BlockByNumber(ctx, new(big.Int).SetUint64(blockNumber))
	if err != nil {
		return nil, err
	}
	if block == nil {
		return nil, fmt.Errorf("block %d not found from full node", blockNumber)
	}
	return block, nil
}

func (v *OPEspressoBatchVerifier) refresh(ctx context.Context) error {
	// Reuse the latest finality snapshot cached by the finality poller.
	snapshot, err := v.lastSnapshot()
	if err != nil {
		return err
	}

	// If Ethereum finality is ahead of our Espresso-verified head, fast-forward
	// the store and tip to it; that becomes the position we refresh from (never
	// backwards).
	state := v.espressoStore.GetState()
	fallbackPos, err := v.syncEspressoStateWithEthereumFinality(snapshot, state.L2BlockNumber)
	if err != nil {
		return fmt.Errorf("failed to sync espresso state with Ethereum finality: %w", err)
	}

	// Check if we have l1 origin, if not use it from finality poller
	if v.ethOrigin == (eth.BlockID{}) {
		v.ethOrigin = snapshot.finalizedEthOrigin
	}

	if err := v.streamer.Refresh(ctx, snapshot.finalizedEth, fallbackPos, v.ethOrigin); err != nil {
		v.logger.Error("failed to refresh OP streamer", "error", err)
		return err
	}

	return nil
}

// peekNextBatch follows the pattern Update -> Peek, then checks the peeked batch
// against our last verified head (tip): if it does not chain onto tip it is on a
// fork and ErrForkMismatch is returned for the caller.
// Before any batch has been verified (tip unset) the first available batch is
// accepted as-is, since there is nothing to chain against yet. This will still be verified.
//
// It doesnt call Next because Proxy only calls Next if the full node block matches
// what Espresso has finalized, otherwise it remains stuck on the same batch until the OP node catches up.
func (v *OPEspressoBatchVerifier) peekNextBatch(ctx context.Context) (*derivation.EspressoBatch, error) {
	if !v.streamer.HasNext(ctx) {
		err := v.streamer.Update(ctx)
		if err != nil {
			return nil, err
		}
	}

	// Now we Peek the next batch and return it for verification
	espressoBatch := v.streamer.Peek(ctx)
	if espressoBatch == nil {
		return nil, nil
	}

	// Until we have verified a batch we have no tip to chain against, so accept
	// the first available batch as-is, we will still verify it.
	if v.tip == (common.Hash{}) {
		return espressoBatch, nil
	}

	// If the next batch does not build on our last verified head (tip), it is on
	// a fork. Surface ErrForkMismatch (wrapped with context); the caller
	// repositions the streamer.
	if espressoBatch.Header().ParentHash != v.tip {
		return nil, fmt.Errorf(
			"batch_number=%d batch_parent=%s tip=%s: %w",
			espressoBatch.Number(),
			espressoBatch.Header().ParentHash.Hex(),
			v.tip.Hex(),
			ErrForkMismatch,
		)
	}

	return espressoBatch, nil
}

// lastSnapshot returns the finality poller's most recent snapshot.
func (v *OPEspressoBatchVerifier) lastSnapshot() (opFinalitySnapshot, error) {
	snapshot, ok := v.finalityPoller.LastSnapshot()
	if !ok {
		return opFinalitySnapshot{}, fmt.Errorf("finality poller has no snapshot")
	}
	return snapshot, nil
}

func (v *OPEspressoBatchVerifier) Stop() {
	if !v.running.CompareAndSwap(true, false) {
		v.logger.Warn("OP Verifier is not running or is already stopping")
		return
	}
	v.logger.Info("Stopping OP Verifier")
	if v.cancel != nil {
		v.cancel()
	}
	v.runWg.Wait()
	v.finalityPoller.Stop()

	v.l2Client.Close()
	v.l1Client.Close()
	v.logger.Info("OP Verifier stopped")
}

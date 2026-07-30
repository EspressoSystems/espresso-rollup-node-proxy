package verifier

import (
	"bytes"
	"context"
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

type OPEspressoBatchVerifierConfig struct {
	EthRPC                    string         `json:"eth_rpc"`
	FullNodeExecutionRPC      string         `json:"full_node_execution_rpc"`
	Namespace                 uint64         `json:"namespace"`
	VerificationInterval      time.Duration  `json:"verification_interval"`
	FinalityPollInterval      time.Duration  `json:"finality_poll_interval"`
	QueryServiceURL           string         `json:"query_service_url"`
	BatcherAddress            common.Address `json:"batcher_address"`
	BatchAuthenticatorAddress common.Address `json:"batch_authenticator_address"`
}

const (
	startupFinalityTimeout      = 10 * time.Second
	startupFinalityPollInterval = 50 * time.Millisecond
	streamerRetryInterval       = time.Second
)

// EspressoStreamer is the subset of the streamer the verifier drives. The streamer
// package exports a concrete type rather than an interface, so we declare the
// methods we use here: it documents the coupling and lets tests substitute a mock.
type EspressoStreamer interface {
	// Start launches the streamer's background finality and HotShot poll loops.
	Start(ctx context.Context) error

	// Stop cancels those loops and blocks until they have returned.
	Stop()

	// Peek returns the batch extending the tip the streamer tracks, without
	// consuming it, or nil if there is none or it is not yet known to be valid.
	Peek(ctx context.Context) *derivation.EspressoBatch

	// AdvancePosition consumes the batch last returned by Peek, promoting it to
	// the streamer's tip so the next Peek looks for its child.
	AdvancePosition()

	// SetBatchPosition re-anchors the streamer onto l2Head, so the next batch it
	// serves is that block's child. Whatever it was tracking is dropped.
	SetBatchPosition(l2Head eth.L2BlockRef)

	// GetFallbackHotshotPos returns the HotShot height it is safe to resume from.
	GetFallbackHotshotPos() uint64
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
	streamer       EspressoStreamer
	espressoStore  *espressoStore.EspressoStore
	config         *OPEspressoBatchVerifierConfig
	l2Client       ExecutionClient
	logger         log.Logger
	l1Client       *ethclient.Client
	finalityPoller sharedVerifier.FinalityPollerInterface[opFinalitySnapshot]
	cancel         context.CancelFunc
	runWg          sync.WaitGroup
	running        atomic.Bool
	// originHotshotPos is the fallback HotShot height the streamer was seeded with.
	// It is the floor for what we persist; see fallbackHotshotPos.
	originHotshotPos uint64
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

	// The streamer resolves the hash of the block it is anchored to from the L2
	// execution client, which the same adapter serves for L1 and L2.
	l2Adapter := NewAdaptL1BlockRefClient(l2Client)

	// Create the OP streamer, anchored at the last block Espresso verified. It runs
	// its own finality and HotShot loops once started, reading finality through
	// v.syncStatus so it shares the verifier's finality poller rather than polling
	// the same RPCs a second time.
	streamer, err := opStreamer.NewStreamer(
		ctx,
		espressoClient,
		l1Adapter,
		l2Adapter,
		espressoLightClient,
		opVerifierConfig.BatchAuthenticatorAddress,
		opVerifierConfig.Namespace,
		derivation.CreateEspressoBatchUnmarshaler(),
		v.syncStatus,
		streamerRetryInterval,
		logger,
		espressoState.FallbackHotshotHeight,
		espressoState.L2BlockNumber,
	)
	if err != nil {
		logger.Crit("failed to create OP streamer", "error", err)
		return nil
	}
	v.streamer = streamer
	v.originHotshotPos = espressoState.FallbackHotshotHeight

	return v
}

func (v *OPEspressoBatchVerifier) fallbackHotshotPos() uint64 {
	return max(v.streamer.GetFallbackHotshotPos(), v.originHotshotPos)
}

// syncStatus is the streamer's finality source
func (v *OPEspressoBatchVerifier) syncStatus(_ context.Context) (*eth.SyncStatus, error) {
	snapshot, err := v.lastSnapshot()
	if err != nil {
		return nil, err
	}
	return &eth.SyncStatus{
		FinalizedL1: snapshot.finalizedEth,
		FinalizedL2: eth.L2BlockRef{
			Number:   snapshot.finalizedL2Number,
			Hash:     snapshot.finalizedL2Hash,
			L1Origin: snapshot.finalizedEthOrigin,
		},
	}, nil
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

	// Initialize finalized block
	if err := v.waitForFinalitySnapshot(ctx); err != nil {
		v.logger.Warn("starting the OP streamer without a finality snapshot", "error", err)
	}

	if err := v.streamer.Start(ctx); err != nil {
		v.logger.Error("failed to start the OP streamer", "error", err)
		cancel()
		v.finalityPoller.Stop()
		v.running.Store(false)
		return
	}

	v.runWg.Add(1)
	go v.run(ctx)
}

// waitForFinalitySnapshot blocks until the finality poller has published a snapshot,
// the context is done, or startupFinalityTimeout elapses.
func (v *OPEspressoBatchVerifier) waitForFinalitySnapshot(ctx context.Context) error {
	deadline := time.NewTimer(startupFinalityTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(startupFinalityPollInterval)
	defer ticker.Stop()

	for {
		if _, ok := v.finalityPoller.LastSnapshot(); ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("no finality snapshot after %s", startupFinalityTimeout)
		case <-ticker.C:
		}
	}
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
			if err.Error() == "not found" {
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

		v.streamer.AdvancePosition()
		verifiedBatch = espressoBatch
		v.logger.Info("Successfully verified OP batch", "batch_number", batchNumber)
	}
	return verifiedBatch
}

// verifyAndAdvance call drainAndVerifyBatches to drain all available verified batches from the streamer in a tight loop,
// then calls UpdateIfGreater once at the end to minimize disk writes.
func (v *OPEspressoBatchVerifier) verifyAndAdvance(ctx context.Context) {
	v.logger.Debug("Starting OP batch verification")

	if err := v.refresh(); err != nil {
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

	hotshotFallbackPos := v.fallbackHotshotPos()
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

// syncEspressoStateWithEthereumFinality fast-forwards the store and the streamer's
// position to the Ethereum-finalized L2 block when it is ahead of
// espressoFinalizedBlockNumber. It is a no-op when it is not.
func (v *OPEspressoBatchVerifier) syncEspressoStateWithEthereumFinality(snapshot opFinalitySnapshot, espressoFinalizedBlockNumber uint64) error {
	ethFinalizedBlockNumber := snapshot.finalizedL2Number
	if ethFinalizedBlockNumber <= espressoFinalizedBlockNumber {
		return nil
	}

	v.logger.Error("ethereum finalized block is ahead of espresso finalized block",
		"eth_l2_finalized_block", ethFinalizedBlockNumber,
		"espresso_finalized_block", espressoFinalizedBlockNumber,
		"eth_finalized_num", snapshot.finalizedEthOrigin.Number,
		"eth_finalized_hash", snapshot.finalizedEthOrigin.Hash.Hex())

	// Update always advances here (eth > current store, single writer), so ignore the bool.
	if _, err := v.espressoStore.UpdateIfGreater(ethFinalizedBlockNumber, v.fallbackHotshotPos()); err != nil {
		return err
	}

	// The store now sits at the eth-finalized l2 block, so re-anchor the streamer
	// there: the next batch it serves must chain onto that block rather than onto
	// wherever it was tracking further back.
	v.streamer.SetBatchPosition(eth.L2BlockRef{
		Number:   ethFinalizedBlockNumber,
		Hash:     snapshot.finalizedL2Hash,
		L1Origin: snapshot.finalizedEthOrigin,
	})
	return nil
}

// VerifyNextBatch peeks the next batch from the OP streamer, reconstructs the
// block Espresso finalized from it, and fetches the corresponding block from the
// full node. It then verifies the full node produced exactly the block Espresso
// finalized by comparing the two blocks. If they match, it returns the batch for
// further processing (advancing the streamer and updating state); if not, it
// returns an error.
func (v *OPEspressoBatchVerifier) VerifyNextBatch(ctx context.Context) (*derivation.EspressoBatch, error) {
	// Peek the next batch from the OP streamer without advancing it. The streamer only
	// serves batches that chain onto the tip it tracks, and we advance it only once the
	// full node block matches, so it stays on this batch until the OP node catches up.
	espressoBatch := v.streamer.Peek(ctx)
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

	if err := ensureBlocksMatch(espressoBlock, fullNodeBlock); err != nil {
		v.logger.Error("batch mismatch details",
			"batch_number", batchNumber,
			"espresso_hash", espressoBlock.Hash().Hex(),
			"espresso_parent", espressoBlock.ParentHash().Hex(),
			"fullnode_hash", fullNodeBlock.Hash().Hex(),
			"fullnode_parent", fullNodeBlock.ParentHash().Hex(),
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

// refresh reconciles the store with Ethereum finality before a round of verification
func (v *OPEspressoBatchVerifier) refresh() error {
	// Reuse the latest finality snapshot cached by the finality poller.
	snapshot, err := v.lastSnapshot()
	if err != nil {
		return err
	}

	// If Ethereum finality is ahead of our Espresso-verified head, fast-forward the
	// store to it and re-anchor the streamer there (never backwards).
	state := v.espressoStore.GetState()
	if err := v.syncEspressoStateWithEthereumFinality(snapshot, state.L2BlockNumber); err != nil {
		return fmt.Errorf("failed to sync espresso state with Ethereum finality: %w", err)
	}

	return nil
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
	v.streamer.Stop()
	v.finalityPoller.Stop()

	v.l2Client.Close()
	v.l1Client.Close()
	v.logger.Info("OP Verifier stopped")
}

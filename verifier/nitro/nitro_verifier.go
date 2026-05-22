package verifier

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	sharedVerifier "proxy/verifier"
	feedclient "proxy/verifier/nitro/feed_client"

	espressoStore "proxy/store"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	nitroStreamer "github.com/EspressoSystems/espresso-streamers/nitro"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

type BatcherAddressConfig struct {
	Address string `json:"address"`
	From    uint64 `json:"from"`
	To      uint64 `json:"to"`
}

type NitroEspressoBatchVerifierConfig struct {
	FeedURL               string                 `json:"feed_url"`
	FullNodeExecutionRPC  string                 `json:"full_node_execution_rpc"`
	VerificationInterval  time.Duration          `json:"verification_interval"`
	QueryServiceURL       string                 `json:"query_service_url"`
	Namespace             uint64                 `json:"namespace"`
	InitialHotshotBlock   uint64                 `json:"initial_hotshot_block"`
	ValidBatcherAddresses []BatcherAddressConfig `json:"valid_batcher_addresses"`
}

// NitroEspressoBatchVerifier verifies that messages from the Nitro sequencer feed
// match what the Nitro Espresso streamer has buffered from HotShot.
// It peeks the next message from the streamer, fetches the corresponding message
// from the feed by sequence number, and compares them byte-for-byte via RLP encoding.
// When the feed has cleaned a message, it falls back to advancing on Nitro L2 finality,
// So we take the max(espresso, nitro finalized).
type NitroEspressoBatchVerifier struct {
	streamer       nitroStreamer.EspressoStreamerInterface
	feedClient     *feedclient.FeedClient
	l2Client       *ethclient.Client
	espressoStore  *espressoStore.EspressoStore
	config         *NitroEspressoBatchVerifierConfig
	finalityPoller sharedVerifier.FinalityPollerInterface
	logger         log.Logger
	cancel         context.CancelFunc
	runWg          sync.WaitGroup
	running        atomic.Bool
}

func NewNitroEspressoBatchVerifier(
	ctx context.Context,
	logger log.Logger,
	store *espressoStore.EspressoStore,
	config *NitroEspressoBatchVerifierConfig,
) *NitroEspressoBatchVerifier {
	if config == nil {
		logger.Crit("Nitro verifier config is nil")
		return nil
	}

	client := espressoClient.NewClient(config.QueryServiceURL)
	if client == nil {
		logger.Crit("failed to create Espresso client")
		return nil
	}

	l2Client, err := ethclient.DialContext(ctx, config.FullNodeExecutionRPC)
	if err != nil {
		logger.Crit("failed to dial Nitro L2 RPC", "url", config.FullNodeExecutionRPC, "error", err)
		return nil
	}

	chainID, err := l2Client.ChainID(ctx)
	if err != nil {
		logger.Crit("failed to get chain ID from L2 client", "error", err)
		return nil
	}
	if chainID.Uint64() != config.Namespace {
		logger.Crit("chain ID mismatch", "chain_id", chainID.Uint64(), "namespace", config.Namespace)
		return nil
	}
	logger.Info("chain ID verified", "chain_id", chainID.Uint64())

	addrRanges := make([]nitroStreamer.AddressValidRangeConfig, 0, len(config.ValidBatcherAddresses))
	for _, a := range config.ValidBatcherAddresses {
		addrRanges = append(addrRanges, nitroStreamer.AddressValidRangeConfig{
			Address: a.Address,
			From:    a.From,
			To:      a.To,
		})
	}

	espressoState := store.GetState()
	startHotshotBlock := config.InitialHotshotBlock
	if espressoState.FallbackHotshotHeight > startHotshotBlock {
		startHotshotBlock = espressoState.FallbackHotshotHeight
	}

	// Upon startup see if finalized is ahead of stored
	startNitroBlock := espressoState.L2BlockNumber
	header, err := l2Client.HeaderByNumber(ctx, big.NewInt(int64(rpc.FinalizedBlockNumber)))
	if err != nil {
		logger.Warn("failed to get nitro finalized block on startup", "error", err)
	} else if header == nil {
		logger.Warn("nitro finalized block not found")
	} else if header.Number.Uint64() > espressoState.L2BlockNumber {
		logger.Warn("finalized block is ahead of stored espresso block", "espresso_height", startNitroBlock, "finalized_height", header.Number.Uint64())
		startNitroBlock = header.Number.Uint64()
	}
	streamer := nitroStreamer.NewEspressoStreamer(
		config.Namespace,
		startHotshotBlock,
		client,
		addrRanges,
		time.Second,
		startNitroBlock+1,
		logger,
	)

	feed := feedclient.NewFeedClient(config.FeedURL, config.Namespace, espressoState.L2BlockNumber, logger)

	return &NitroEspressoBatchVerifier{
		streamer:      streamer,
		feedClient:    feed,
		l2Client:      l2Client,
		espressoStore: store,
		config:        config,
		logger:        logger,
		finalityPoller: sharedVerifier.NewFinalityPoller(
			l2Client,
			logger,
		),
	}
}

func (v *NitroEspressoBatchVerifier) Start(ctx context.Context) {
	if !v.running.CompareAndSwap(false, true) {
		v.logger.Warn("Nitro verifier is already running")
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	v.cancel = cancel

	if err := v.streamer.Start(ctx); err != nil {
		v.logger.Crit("failed to start Nitro Espresso streamer", "error", err)
		return
	}

	v.feedClient.Start(ctx)
	v.finalityPoller.Start(ctx)

	v.runWg.Add(1)
	go v.run(ctx)
}

func (v *NitroEspressoBatchVerifier) run(ctx context.Context) {
	defer v.runWg.Done()
	ticker := time.NewTicker(v.config.VerificationInterval)
	defer ticker.Stop()

	espressoState := v.espressoStore.GetState()
	v.logger.Info("Starting Nitro Verifier",
		"start_block_number", espressoState.L2BlockNumber,
		"start_hotshot_height", espressoState.FallbackHotshotHeight,
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for v.verifyAndAdvance() {
				select {
				case <-ctx.Done():
					return
				default:
					// if successful we can have messages queued up in streamer,
					// try again immediately
				}
			}
		}
	}
}

// verifyAndAdvance verifies the next message from the Nitro feed against the Espresso streamer.
// Returns true if a message was verified and the streamer advanced, so we retry immediately
func (v *NitroEspressoBatchVerifier) verifyAndAdvance() bool {
	v.logger.Debug("Starting Nitro batch verification")

	espressoMsg := v.streamer.Peek()
	if espressoMsg == nil {
		nitroFinalizedBlock := v.finalityPoller.LastFinalized()
		if err := v.syncEspressoStateWithNitroFinality(nitroFinalizedBlock); err != nil {
			v.logger.Error("failed to sync espresso state with Nitro finality", "error", err)
		}
		v.logger.Debug("no new messages to verify")
		return false
	}

	feedMsg := v.feedClient.GetMessage(espressoMsg.Pos)
	if feedMsg == nil {
		nitroFinalizedBlock := v.finalityPoller.LastFinalized()
		if err := v.syncEspressoStateWithNitroFinality(nitroFinalizedBlock); err != nil {
			v.logger.Error("failed to sync espresso state with Nitro finality", "error", err)
		}
		v.logger.Debug("feed does not have message yet", "msg_pos", espressoMsg.Pos)
		return false
	}

	if err := ensureMessagesMatch(&espressoMsg.MessageWithMeta, &feedMsg.Message); err != nil {
		v.logger.Error("message mismatch between espresso and nitro feed",
			"msg_pos", espressoMsg.Pos,
			"error", err,
		)
		return false
	}

	updated, err := v.espressoStore.UpdateIfGreater(espressoMsg.Pos, espressoMsg.HotshotHeight)
	if err != nil {
		v.logger.Error("failed to update espresso state in store", "error", err)
		return false
	}
	if !updated {
		v.logger.Warn("not updating espresso state in store because message position is not greater than current message position in store",
			"msg_pos", espressoMsg.Pos)
		v.advance()
		return false
	}

	v.advance()
	v.logger.Info("successfully verified and advanced Nitro message",
		"msg_pos", espressoMsg.Pos,
		"hotshot_height", espressoMsg.HotshotHeight,
	)
	return true
}

func (v *NitroEspressoBatchVerifier) advance() {
	v.streamer.Advance()
	v.feedClient.Advance()
}

func (v *NitroEspressoBatchVerifier) advanceTo(pos uint64) {
	v.streamer.AdvanceTo(pos)
	v.feedClient.AdvanceTo(pos)
}

func (v *NitroEspressoBatchVerifier) syncEspressoStateWithNitroFinality(nitroFinalizedBlock uint64) error {
	espressoState := v.espressoStore.GetState()
	blockNumberToStore := espressoState.L2BlockNumber
	if nitroFinalizedBlock > blockNumberToStore {
		v.logger.Error("nitro finalized block is ahead of Espresso finalized block",
			"nitro_finalized", nitroFinalizedBlock,
			"espresso_finalized", espressoState.L2BlockNumber,
		)
		blockNumberToStore = nitroFinalizedBlock
		hotshotFallback := v.streamer.GetCurrentEarliestHotShotBlockNumber(blockNumberToStore)
		updated, err := v.espressoStore.UpdateIfGreater(blockNumberToStore, hotshotFallback)
		if updated {
			// We add 1 here because we are looking for the finalized + 1 on next `peek()` call
			v.advanceTo(blockNumberToStore + 1)
		}
		return err
	}

	return nil
}

func ensureMessagesMatch(espresso *nitroStreamer.MessageWithMetadata, feed *nitroStreamer.MessageWithMetadata) error {
	// If the Espresso message has no L2msg it is a delayed message — the payload
	// lives in the L1 delayed inbox and is not included in the HotShot batch.
	// DelayedMessagesRead uniquely identifies which inbox entry this is, so that's sufficient.
	if len(espresso.Message.L2msg) == 0 {
		if espresso.DelayedMessagesRead != feed.DelayedMessagesRead {
			return fmt.Errorf("delayed message mismatch: espresso=%d feed=%d",
				espresso.DelayedMessagesRead, feed.DelayedMessagesRead)
		}
		return nil
	}

	espressoBytes, err := rlp.EncodeToBytes(espresso)
	if err != nil {
		return fmt.Errorf("failed to RLP-encode Espresso message: %w", err)
	}
	feedBytes, err := rlp.EncodeToBytes(feed)
	if err != nil {
		return fmt.Errorf("failed to RLP-encode feed message: %w", err)
	}
	if !bytes.Equal(espressoBytes, feedBytes) {
		return fmt.Errorf("message mismatch: espresso=%+v feed=%+v", espresso, feed)
	}
	return nil
}

func (v *NitroEspressoBatchVerifier) Stop() {
	if !v.running.CompareAndSwap(true, false) {
		v.logger.Warn("Nitro verifier is not running")
		return
	}
	v.logger.Info("Stopping Nitro verifier")
	if v.cancel != nil {
		v.cancel()
	}
	v.runWg.Wait()
	v.finalityPoller.Stop()
	v.streamer.StopAndWait()
	v.logger.Info("Nitro verifier stopped")
}

package verifier

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	feedclient "proxy/verifier/nitro/feed_client"

	espressoStore "proxy/store"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	nitroStreamer "github.com/EspressoSystems/espresso-streamers/nitro"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

type NitroEspressoBatchVerifierConfig struct {
	FeedURL               string        `json:"feed_url"`
	FullNodeExecutionRPC  string        `json:"full_node_execution_rpc"`
	VerificationInterval  time.Duration `json:"verification_interval"`
	QueryServiceURL       string        `json:"query_service_url"`
	Namespace             uint64        `json:"namespace"`
	InitialHotshotBlock   uint64        `json:"initial_hotshot_block"`
	ValidBatcherAddresses []string      `json:"valid_batcher_addresses"`
}

// NitroEspressoBatchVerifier verifies that messages from the Nitro sequencer feed
// match what the Nitro Espresso streamer has buffered from HotShot.
// It peeks the next message from the streamer, fetches the corresponding message
// from the feed by sequence number, and compares them byte-for-byte via RLP encoding.
// When the feed has cleaned a message, it falls back to advancing on Nitro L2 finality,
// So we take the max(espresso, nitro finalized).
type NitroEspressoBatchVerifier struct {
	streamer      nitroStreamer.EspressoStreamerInterface
	feedClient    *feedclient.FeedClient
	l2Client      *ethclient.Client
	espressoStore *espressoStore.EspressoStore
	config        *NitroEspressoBatchVerifierConfig
	logger        log.Logger
	cancel        context.CancelFunc
	runWg         sync.WaitGroup
	running       atomic.Bool
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

	batcherAddrs := make([]common.Address, 0, len(config.ValidBatcherAddresses))
	for _, addr := range config.ValidBatcherAddresses {
		batcherAddrs = append(batcherAddrs, common.HexToAddress(addr))
	}

	espressoState := store.GetState()
	startHotshotBlock := config.InitialHotshotBlock
	if espressoState.FallbackHotshotHeight > startHotshotBlock {
		startHotshotBlock = espressoState.FallbackHotshotHeight
	}

	streamer := nitroStreamer.NewEspressoStreamer(
		config.Namespace,
		espressoState.L2BlockNumber,
		client,
		batcherAddrs,
		time.Second,
		startHotshotBlock,
		logger,
	)

	feed := feedclient.NewFeedClient(config.FeedURL, espressoState.L2BlockNumber, logger, nil, nil)

	return &NitroEspressoBatchVerifier{
		streamer:      streamer,
		feedClient:    feed,
		l2Client:      l2Client,
		espressoStore: store,
		config:        config,
		logger:        logger,
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
			v.verifyAndAdvance(ctx)
		}
	}
}

func (v *NitroEspressoBatchVerifier) verifyAndAdvance(ctx context.Context) {
	v.logger.Debug("Starting Nitro batch verification")

	nitroFinalizedBlock, err := v.getNitroFinalizedBlock(ctx)
	if err != nil {
		v.logger.Error("failed to fetch Nitro finalized L2 block", "error", err)
	}

	espressoMsg := v.streamer.Peek()
	if espressoMsg == nil {
		if err == nil {
			if err := v.syncEspressoStateWithNitroFinality(nitroFinalizedBlock); err != nil {
				v.logger.Error("failed to sync espresso state with Nitro finality", "error", err)
			}
		}
		v.logger.Debug("no new messages to verify")
		return
	}

	feedMsg := v.feedClient.GetMessage(espressoMsg.Pos)
	if feedMsg == nil {
		if err == nil {
			if err := v.syncEspressoStateWithNitroFinality(nitroFinalizedBlock); err != nil {
				v.logger.Error("failed to sync espresso state with Nitro finality", "error", err)
			}
		}
		v.logger.Debug("feed does not have message yet", "msg_pos", espressoMsg.Pos)
		return
	}

	if err := ensureMessagesMatch(&espressoMsg.MessageWithMeta, &feedMsg.Message); err != nil {
		v.logger.Error("message mismatch between espresso and nitro feed",
			"msg_pos", espressoMsg.Pos,
			"error", err,
		)
		return
	}

	updated, err := v.espressoStore.UpdateIfGreater(espressoMsg.Pos, espressoMsg.HotshotHeight)
	if err != nil {
		v.logger.Error("failed to update espresso state in store", "error", err)
		return
	}
	if !updated {
		v.logger.Warn("espresso state not updated — message position not greater than current",
			"msg_pos", espressoMsg.Pos)
	}

	v.advance()
	v.logger.Info("successfully verified and advanced Nitro message",
		"msg_pos", espressoMsg.Pos,
		"hotshot_height", espressoMsg.HotshotHeight,
	)
}

func (v *NitroEspressoBatchVerifier) advance() {
	v.streamer.Advance()
	v.feedClient.Advance()
}

func (v *NitroEspressoBatchVerifier) advanceTo(pos uint64) {
	v.streamer.AdvanceTo(pos)
	v.feedClient.AdvanceTo(pos)
}

func (v *NitroEspressoBatchVerifier) getNitroFinalizedBlock(ctx context.Context) (uint64, error) {
	header, err := v.l2Client.HeaderByNumber(ctx, big.NewInt(int64(rpc.FinalizedBlockNumber)))
	if err != nil {
		return 0, fmt.Errorf("failed to fetch Nitro finalized block: %w", err)
	}
	if header == nil {
		return 0, fmt.Errorf("nitro finalized block not found")
	}
	return header.Number.Uint64(), nil
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

func ensureMessagesMatch(espresso, feed *nitroStreamer.MessageWithMetadata) error {
	// If the Espresso message has no L2msg it is a delayed message — the payload
	// lives in the L1 delayed inbox and is not included in the messages sent to hotshot.
	// In that case only compare the header and delayed message counter.
	if len(espresso.Message.L2msg) == 0 {
		espressoHeader, feedHeader := espresso.Message.Header, feed.Message.Header
		if espresso.DelayedMessagesRead != feed.DelayedMessagesRead ||
			espressoHeader.Kind != feedHeader.Kind ||
			espressoHeader.Poster != feedHeader.Poster ||
			espressoHeader.BlockNumber != feedHeader.BlockNumber ||
			espressoHeader.Timestamp != feedHeader.Timestamp {
			return fmt.Errorf(
				"delayed message header mismatch\n"+
					"  delayed_messages_read: espresso=%d feed=%d\n"+
					"  header.kind:          espresso=%d feed=%d\n"+
					"  header.poster:        espresso=%s feed=%s\n"+
					"  header.block_number:  espresso=%d feed=%d\n"+
					"  header.timestamp:     espresso=%d feed=%d",
				espresso.DelayedMessagesRead, feed.DelayedMessagesRead,
				espressoHeader.Kind, feedHeader.Kind,
				espressoHeader.Poster, feedHeader.Poster,
				espressoHeader.BlockNumber, feedHeader.BlockNumber,
				espressoHeader.Timestamp, feedHeader.Timestamp,
			)
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
		espressoHeader := espresso.Message.Header
		feedHeader := feed.Message.Header
		return fmt.Errorf(
			"espresso message does not match Nitro feed message\n"+
				"  delayed_messages_read: espresso=%d feed=%d\n"+
				"  header.kind:          espresso=%d feed=%d\n"+
				"  header.poster:        espresso=%s feed=%s\n"+
				"  header.block_number:  espresso=%d feed=%d\n"+
				"  header.timestamp:     espresso=%d feed=%d\n"+
				"  header.request_id:    espresso=%v feed=%v\n"+
				"  header.l1_base_fee:   espresso=%v feed=%v\n"+
				"  l2msg (hex):          espresso=%x feed=%x\n"+
				"  rlp (hex):            espresso=%x feed=%x",
			espresso.DelayedMessagesRead, feed.DelayedMessagesRead,
			espressoHeader.Kind, feedHeader.Kind,
			espressoHeader.Poster, feedHeader.Poster,
			espressoHeader.BlockNumber, feedHeader.BlockNumber,
			espressoHeader.Timestamp, feedHeader.Timestamp,
			espressoHeader.RequestId, feedHeader.RequestId,
			espressoHeader.L1BaseFee, feedHeader.L1BaseFee,
			espresso.Message.L2msg, feed.Message.L2msg,
			espressoBytes, feedBytes,
		)
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
	v.streamer.StopAndWait()
	v.l2Client.Close()
	v.logger.Info("Nitro verifier stopped")
}

func ValidateNitroVerifierConfig(config *NitroEspressoBatchVerifierConfig) error {
	if config.FeedURL == "" {
		return fmt.Errorf("feed_url is required")
	}
	if config.FullNodeExecutionRPC == "" {
		return fmt.Errorf("full_node_execution_rpc is required")
	}
	if config.QueryServiceURL == "" {
		return fmt.Errorf("query_service_url is required")
	}
	if config.Namespace == 0 {
		return fmt.Errorf("namespace is required")
	}
	if len(config.ValidBatcherAddresses) == 0 {
		return fmt.Errorf("at least one valid_batcher_address is required")
	}
	if config.VerificationInterval <= 0 {
		return fmt.Errorf("verification_interval must be positive")
	}
	return nil
}

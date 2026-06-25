package verifier

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	sharedVerifier "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier"
	delayedmessagefetcher "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/nitro/delayed_message_fetcher"
	feedclient "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/nitro/feed_client"

	espressoStore "github.com/EspressoSystems/espresso-rollup-node-proxy/store"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	nitroStreamer "github.com/EspressoSystems/espresso-streamers/nitro"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

const ethFinalityWaitLogInterval = 5 * time.Second

type SigningKeyAddressConfig struct {
	Address common.Address `json:"address"`
	From    uint64         `json:"from"`
	To      uint64         `json:"to"`
}

type NitroEspressoBatchVerifierConfig struct {
	FeedURL                  string                    `json:"feed_url"`
	FullNodeExecutionRPC     string                    `json:"full_node_execution_rpc"`
	EthRpc                   string                    `json:"eth_rpc"`
	BridgeAddress            common.Address            `json:"bridge_address"`
	VerificationInterval     time.Duration             `json:"verification_interval"`
	FinalityPollInterval     time.Duration             `json:"finality_poll_interval"`
	QueryServiceURL          string                    `json:"query_service_url"`
	Namespace                uint64                    `json:"namespace"`
	ValidSigningKeyAddresses []SigningKeyAddressConfig `json:"valid_signing_key_addresses"`
	WaitForEthFinalization   bool                      `json:"wait_for_eth_finalization"`
	EthLogScanBlockRange     uint64                    `json:"eth_log_scan_block_range"`
}

// NitroEspressoBatchVerifier verifies that messages from the Nitro sequencer feed
// match what the Nitro Espresso streamer has buffered from HotShot.
// It peeks the next message from the streamer, fetches the corresponding message
// from the feed by sequence number, and compares them byte-for-byte via RLP encoding.
// When the feed has cleaned a message, it falls back to advancing on Nitro L2 finality,
// So we take the max(espresso, nitro finalized).
type NitroEspressoBatchVerifier struct {
	streamer            nitroStreamer.EspressoStreamerInterface
	feedClient          *feedclient.FeedClient
	l2Client            *ethclient.Client
	ethClient           *ethclient.Client
	espressoStore       *espressoStore.EspressoStore
	config              *NitroEspressoBatchVerifierConfig
	finalityPoller      sharedVerifier.FinalityPollerInterface[uint64]
	delayedMsgFetcher   *delayedmessagefetcher.DelayedMessageFetcher
	logger              log.Logger
	cancel              context.CancelFunc
	runWg               sync.WaitGroup
	running             atomic.Bool
	lastFinalityWaitLog time.Time
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

	ethClient, err := ethclient.DialContext(ctx, config.EthRpc)
	if err != nil {
		logger.Crit("failed to dial Nitro eth RPC", "url", config.EthRpc, "error", err)
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

	addrRanges := make([]nitroStreamer.AddressValidRangeConfig, 0, len(config.ValidSigningKeyAddresses))
	for _, a := range config.ValidSigningKeyAddresses {
		addrRanges = append(addrRanges, nitroStreamer.AddressValidRangeConfig{
			Address: a.Address.Hex(),
			From:    a.From,
			To:      a.To,
		})
	}

	v := &NitroEspressoBatchVerifier{
		l2Client:      l2Client,
		ethClient:     ethClient,
		espressoStore: store,
		config:        config,
		logger:        logger,
		delayedMsgFetcher: delayedmessagefetcher.MustNewDelayedMessageFetcher(
			ctx,
			ethClient,
			config.BridgeAddress,
			config.WaitForEthFinalization,
			config.EthLogScanBlockRange,
			logger,
		),
	}

	v.finalityPoller = sharedVerifier.NewFinalityPoller(
		v.fetchFinalitySnapshot,
		logger,
		config.FinalityPollInterval,
	)

	espressoState := store.GetState()

	v.streamer = nitroStreamer.NewEspressoStreamer(
		config.Namespace,
		espressoState.FallbackHotshotHeight,
		client,
		addrRanges,
		time.Second,
		espressoState.L2BlockNumber+1,
		logger,
	)

	v.feedClient = feedclient.NewFeedClient(config.FeedURL, config.Namespace, espressoState.L2BlockNumber, logger)

	return v
}

// fetchFinalitySnapshot polls the Nitro L2 node's finalized block number for the
// finality poller. Nitro only needs the finalized block number.
func (v *NitroEspressoBatchVerifier) fetchFinalitySnapshot(ctx context.Context) (uint64, error) {
	header, err := v.l2Client.HeaderByNumber(ctx, big.NewInt(rpc.FinalizedBlockNumber.Int64()))
	if err != nil {
		return 0, err
	}
	if header == nil {
		return 0, fmt.Errorf("nitro finalized block not found")
	}
	return header.Number.Uint64(), nil
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
	v.delayedMsgFetcher.Start(ctx)

	v.runWg.Add(1)
	go v.run(ctx)
}

func (v *NitroEspressoBatchVerifier) run(ctx context.Context) {
	defer v.runWg.Done()
	ticker := time.NewTicker(v.config.VerificationInterval)
	defer ticker.Stop()

	espressoState := v.espressoStore.GetState()
	v.logger.Info(
		"Starting Nitro Verifier",
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

func (v *NitroEspressoBatchVerifier) drainAndVerifyMessages(ctx context.Context) *nitroStreamer.MessageWithMetadataAndPos {
	var verifiedMsg *nitroStreamer.MessageWithMetadataAndPos

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		espressoMsg := v.streamer.Peek()
		if espressoMsg == nil {
			v.logger.Debug("no new messages to verify")
			break
		}

		feedMsg := v.feedClient.GetMessage(espressoMsg.Pos)
		if feedMsg == nil {
			v.logger.Debug("feed does not have message yet", "msg_pos", espressoMsg.Pos)
			break
		}

		if err := v.verifyMessage(ctx, espressoMsg, feedMsg); err != nil {
			if errors.Is(err, delayedmessagefetcher.ErrParentBlockNotFinalized) || errors.Is(err, delayedmessagefetcher.ErrDelayedMessageNotFound) {
				// This can get noisy so rate limit because if sequencer is set to safe block
				// then we may be waiting another 7 minutes for L1 finalization
				if time.Since(v.lastFinalityWaitLog) > ethFinalityWaitLogInterval {
					v.logger.Warn("error verifying delayed message", "msg_pos", espressoMsg.Pos, "error", err)
					v.lastFinalityWaitLog = time.Now()
				}
			} else {
				v.logger.Error("error verifying message", "msg_pos", espressoMsg.Pos, "error", err)
			}
			break
		}

		v.advance(espressoMsg.MessageWithMeta.DelayedMessagesRead - 1)
		verifiedMsg = espressoMsg
		v.logger.Info("successfully verified nitro message", "msg_pos", verifiedMsg.Pos)
	}
	return verifiedMsg
}

func (v *NitroEspressoBatchVerifier) verifyMessage(ctx context.Context, espressoMsg *nitroStreamer.MessageWithMetadataAndPos, feedMsg *feedclient.BroadcastFeedMessage) error {
	if len(espressoMsg.MessageWithMeta.Message.L2msg) == 0 {
		return v.verifyDelayedMessage(ctx, &espressoMsg.MessageWithMeta, &feedMsg.Message, espressoMsg.Pos)
	}
	return ensureMessagesMatch(&espressoMsg.MessageWithMeta, &feedMsg.Message)
}

func (v *NitroEspressoBatchVerifier) verifyDelayedMessage(ctx context.Context, espressoMsg *nitroStreamer.MessageWithMetadata, feedMsg *nitroStreamer.MessageWithMetadata, pos uint64) error {
	// Espresso does not have l2 message data for delayed messages, Espresso just confirms a delayed message was processed.
	// So before advancing the espresso tag, ensure we verify the feed and what is on the L1 match.
	if espressoMsg.DelayedMessagesRead != feedMsg.DelayedMessagesRead {
		return fmt.Errorf("delayed message DelayedMessagesRead mismatch: espresso=%d feed=%d",
			espressoMsg.DelayedMessagesRead, feedMsg.DelayedMessagesRead)
	}
	if espressoMsg.DelayedMessagesRead == 0 {
		return fmt.Errorf("delayed message has DelayedMessagesRead=0, cannot determine message index")
	}

	// Get the delayed message data from L1 and verify it matches the feed.
	messageIndex := espressoMsg.DelayedMessagesRead - 1

	delayedMsg, err := v.delayedMsgFetcher.GetDelayedMessage(
		ctx,
		messageIndex,
	)
	if err != nil {
		return fmt.Errorf("failed to fetch delayed message from parent chain: %w", err)
	}

	espressoMsg.Message.L2msg = delayedMsg
	if err := ensureMessagesMatch(espressoMsg, feedMsg); err != nil {
		return fmt.Errorf("failed to verify delayed message: %w", err)
	}
	v.logger.Info("delayed message verified", "msg_pos", pos, "delayed_msg_num", espressoMsg.DelayedMessagesRead)
	return nil
}

// verifyAndAdvance call drainAndVerifyMessages to drain all available verified messages from the streamer in a tight loop,
// then calls UpdateIfGreater once at the end to minimize disk writes.
func (v *NitroEspressoBatchVerifier) verifyAndAdvance(ctx context.Context) {
	v.logger.Debug("Starting Nitro batch verification")
	verifiedMsg := v.drainAndVerifyMessages(ctx)
	if ctx.Err() != nil {
		return
	}
	if err := v.syncEspressoStateWithNitroFinality(); err != nil {
		v.logger.Error("failed to sync espresso state with Nitro finality", "error", err)
		return
	}
	if verifiedMsg == nil {
		v.logger.Debug("no verified msg found")
		return
	}

	state := v.espressoStore.GetState()
	updated, err := v.espressoStore.UpdateIfGreater(verifiedMsg.Pos, verifiedMsg.HotshotHeight)
	if err != nil {
		v.logger.Error("failed to update espresso state in store", "error", err)
		return
	}
	if !updated {
		v.logger.Warn(
			"not updating espresso state in store because message position is not greater than current message position in store",
			"msg_pos", verifiedMsg.Pos,
			"state_pos", state.L2BlockNumber,
		)

		v.advanceTo(state.L2BlockNumber + 1)
		return
	}
	v.logger.Info(
		"successfully verified and advanced nitro messages",
		"prev_msg_pos", state.L2BlockNumber,
		"msg_pos", verifiedMsg.Pos,
		"hotshot_height", verifiedMsg.HotshotHeight,
	)
}

func (v *NitroEspressoBatchVerifier) advance(messageIndex uint64) {
	v.streamer.Advance()
	v.feedClient.Advance()
	v.delayedMsgFetcher.Advance(messageIndex)
}

func (v *NitroEspressoBatchVerifier) advanceTo(pos uint64) {
	v.streamer.AdvanceTo(pos)
	v.feedClient.AdvanceTo(pos)
}

func (v *NitroEspressoBatchVerifier) syncEspressoStateWithNitroFinality() error {
	espressoState := v.espressoStore.GetState()

	var nitroFinalizedBlock uint64
	if block, ok := v.finalityPoller.LastSnapshot(); ok {
		nitroFinalizedBlock = block
	}
	if nitroFinalizedBlock > espressoState.L2BlockNumber {
		v.logger.Error("nitro finalized block is ahead of Espresso finalized block",
			"nitro_finalized", nitroFinalizedBlock,
			"espresso_finalized", espressoState.L2BlockNumber,
		)
		hotshotFallback := v.streamer.GetCurrentEarliestHotShotBlockNumber(nitroFinalizedBlock)
		updated, err := v.espressoStore.UpdateIfGreater(nitroFinalizedBlock, hotshotFallback)
		if updated {
			// We add 1 here because we are looking for the finalized + 1 on next `peek()` call
			v.advanceTo(nitroFinalizedBlock + 1)
		}
		return err
	}

	return nil
}

func ensureMessagesMatch(espresso *nitroStreamer.MessageWithMetadata, feed *nitroStreamer.MessageWithMetadata) error {
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
	v.delayedMsgFetcher.Stop()
	v.streamer.StopAndWait()
	v.ethClient.Close()
	v.l2Client.Close()
	v.logger.Info("Nitro verifier stopped")
}

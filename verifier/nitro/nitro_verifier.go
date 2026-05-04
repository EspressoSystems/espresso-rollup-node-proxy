package verifier

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	feedclient "proxy/verifier/nitro/feed_client"

	espressoStore "proxy/store"

	espressoNetClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	nitroStreamer "github.com/EspressoSystems/espresso-streamers/nitro"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

type NitroEspressoBatchVerifierConfig struct {
	FeedURL               string        `json:"feed_url"`
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
type NitroEspressoBatchVerifier struct {
	streamer      nitroStreamer.EspressoStreamerInterface
	feedClient    *feedclient.FeedClient
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

	ec := espressoNetClient.NewClient(config.QueryServiceURL)
	if ec == nil {
		logger.Crit("failed to create Espresso client")
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
		startHotshotBlock,
		ec,
		batcherAddrs,
		time.Second,
		logger,
	)

	fc := feedclient.NewFeedClient(config.FeedURL, 1, logger, nil, nil)

	return &NitroEspressoBatchVerifier{
		streamer:      streamer,
		feedClient:    fc,
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
	v.logger.Info("Starting Nitro verifier",
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

func (v *NitroEspressoBatchVerifier) verifyAndAdvance(_ context.Context) {
	v.logger.Debug("Starting Nitro batch verification")

	espressoMsg := v.streamer.Peek()
	if espressoMsg == nil {
		v.logger.Debug("no new messages to verify")
		return
	}

	feedMsg := v.feedClient.GetMessage(espressoMsg.Pos)
	if feedMsg == nil {
		v.logger.Info("feed does not have message yet, will retry", "seq_num", espressoMsg.Pos)
		return
	}

	if err := ensureMessagesMatch(&espressoMsg.MessageWithMeta, &feedMsg.Message); err != nil {
		v.logger.Error("message mismatch between Espresso and Nitro feed",
			"seq_num", espressoMsg.Pos,
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
			"seq_num", espressoMsg.Pos)
	}

	v.streamer.Advance()
	v.logger.Info("Successfully verified and advanced Nitro message",
		"seq_num", espressoMsg.Pos,
		"hotshot_height", espressoMsg.HotshotHeight,
	)
}

func ensureMessagesMatch(espresso, feed *nitroStreamer.MessageWithMetadata) error {
	espressoBytes, err := rlp.EncodeToBytes(espresso)
	if err != nil {
		return fmt.Errorf("failed to RLP-encode Espresso message: %w", err)
	}
	feedBytes, err := rlp.EncodeToBytes(feed)
	if err != nil {
		return fmt.Errorf("failed to RLP-encode feed message: %w", err)
	}
	if !bytes.Equal(espressoBytes, feedBytes) {
		return errors.New("Espresso message does not match Nitro feed message")
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
	v.logger.Info("Nitro verifier stopped")
}

func (v *NitroEspressoBatchVerifier) GetCurrentEarliestHotShotBlock() uint64 {
	espressoState := v.espressoStore.GetState()
	return v.streamer.GetCurrentEarliestHotShotBlockNumber(espressoState.L2BlockNumber)
}

// ValidateNitroVerifierConfig checks required fields.
func ValidateNitroVerifierConfig(config *NitroEspressoBatchVerifierConfig) error {
	if config.FeedURL == "" {
		return fmt.Errorf("feed_url is required")
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
	return nil
}

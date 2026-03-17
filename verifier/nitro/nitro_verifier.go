package verifier

import (
	"bytes"
	"context"
	espressostore "proxy/espresso_store"
	"proxy/streamer/nitro"
	"proxy/verifier/nitro/feedclient"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

type Verifier struct {
	streamer      nitro.EspressoStreamerInterface
	feedclient    *feedclient.FeedClient
	espressoStore *espressostore.EspressoStore
	interval      time.Duration
}

func NewVerifier(streamer nitro.EspressoStreamerInterface,
	feedclient *feedclient.FeedClient,
	espressoStore *espressostore.EspressoStore,
	interval time.Duration) *Verifier {
	return &Verifier{
		streamer:      streamer,
		feedclient:    feedclient,
		espressoStore: espressoStore,
		interval:      interval,
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

func (v *Verifier) verify(ctx context.Context) {
	// Get the message from the streamer
	espressoMsg := v.streamer.Peek(ctx)
	if espressoMsg == nil {
		return
	}

	// Get the corresponding feed message from the feed client
	feedMsg := v.feedclient.GetMessage(espressoMsg.Pos)
	if feedMsg == nil {
		return
	}

	if !messagesEqual(&espressoMsg.MessageWithMeta, &feedMsg.Message) {
		log.Warn("message mismatch at position",
			"pos", espressoMsg.Pos,
			"hotshotHeight", espressoMsg.HotshotHeight,
		)
		return
	}

	earliestHotshotHeight := v.streamer.GetCurrentEarliestHotShotBlockNumber()

	log.Info("espresso message confirmed",
		"pos", espressoMsg.Pos,
		"earliestHotshotHeight", earliestHotshotHeight,
	)

	if err := v.espressoStore.Update(espressoMsg.Pos, earliestHotshotHeight); err != nil {
		log.Warn("failed to write position and height to disk", "err", err)
	}
	v.streamer.Advance()

}

// TODO: for delayed messages we need to compare with what is stored on L1
// TODO: LegacyBatchGasCost and BatchDataStats should these be compared or not?
func messagesEqual(msg1, msg2 *nitro.MessageWithMetadata) bool {
	if msg1.DelayedMessagesRead != msg2.DelayedMessagesRead {
		return false
	}
	if (msg1.Message == nil) != (msg2.Message == nil) {
		return false
	}

	if !bytes.Equal(msg1.Message.L2msg, msg2.Message.L2msg) {
		return false
	}

	if (msg1.Message.Header == nil) != (msg2.Message.Header == nil) {
		return false
	}

	msg1Header, msg2Header := msg1.Message.Header, msg2.Message.Header
	if msg1Header.Kind != msg2Header.Kind ||
		msg1Header.Poster != msg2Header.Poster ||
		msg1Header.BlockNumber != msg2Header.BlockNumber ||
		msg1Header.Timestamp != msg2Header.Timestamp {
		return false
	}

	if msg1Header.RequestId != msg2Header.RequestId {
		if msg1Header.RequestId == nil || msg2Header.RequestId == nil ||
			*msg1Header.RequestId != *msg2Header.RequestId {
			return false
		}
	}
	if msg1Header.L1BaseFee != msg2Header.L1BaseFee {
		if msg1Header.L1BaseFee == nil || msg2Header.L1BaseFee == nil ||
			msg1Header.L1BaseFee.Cmp(msg2Header.L1BaseFee) != 0 {
			return false
		}
	}

	return true
}

func (v *Verifier) Stop(ctx context.Context) {
	ctx.Done()
}

package verifier

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	feedclient "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/nitro/feed_client"
	espstreamersnitro "github.com/EspressoSystems/espresso-streamers/nitro"
)

// isNitroMessageAnL1Message checks if the given Nitro message is an L1 message
// (i.e., it has no L2 message content).
//
// In the case of the messages being stored on Espresso, we specifically
// omit the L2msg content when the message contents are actually informed
// by the `L1` (`Parent`) chain.
//
// Since this data ultimately comes from the `L1` source, we opted not
// to store it within Espresso, since we'd require the `L1` source to
// reconstruct the chain anyway.
//
// NOTE: parameter is assumed to not be nil, additionally,
// msg.MessageWithMeta.Message is assumed to not be nil.
//
// References:
// - https://github.com/EspressoSystems/nitro-espresso-integration/blob/1f5ea58d24ba5c4ac1ddc6ff56578ddc38c96648/espresso/submitter/polling_espresso_submitter.go#L371-L376
// - https://github.com/EspressoSystems/nitro-espresso-integration/blob/1f5ea58d24ba5c4ac1ddc6ff56578ddc38c96648/espresso/submitter/nitro_message_to_espresso_transaction_adapter.go#L54-L59
func isNitroMessageAnL1Message(msg *espstreamersnitro.MessageWithMetadataAndPos) bool {
	return len(msg.MessageWithMeta.Message.L2msg) == 0
}

// FeedSource is an interface that defines a method for retrieving feed
// messages based on a given sequence number.
type FeedSource interface {
	GetMessage(seqNum uint64) *feedclient.BroadcastFeedMessage
}

// DelayedMessageFetcher is an interface that defines a method for retrieving
// Delayed Message content for a given message index.
type DelayedMessageFetcher interface {
	GetDelayedMessage(ctx context.Context, messageIndex uint64) ([]byte, error)
}

// ErrNoMessageProvided is a sentinel error returned when the Nitro message
// provided is nil.
var ErrNoMessageProvided = errors.New("no message provided")

// ErrUnableToRetrieveFeedMessage is returned when the feed message for a
// given position.
type ErrUnableToRetrieveFeedMessage struct {
	Pos uint64
}

// Error implements error
func (e *ErrUnableToRetrieveFeedMessage) Error() string {
	return fmt.Sprintf("unabvle to retrieve feed message for %d", e.Pos)
}

// ErrPositionMismatch is returned when the Nitro message's position does not
// match the feed message's sequence number.
type ErrPositionMismatch struct {
	Have, Want uint64
}

// Error implements error
func (e *ErrPositionMismatch) Error() string {
	return fmt.Sprintf("position mismatch: have %d, want %d", e.Have, e.Want)
}

// LogValue implements [slog.LogValuer].
//
// This implementation returns a [slog.GroupValue] comtaining the error
// message (msg), and the mismatched position values (have, want)
func (e *ErrPositionMismatch) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("msg", "position mismatch"),
		slog.Uint64("have", e.Have),
		slog.Uint64("want", e.Want),
	)
}

// ErrDelayedMessageReadMismatch is returned when the Nitro message's
// DelayedMessageRead value does not match the feed message's
// DelayedMessagesRead value.
type ErrDelayedMessageReadMismatch struct {
	Have, Want uint64
}

// Error implements error
func (e *ErrDelayedMessageReadMismatch) Error() string {
	return fmt.Sprintf("delayed message read mismatch: have %d, want %d", e.Have, e.Want)
}

// LogValue implements [slog.LogValuer].
//
// The implementation returns a [slog.GroupValue] containing the error
// message (msg), and the mismatched delayed message read values (have, want)
func (e *ErrDelayedMessageReadMismatch) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("msg", "delayed message read mismatch"),
		slog.Uint64("have", e.Have),
		slog.Uint64("want", e.Want),
	)
}

func NitroMessageVerifier(
	ctx context.Context,
	delayedMsgFetcher DelayedMessageFetcher,
	feedSource FeedSource,
	current,
	espMessage *espstreamersnitro.MessageWithMetadataAndPos,
) error {
	if espMessage == nil {
		// We are expecting a non-nil Espresso Message
		return ErrNoMessageProvided
	}

	if espMessage.Pos > current.Pos {
	}

	if espMessage.MessageWithMeta.DelayedMessagesRead >= current.MessageWithMeta.DelayedMessagesRead {
	}

	feedMessage := feedSource.GetMessage(espMessage.Pos)
	if feedMessage == nil {
		// We are expecting that the feed message already exists
		return &ErrUnableToRetrieveFeedMessage{Pos: espMessage.Pos}
	}

	if espMessage.Pos != feedMessage.SequenceNumber {
		// We are expecting that our retrieved feed message matches
		// what we asked for.
		return &ErrPositionMismatch{Have: feedMessage.SequenceNumber, Want: espMessage.Pos}
	}

	if isNitroMessageAnL1Message(espMessage) {
		// In the event of an L1 Message, we need to populate our Espresso
		// Message's L2 data with the data retrieved from the `L1` or `Parent`
		// chain.
		if have, want := espMessage.MessageWithMeta.DelayedMessagesRead, feedMessage.Message.DelayedMessagesRead; have != want {
			// This only occurs if an L1 Block.
			return &ErrDelayedMessageReadMismatch{
				Have: espMessage.MessageWithMeta.DelayedMessagesRead,
				Want: feedMessage.Message.DelayedMessagesRead,
			}
		}

		messageIndex := espMessage.MessageWithMeta.DelayedMessagesRead - 1
		delayedMessages, err := delayedMsgFetcher.GetDelayedMessage(ctx, messageIndex)
		if err != nil {
			return fmt.Errorf("failed to retrieve delayed message: %w", err)
		}

		espMessage.MessageWithMeta.Message.L2msg = delayedMessages
	}

	// verify that the MessageWithMetadata matches
	if err := ensureMessagesMatch(&espMessage.MessageWithMeta, &feedMessage.Message); err != nil {
		return fmt.Errorf("message mismatch: %w", err)
	}

	return nil
}

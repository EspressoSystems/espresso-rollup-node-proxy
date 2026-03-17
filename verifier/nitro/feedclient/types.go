package feedclient

import (
	"proxy/streamer/nitro"

	"github.com/ethereum/go-ethereum/common"
)

type BroadcastMessage struct {
	Version                        int                             `json:"version"`
	Messages                       []*BroadcastFeedMessage         `json:"messages,omitempty"`
	ConfirmedSequenceNumberMessage *ConfirmedSequenceNumberMessage `json:"confirmedSequenceNumberMessage,omitempty"`
}

type BroadcastFeedMessage struct {
	SequenceNumber uint64                    `json:"sequenceNumber"`
	Message        nitro.MessageWithMetadata `json:"message"`
	BlockHash      *common.Hash              `json:"blockHash,omitempty"`
	Signature      []byte                    `json:"signature"`
}

type ConfirmedSequenceNumberMessage struct {
	SequenceNumber uint64 `json:"sequenceNumber"`
}

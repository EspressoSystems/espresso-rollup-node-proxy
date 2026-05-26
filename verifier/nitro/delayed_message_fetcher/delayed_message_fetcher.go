package delayedmessagefetcher

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"

	nitroabi "proxy/verifier/nitro/abi"
)

const (
	eventInboxMessageDelivered = "InboxMessageDelivered"
	eventInboxFromOrigin       = "InboxMessageDeliveredFromOrigin"
	methodSendL2FromOrigin     = "sendL2MessageFromOrigin"

	// abiSelectorLen is the number of bytes used by the ABI function selector
	// (first 4 bytes of keccak256 of the function signature).
	abiSelectorLen = 4
)

type sendL2MessageFromOrigin struct {
	MessageData []byte
}

var (
	inboxMessageDeliveredTopic common.Hash
	inboxFromOriginTopic       common.Hash
	sendL2FromOriginInputs     abi.Arguments
)

func init() {
	inboxAbi, err := nitroabi.InboxMetaData.GetAbi()
	if err != nil {
		panic("error getting inbox ABI: " + err.Error())
	}
	inboxMessageDeliveredTopic = inboxAbi.Events[eventInboxMessageDelivered].ID
	inboxFromOriginTopic = inboxAbi.Events[eventInboxFromOrigin].ID
	sendL2FromOriginInputs = inboxAbi.Methods[methodSendL2FromOrigin].Inputs
}

// FetchDelayedMessageFromL1 fetches the full L2msg bytes for the delayed message at
// messageIndex from the Bridge and Inbox contracts at the given L1 block.
// It verifies the data integrity via the Bridge's MessageDataHash.
func FetchDelayedMessageFromL1(
	ctx context.Context,
	l1Client *ethclient.Client,
	bridgeAddress common.Address,
	l1BlockNumber uint64,
	messageIndex uint64,
	logger log.Logger,
) ([]byte, error) {
	delivered, err := fetchBridgeEvent(ctx, l1Client, bridgeAddress, l1BlockNumber, messageIndex)
	if err != nil {
		return nil, err
	}

	data, err := fetchInboxData(ctx, l1Client, delivered.Inbox, l1BlockNumber, messageIndex, logger)
	if err != nil {
		return nil, err
	}

	if crypto.Keccak256Hash(data) != delivered.MessageDataHash {
		return nil, fmt.Errorf("message data hash mismatch for messageIndex=%d", messageIndex)
	}
	return data, nil
}

func fetchBridgeEvent(ctx context.Context, l1Client *ethclient.Client, bridgeAddress common.Address, l1BlockNumber uint64, messageIndex uint64) (*nitroabi.BridgeMessageDelivered, error) {
	filterer, err := nitroabi.NewBridgeFilterer(bridgeAddress, l1Client)
	if err != nil {
		return nil, fmt.Errorf("failed to create bridge filterer: %w", err)
	}
	messageDeliveredIter, err := filterer.FilterMessageDelivered(
		&bind.FilterOpts{Context: ctx, Start: l1BlockNumber, End: &l1BlockNumber},
		[]*big.Int{new(big.Int).SetUint64(messageIndex)},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to filter Bridge MessageDelivered logs: %w", err)
	}
	defer messageDeliveredIter.Close()
	if !messageDeliveredIter.Next() {
		return nil, fmt.Errorf("no MessageDelivered event for messageIndex=%d at L1 block %d", messageIndex, l1BlockNumber)
	}
	return messageDeliveredIter.Event, nil
}

func fetchInboxData(
	ctx context.Context,
	l1Client *ethclient.Client,
	inboxAddress common.Address,
	l1BlockNumber uint64,
	messageIndex uint64,
	logger log.Logger,
) ([]byte, error) {
	block := new(big.Int).SetUint64(l1BlockNumber)
	msgIdxHash := common.BigToHash(new(big.Int).SetUint64(messageIndex))

	logs, err := l1Client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: block,
		ToBlock:   block,
		Addresses: []common.Address{inboxAddress},
		Topics:    [][]common.Hash{{inboxMessageDeliveredTopic, inboxFromOriginTopic}, {msgIdxHash}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to filter Inbox logs: %w", err)
	}
	if len(logs) == 0 {
		return nil, fmt.Errorf("no Inbox event for messageIndex=%d at L1 block %d", messageIndex, l1BlockNumber)
	}

	filterer, err := nitroabi.NewInboxFilterer(inboxAddress, l1Client)
	if err != nil {
		return nil, fmt.Errorf("failed to create inbox filterer: %w", err)
	}
	return extractInboxData(ctx, l1Client, filterer, logs[0], logger)
}

func extractInboxData(
	ctx context.Context,
	l1Client *ethclient.Client,
	filterer *nitroabi.InboxFilterer,
	ethLog types.Log,
	logger log.Logger,
) ([]byte, error) {
	switch ethLog.Topics[0] {
	case inboxMessageDeliveredTopic:
		event, err := filterer.ParseInboxMessageDelivered(ethLog)
		if err != nil {
			return nil, fmt.Errorf("failed to parse InboxMessageDelivered: %w", err)
		}
		logger.Info("fetched delayed message via InboxMessageDelivered", "l1_block_num", ethLog.BlockNumber, "tx_hash", ethLog.TxHash.Hex(), "data_len", len(event.Data))
		return event.Data, nil

	case inboxFromOriginTopic:
		tx, _, err := l1Client.TransactionByHash(ctx, ethLog.TxHash)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch tx for InboxMessageDeliveredFromOrigin: %w", err)
		}
		if len(tx.Data()) < abiSelectorLen {
			return nil, fmt.Errorf("tx data too short for sendL2MessageFromOrigin")
		}
		values, err := sendL2FromOriginInputs.Unpack(tx.Data()[abiSelectorLen:])
		if err != nil {
			return nil, fmt.Errorf("failed to unpack sendL2MessageFromOrigin calldata: %w", err)
		}
		var msg sendL2MessageFromOrigin
		if err := sendL2FromOriginInputs.Copy(&msg, values); err != nil {
			return nil, fmt.Errorf("failed to copy sendL2MessageFromOrigin: %w", err)
		}
		logger.Info("fetched delayed message from sendL2MessageFromOrigin", "l1_block_num", ethLog.BlockNumber, "tx_hash", ethLog.TxHash.Hex(), "data_len", len(msg.MessageData))
		return msg.MessageData, nil

	default:
		return nil, fmt.Errorf("unexpected inbox log topic: %s", ethLog.Topics[0].Hex())
	}
}

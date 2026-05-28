package delayedmessagefetcher

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/ethereum/go-ethereum/rpc"

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

// ErrL1NotFinalized is returned when the L1 block containing the delayed message
// has not yet been finalized.
var ErrL1NotFinalized = errors.New("L1 block not yet finalized")

type sendL2MessageFromOrigin struct {
	MessageData []byte
}

type DelayedMessageFetcher struct {
	l1Client        *ethclient.Client
	bridgeAddress   common.Address
	waitForFinality bool
	logger          log.Logger

	inboxMessageDeliveredTopic common.Hash
	inboxFromOriginTopic       common.Hash
	sendL2FromOriginInputs     abi.Arguments
	sendL2FromOriginSelector   []byte
}

func NewDelayedMessageFetcher(l1Client *ethclient.Client, bridgeAddress common.Address, waitForFinality bool, logger log.Logger) *DelayedMessageFetcher {
	inboxAbi, err := nitroabi.InboxMetaData.GetAbi()
	if err != nil {
		panic("error getting inbox ABI: " + err.Error())
	}

	return &DelayedMessageFetcher{
		l1Client:                   l1Client,
		bridgeAddress:              bridgeAddress,
		waitForFinality:            waitForFinality,
		logger:                     logger,
		inboxMessageDeliveredTopic: inboxAbi.Events[eventInboxMessageDelivered].ID,
		inboxFromOriginTopic:       inboxAbi.Events[eventInboxFromOrigin].ID,
		sendL2FromOriginInputs:     inboxAbi.Methods[methodSendL2FromOrigin].Inputs,
		sendL2FromOriginSelector:   inboxAbi.Methods[methodSendL2FromOrigin].ID,
	}
}

// FetchDelayedMessageFromL1 fetches the full L2msg bytes for the delayed message at
// messageIndex from the Bridge and Inbox contracts at the given L1 block.
func (f *DelayedMessageFetcher) FetchDelayedMessageFromL1(
	ctx context.Context,
	l1BlockNumber uint64,
	messageIndex uint64,
) ([]byte, error) {
	if f.waitForFinality {
		finalizedHeader, err := f.l1Client.HeaderByNumber(ctx, big.NewInt(rpc.FinalizedBlockNumber.Int64()))
		if err != nil {
			return nil, fmt.Errorf("failed to get L1 finalized block: %w", err)
		}
		if l1BlockNumber > finalizedHeader.Number.Uint64() {
			return nil, fmt.Errorf("block=%d finalized=%d: %w", l1BlockNumber, finalizedHeader.Number.Uint64(), ErrL1NotFinalized)
		}
	}
	delivered, err := f.fetchBridgeEvent(ctx, l1BlockNumber, messageIndex)
	if err != nil {
		return nil, err
	}

	data, err := f.fetchInboxData(ctx, delivered.Inbox, l1BlockNumber, messageIndex)
	if err != nil {
		return nil, err
	}

	if crypto.Keccak256Hash(data) != delivered.MessageDataHash {
		return nil, fmt.Errorf("message data hash mismatch for messageIndex=%d", messageIndex)
	}
	return data, nil
}

func (f *DelayedMessageFetcher) fetchBridgeEvent(ctx context.Context, l1BlockNumber uint64, messageIndex uint64) (*nitroabi.BridgeMessageDelivered, error) {
	filterer, err := nitroabi.NewBridgeFilterer(f.bridgeAddress, f.l1Client)
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
	defer func() { _ = messageDeliveredIter.Close() }()
	if !messageDeliveredIter.Next() {
		if err := messageDeliveredIter.Error(); err != nil {
			return nil, fmt.Errorf("error iterating MessageDelivered events: %w", err)
		}
		return nil, fmt.Errorf("no MessageDelivered event for messageIndex=%d at L1 block %d", messageIndex, l1BlockNumber)
	}
	return messageDeliveredIter.Event, nil
}

func (f *DelayedMessageFetcher) fetchInboxData(
	ctx context.Context,
	inboxAddress common.Address,
	l1BlockNumber uint64,
	messageIndex uint64,
) ([]byte, error) {
	block := new(big.Int).SetUint64(l1BlockNumber)
	msgIdxHash := common.BigToHash(new(big.Int).SetUint64(messageIndex))

	// We know the l1 block number from the feed, if it is not found, we do not advance espresso tag
	logs, err := f.l1Client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: block,
		ToBlock:   block,
		Addresses: []common.Address{inboxAddress},
		Topics:    [][]common.Hash{{f.inboxMessageDeliveredTopic, f.inboxFromOriginTopic}, {msgIdxHash}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to filter Inbox logs: %w", err)
	}
	if len(logs) == 0 {
		return nil, fmt.Errorf("no Inbox event for messageIndex=%d at L1 block %d", messageIndex, l1BlockNumber)
	}

	filterer, err := nitroabi.NewInboxFilterer(inboxAddress, f.l1Client)
	if err != nil {
		return nil, fmt.Errorf("failed to create inbox filterer: %w", err)
	}
	return f.extractInboxData(ctx, filterer, logs[0])
}

func (f *DelayedMessageFetcher) extractInboxData(
	ctx context.Context,
	filterer *nitroabi.InboxFilterer,
	ethLog types.Log,
) ([]byte, error) {
	switch ethLog.Topics[0] {
	case f.inboxMessageDeliveredTopic:
		return f.extractFromInboxMessageDelivered(filterer, ethLog)
	case f.inboxFromOriginTopic:
		return f.extractFromInboxFromOrigin(ctx, ethLog)
	default:
		return nil, fmt.Errorf("unexpected inbox log topic: %s", ethLog.Topics[0].Hex())
	}
}

func (f *DelayedMessageFetcher) extractFromInboxMessageDelivered(filterer *nitroabi.InboxFilterer, ethLog types.Log) ([]byte, error) {
	event, err := filterer.ParseInboxMessageDelivered(ethLog)
	if err != nil {
		return nil, fmt.Errorf("failed to parse InboxMessageDelivered: %w", err)
	}
	f.logger.Info(
		"fetched delayed message via InboxMessageDelivered",
		"l1_block_num", ethLog.BlockNumber,
		"tx_hash", ethLog.TxHash.Hex(),
		"data_len", len(event.Data),
	)
	return event.Data, nil
}

func (f *DelayedMessageFetcher) extractFromInboxFromOrigin(ctx context.Context, ethLog types.Log) ([]byte, error) {
	tx, _, err := f.l1Client.TransactionByHash(ctx, ethLog.TxHash)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tx for InboxMessageDeliveredFromOrigin: %w", err)
	}

	data := tx.Data()
	if !bytes.HasPrefix(data, f.sendL2FromOriginSelector) {
		return nil, fmt.Errorf("tx selector mismatch: expected sendL2MessageFromOrigin (%x), got %x", f.sendL2FromOriginSelector, data[:min(len(data), abiSelectorLen)])
	}

	values, err := f.sendL2FromOriginInputs.Unpack(data[abiSelectorLen:])
	if err != nil {
		return nil, fmt.Errorf("failed to unpack sendL2MessageFromOrigin calldata: %w", err)
	}
	var l2Msg sendL2MessageFromOrigin
	if err := f.sendL2FromOriginInputs.Copy(&l2Msg, values); err != nil {
		return nil, fmt.Errorf("failed to copy sendL2MessageFromOrigin: %w", err)
	}
	f.logger.Info(
		"fetched delayed message from sendL2MessageFromOrigin",
		"l1_block_num", ethLog.BlockNumber,
		"tx_hash", ethLog.TxHash.Hex(),
		"data_len", len(l2Msg.MessageData),
	)
	return l2Msg.MessageData, nil
}

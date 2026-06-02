package delayedmessagefetcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

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
	maxBlocksPerScan           = 10000
	startBlockLookback         = 20000
	timeoutPerCall             = 10 * time.Second
	pollInterval               = 5 * time.Second

	// Two events are:
	// `event InboxMessageDelivered(uint256 indexed messageNum, bytes data)`
	// `event InboxMessageDeliveredFromOrigin(uint256 indexed messageNum)`
	// Both have message number indexed at the topic index 1.
	messageIndexTopicPos = 1

	// abiSelectorLen is the number of bytes used by the ABI function selector
	// (first 4 bytes of keccak256 of the function signature).
	abiSelectorLen = 4
)

var (
	// ErrParentBlockNotFinalized is returned when the Parent block containing the delayed message
	// has not yet been finalized.
	ErrParentBlockNotFinalized = errors.New("parent block not yet finalized")

	// ErrDelayedMessageNotFound is returned when the delayed message has not yet been found
	ErrDelayedMessageNotFound = errors.New("delayed message not yet found")
)

type sendL2MessageFromOrigin struct {
	MessageData []byte
}

type delayedMessage struct {
	data         []byte
	parentBlock  uint64
	messageIndex uint64
}

type DelayedMessageFetcher struct {
	l1Client          *ethclient.Client
	bridgeAddress     common.Address
	waitForFinality   bool
	parentBlockNumber atomic.Uint64
	bridgeFilterer    *nitroabi.BridgeFilterer
	delayedMessages   map[uint64]*delayedMessage

	cancel  context.CancelFunc
	runWg   sync.WaitGroup
	running atomic.Bool

	inboxMessageDeliveredTopic common.Hash
	inboxFromOriginTopic       common.Hash
	sendL2FromOriginInputs     abi.Arguments
	sendL2FromOriginSelector   []byte

	logger log.Logger

	mu sync.RWMutex
}

func NewDelayedMessageFetcher(
	ctx context.Context,
	l1Client *ethclient.Client,
	bridgeAddress common.Address,
	waitForFinality bool,
	logger log.Logger,
) *DelayedMessageFetcher {
	inboxAbi, err := nitroabi.InboxMetaData.GetAbi()
	if err != nil {
		panic("error getting inbox ABI: " + err.Error())
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeoutPerCall)
	defer cancel()
	finalized, err := l1Client.HeaderByNumber(timeoutCtx, big.NewInt(rpc.FinalizedBlockNumber.Int64()))
	if err != nil {
		panic(fmt.Sprintf("failed to fetch L1 header: %v", err))
	}

	filterer, err := nitroabi.NewBridgeFilterer(bridgeAddress, l1Client)
	if err != nil {
		panic(fmt.Sprintf("failed to create bridge filterer: %v", err))
	}

	finalizedBlock := finalized.Number.Uint64()
	var startBlock uint64
	if finalizedBlock > startBlockLookback {
		startBlock = finalizedBlock - startBlockLookback
	}

	logger.Info(
		"delayed message fetcher starting from finalized parent block",
		"start_parent_chain_block", startBlock,
		"finalized_parent_chain_block", finalizedBlock,
	)

	f := &DelayedMessageFetcher{
		l1Client:                   l1Client,
		bridgeAddress:              bridgeAddress,
		waitForFinality:            waitForFinality,
		bridgeFilterer:             filterer,
		delayedMessages:            make(map[uint64]*delayedMessage),
		inboxMessageDeliveredTopic: inboxAbi.Events[eventInboxMessageDelivered].ID,
		inboxFromOriginTopic:       inboxAbi.Events[eventInboxFromOrigin].ID,
		sendL2FromOriginInputs:     inboxAbi.Methods[methodSendL2FromOrigin].Inputs,
		sendL2FromOriginSelector:   inboxAbi.Methods[methodSendL2FromOrigin].ID,
		logger:                     logger,
	}
	f.parentBlockNumber.Store(startBlock)
	return f
}

func (f *DelayedMessageFetcher) GetDelayedMessage(ctx context.Context, messageIndex uint64) ([]byte, error) {
	f.mu.RLock()
	delayedMsg := f.delayedMessages[messageIndex]
	f.mu.RUnlock()

	if delayedMsg == nil {
		return nil, fmt.Errorf(
			"messageIndex=%d parentBlock=%d: %w",
			messageIndex,
			f.parentBlockNumber.Load(),
			ErrDelayedMessageNotFound,
		)
	}
	if f.waitForFinality {
		err := f.verifyFinality(ctx, delayedMsg, messageIndex)
		if err != nil {
			return nil, err
		}
		return delayedMsg.data, nil
	}

	f.logger.Info(
		"delayed message retrieved",
		"message_index", messageIndex,
		"parent_block", delayedMsg.parentBlock,
	)
	return delayedMsg.data, nil
}

func (f *DelayedMessageFetcher) Advance(messageIndex uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.delayedMessages) == 0 {
		return
	}
	for _, msg := range f.delayedMessages {
		if msg.messageIndex <= messageIndex {
			delete(f.delayedMessages, msg.messageIndex)
			if msg.messageIndex == messageIndex {
				f.logger.Info("delayed message advanced", "message_index", messageIndex)
			}
		}
	}

}

func (f *DelayedMessageFetcher) verifyFinality(ctx context.Context, delayedMsg *delayedMessage, messageIndex uint64) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeoutPerCall)
	defer cancel()
	finalizedHeader, err := f.l1Client.HeaderByNumber(timeoutCtx, big.NewInt(rpc.FinalizedBlockNumber.Int64()))
	if err != nil {
		return err
	}
	if delayedMsg.parentBlock > finalizedHeader.Number.Uint64() {
		return fmt.Errorf(
			"msgIndex=%d, parentBlock=%d, finalizedBlock=%d: %w",
			messageIndex,
			delayedMsg.parentBlock,
			finalizedHeader.Number.Uint64(),
			ErrParentBlockNotFinalized,
		)
	}

	f.logger.Info(
		"delayed message retrieved",
		"message_index", messageIndex,
		"parent_block", delayedMsg.parentBlock,
		"finalized_block", finalizedHeader.Number.Uint64(),
	)
	return nil
}

func (f *DelayedMessageFetcher) fetchDelayedMessageFromParent(
	ctx context.Context,
	endBlock uint64,
) error {
	delivered, err := f.fetchBridgeEvents(ctx, endBlock)
	if err != nil {
		return err
	}
	if len(delivered) == 0 {
		f.logger.Debug(
			"no bridge message delivered events found, advancing parent block number",
			"old_parent_block_number", f.parentBlockNumber.Load(),
			"new_parent_block_number", endBlock+1,
		)
		f.parentBlockNumber.Store(endBlock + 1)
		return nil
	}

	if err = f.fetchInboxData(ctx, delivered, endBlock); err != nil {
		return err
	}
	f.parentBlockNumber.Store(endBlock + 1)

	return nil
}

func (f *DelayedMessageFetcher) fetchBridgeEvents(ctx context.Context, endBlock uint64) ([]*nitroabi.BridgeMessageDelivered, error) {
	messageDeliveredIter, err := f.bridgeFilterer.FilterMessageDelivered(
		&bind.FilterOpts{Context: ctx, Start: f.parentBlockNumber.Load(), End: &endBlock},
		nil,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to filter Bridge MessageDelivered logs: %w", err)
	}
	defer func() {
		if err := messageDeliveredIter.Close(); err != nil {
			f.logger.Warn("failed to close message delivered iterator", "error", err)
		}
	}()

	var events []*nitroabi.BridgeMessageDelivered
	for messageDeliveredIter.Next() {
		events = append(events, messageDeliveredIter.Event)
	}
	if err := messageDeliveredIter.Error(); err != nil {
		return nil, fmt.Errorf("error iterating MessageDelivered events: %w", err)
	}
	return events, nil
}

func (f *DelayedMessageFetcher) fetchInboxData(
	ctx context.Context,
	delivered []*nitroabi.BridgeMessageDelivered,
	endBlock uint64,
) error {
	startBlock := new(big.Int).SetUint64(f.parentBlockNumber.Load())
	end := new(big.Int).SetUint64(endBlock)

	addresses := make([]common.Address, 0, len(delivered))
	seen := make(map[common.Address]bool)
	dataHashes := make(map[uint64]common.Hash, len(delivered))
	messageIds := make([]common.Hash, 0, len(delivered))

	for _, event := range delivered {
		if !seen[event.Inbox] {
			seen[event.Inbox] = true
			addresses = append(addresses, event.Inbox)
		}
		dataHashes[event.MessageIndex.Uint64()] = common.Hash(event.MessageDataHash)
		messageIds = append(messageIds, common.BigToHash(event.MessageIndex))
	}

	logs, err := f.l1Client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: startBlock,
		ToBlock:   end,
		Addresses: addresses,
		Topics:    [][]common.Hash{{f.inboxMessageDeliveredTopic, f.inboxFromOriginTopic}, messageIds},
	})
	if err != nil {
		return fmt.Errorf("failed to filter Inbox logs: %w", err)
	}
	if len(logs) == 0 {
		f.logger.Info(
			"no Inbox events found for message, advancing parent block number",
			"old_parent_block_number", f.parentBlockNumber.Load(),
			"new_parent_block_number", endBlock+1,
		)
		f.parentBlockNumber.Store(endBlock + 1)
		return nil
	}

	// Create a filter for each address
	filterers := make(map[common.Address]*nitroabi.InboxFilterer, len(addresses))
	for _, addr := range addresses {
		filterer, err := nitroabi.NewInboxFilterer(addr, f.l1Client)
		if err != nil {
			return fmt.Errorf("failed to create inbox filterer for %s: %w", addr.Hex(), err)
		}

		filterers[addr] = filterer
	}

	return f.extractInboxData(ctx, filterers, dataHashes, logs)
}

func (f *DelayedMessageFetcher) extractInboxData(
	ctx context.Context,
	filterers map[common.Address]*nitroabi.InboxFilterer,
	dataHashes map[uint64]common.Hash,
	eventLogs []types.Log,
) error {
	for _, eventLog := range eventLogs {
		// Length of topics needs to be at least messageIndexPos + 1
		// Because signature is at index 0, and message index is at index 1
		if len(eventLog.Topics) < messageIndexTopicPos+1 {
			return fmt.Errorf("inbox log missing messageIndex topic")
		}
		msgIndex := eventLog.Topics[messageIndexTopicPos].Big().Uint64()

		filterer, ok := filterers[eventLog.Address]
		if !ok {
			return fmt.Errorf("no filterer registered for inbox %s", eventLog.Address.Hex())
		}

		var data []byte
		var err error
		switch eventLog.Topics[0] {
		case f.inboxMessageDeliveredTopic:
			data, err = f.extractFromInboxMessageDelivered(filterer, eventLog, msgIndex)
		case f.inboxFromOriginTopic:
			data, err = f.extractFromInboxFromOrigin(ctx, eventLog, msgIndex)
		default:
			return fmt.Errorf("unexpected inbox log topic: %s", eventLog.Topics[0].Hex())
		}
		if err != nil {
			return err
		}

		// Check the data matches the bridge's committed MessageDataHash for specified message index
		expected, ok := dataHashes[msgIndex]
		if !ok {
			return fmt.Errorf("no MessageDelivered hash for messageIndex=%d", msgIndex)
		}
		if crypto.Keccak256Hash(data) != expected {
			return fmt.Errorf("message data hash mismatch for messageIndex=%d", msgIndex)
		}

		f.mu.Lock()
		f.delayedMessages[msgIndex] = &delayedMessage{
			data:         data,
			parentBlock:  eventLog.BlockNumber,
			messageIndex: msgIndex,
		}
		f.mu.Unlock()
	}
	return nil
}

func (f *DelayedMessageFetcher) extractFromInboxMessageDelivered(
	filterer *nitroabi.InboxFilterer,
	ethLog types.Log,
	msgIndex uint64,
) ([]byte, error) {
	event, err := filterer.ParseInboxMessageDelivered(ethLog)
	if err != nil {
		return nil, fmt.Errorf("failed to parse InboxMessageDelivered: %w", err)
	}
	f.logger.Info(
		"fetched delayed message via InboxMessageDelivered",
		"message_index", msgIndex,
		"parent_block_num", ethLog.BlockNumber,
		"tx_hash", ethLog.TxHash.Hex(),
		"data_len", len(event.Data),
	)
	return event.Data, nil
}

func (f *DelayedMessageFetcher) extractFromInboxFromOrigin(ctx context.Context, eventLog types.Log, msgIndex uint64) ([]byte, error) {
	tx, _, err := f.l1Client.TransactionByHash(ctx, eventLog.TxHash)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tx for InboxMessageDeliveredFromOrigin: %w", err)
	}

	txData := tx.Data()
	if !bytes.HasPrefix(txData, f.sendL2FromOriginSelector) {
		return nil, fmt.Errorf("tx selector mismatch: expected sendL2MessageFromOrigin (%x), got %x", f.sendL2FromOriginSelector, txData[:min(len(txData), abiSelectorLen)])
	}

	values, err := f.sendL2FromOriginInputs.Unpack(txData[abiSelectorLen:])
	if err != nil {
		return nil, fmt.Errorf("failed to unpack sendL2MessageFromOrigin calldata: %w", err)
	}
	var l2Msg sendL2MessageFromOrigin
	if err := f.sendL2FromOriginInputs.Copy(&l2Msg, values); err != nil {
		return nil, fmt.Errorf("failed to copy sendL2MessageFromOrigin: %w", err)
	}
	f.logger.Info(
		"fetched delayed message from sendL2MessageFromOrigin",
		"message_index", msgIndex,
		"parent_block_num", eventLog.BlockNumber,
		"tx_hash", eventLog.TxHash.Hex(),
		"data_len", len(l2Msg.MessageData),
	)
	return l2Msg.MessageData, nil
}

func (f *DelayedMessageFetcher) run(ctx context.Context) {
	defer f.runWg.Done()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			timeoutCtx, cancel := context.WithTimeout(ctx, timeoutPerCall)
			defer cancel()
			latestHeader, err := f.l1Client.HeaderByNumber(timeoutCtx, nil)
			if err != nil {
				f.logger.Warn("failed to fetch L1 latest header", "error", err)
				continue
			}
			endBlock := latestHeader.Number.Uint64()
			parentBlock := f.parentBlockNumber.Load()
			if endBlock-parentBlock >= maxBlocksPerScan {
				endBlock = parentBlock + maxBlocksPerScan
			}
			if err := f.fetchDelayedMessageFromParent(timeoutCtx, endBlock); err != nil {
				f.logger.Warn("failed to fetch delayed messages", "error", err)
			}
		}
	}
}

func (f *DelayedMessageFetcher) Start(ctx context.Context) {
	if !f.running.CompareAndSwap(false, true) {
		f.logger.Warn("Delayed message fetcher is already running")
		return
	}

	log.Info("started Delayed Message fetcher")

	ctx, cancel := context.WithCancel(ctx)
	f.cancel = cancel
	f.runWg.Add(1)
	go f.run(ctx)
}

func (f *DelayedMessageFetcher) Stop() {
	if !f.running.CompareAndSwap(true, false) {
		f.logger.Warn("Delayed message fetcher is not running")
		return
	}
	f.logger.Info("Stopping Delayed Message fetcher")
	if f.cancel != nil {
		f.cancel()
	}
	f.runWg.Wait()
}

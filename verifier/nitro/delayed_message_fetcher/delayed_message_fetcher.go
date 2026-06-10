package delayedmessagefetcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
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
	startBlockLookback         = 5000
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

	inboxABI *abi.ABI
)

type sendL2MessageFromOrigin struct {
	MessageData []byte
}

type delayedMessage struct {
	data           []byte
	parentBlock    uint64
	messageIndex   uint64
	finalizedBlock uint64
}

type DelayedMessageFetcher struct {
	parentChainClient *ethclient.Client
	bridgeAddress     common.Address
	waitForFinality   bool
	parentBlockNumber uint64
	bridgeFilterer    *nitroabi.BridgeFilterer
	delayedMessages   map[uint64]*delayedMessage

	cancel  context.CancelFunc
	runWg   sync.WaitGroup
	running bool

	inboxMessageDeliveredTopic common.Hash
	inboxFromOriginTopic       common.Hash
	sendL2FromOriginInputs     abi.Arguments
	sendL2FromOriginSelector   []byte

	logger log.Logger

	mu sync.RWMutex
}

func init() {
	var err error
	inboxABI, err = nitroabi.InboxMetaData.GetAbi()
	if err != nil {
		panic("failed to parse inbox ABI: " + err.Error())
	}

	if _, ok := inboxABI.Events[eventInboxMessageDelivered]; !ok {
		panic("inbox ABI missing expected event: " + eventInboxMessageDelivered)
	}
	if _, ok := inboxABI.Events[eventInboxFromOrigin]; !ok {
		panic("inbox ABI missing expected event: " + eventInboxFromOrigin)
	}
	if _, ok := inboxABI.Methods[methodSendL2FromOrigin]; !ok {
		panic("inbox ABI missing expected method: " + methodSendL2FromOrigin)
	}
}

func newDelayedMessageFetcher(
	ctx context.Context,
	parentChainClient *ethclient.Client,
	bridgeAddress common.Address,
	waitForFinality bool,
	logger log.Logger,
) (*DelayedMessageFetcher, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeoutPerCall)
	defer cancel()
	finalized, err := parentChainClient.HeaderByNumber(timeoutCtx, big.NewInt(rpc.FinalizedBlockNumber.Int64()))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch parent chain finalized header: %v", err)
	}

	filterer, err := nitroabi.NewBridgeFilterer(bridgeAddress, parentChainClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create bridge filterer: %v", err)
	}

	finalizedBlock := finalized.Number.Uint64()
	var startBlock uint64
	if finalizedBlock > startBlockLookback {
		startBlock = finalizedBlock - startBlockLookback
	}

	logger.Debug(
		"delayed message fetcher starting from finalized parent block",
		"start_parent_chain_block", startBlock,
		"finalized_parent_chain_block", finalizedBlock,
	)

	f := &DelayedMessageFetcher{
		parentChainClient:          parentChainClient,
		bridgeAddress:              bridgeAddress,
		waitForFinality:            waitForFinality,
		bridgeFilterer:             filterer,
		delayedMessages:            make(map[uint64]*delayedMessage),
		inboxMessageDeliveredTopic: inboxABI.Events[eventInboxMessageDelivered].ID,
		inboxFromOriginTopic:       inboxABI.Events[eventInboxFromOrigin].ID,
		sendL2FromOriginInputs:     inboxABI.Methods[methodSendL2FromOrigin].Inputs,
		sendL2FromOriginSelector:   inboxABI.Methods[methodSendL2FromOrigin].ID,
		parentBlockNumber:          startBlock,
		logger:                     logger,
	}
	return f, nil
}

func MustNewDelayedMessageFetcher(
	ctx context.Context,
	parentChainClient *ethclient.Client,
	bridgeAddress common.Address,
	waitForFinality bool,
	logger log.Logger,
) *DelayedMessageFetcher {
	f, err := newDelayedMessageFetcher(ctx, parentChainClient, bridgeAddress, waitForFinality, logger)
	if err != nil {
		panic(fmt.Sprintf("failed to create DelayedMessageFetcher: %v", err))
	}
	return f
}

func (f *DelayedMessageFetcher) GetDelayedMessage(ctx context.Context, messageIndex uint64) ([]byte, error) {
	f.mu.RLock()
	delayedMsg := f.delayedMessages[messageIndex]
	parentBlock := f.parentBlockNumber
	f.mu.RUnlock()

	if delayedMsg == nil {
		return nil, fmt.Errorf(
			"messageIndex=%d parentBlock=%d: %w",
			messageIndex,
			parentBlock,
			ErrDelayedMessageNotFound,
		)
	}

	// Check to see if the delayed message is still on the chain
	// It is possible that the message was reorged out as currently code doesn't back track
	found, err := f.verifyDelayedMessageOnParentChain(ctx, delayedMsg)
	if err != nil {
		return nil, err
	}
	if !found {
		// If message is not found
		// Rewind to the finalized parent block of where the message was initially found
		// and clear all messages in the cache
		f.rewind(delayedMsg)
		return nil, fmt.Errorf(
			"messageIndex=%d parentBlock=%d err: %w",
			messageIndex,
			delayedMsg.parentBlock,
			ErrDelayedMessageNotFound,
		)
	}

	if f.waitForFinality {
		err := f.ensureFinality(ctx, delayedMsg, messageIndex)
		if err != nil {
			return nil, err
		}
		return delayedMsg.data, nil
	}

	return delayedMsg.data, nil
}

func (f *DelayedMessageFetcher) Advance(toMessageIndex uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.delayedMessages) == 0 {
		return
	}
	toDelete := make([]uint64, len(f.delayedMessages))
	for _, msg := range f.delayedMessages {
		if msg.messageIndex <= toMessageIndex {
			toDelete = append(toDelete, msg.messageIndex)
		}
	}
	for _, msgIndex := range toDelete {
		delete(f.delayedMessages, msgIndex)
		if msgIndex == toMessageIndex {
			f.logger.Info("delayed message advanced", "message_index", toMessageIndex)
		}
	}

}

// ensureFinality checks that the parent block containing the delayed message is finalized. If not, it returns an error.
func (f *DelayedMessageFetcher) ensureFinality(ctx context.Context, delayedMsg *delayedMessage, messageIndex uint64) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeoutPerCall)
	defer cancel()
	finalizedHeader, err := f.parentChainClient.HeaderByNumber(timeoutCtx, big.NewInt(rpc.FinalizedBlockNumber.Int64()))
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

	return nil
}

// verifyDelayedMessageOnParentChain checks that the delayed message is still on the chain by
// re-querying the Bridge's MessageDelivered event for the message index and comparing the data hash.
// If the message is not found, it returns false. If there is an error during verification, it returns an error.
// This can be the case of a parent chain reorg where we initially saw the message, but it got reorged out later.
func (f *DelayedMessageFetcher) verifyDelayedMessageOnParentChain(ctx context.Context, msg *delayedMessage) (bool, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeoutPerCall)
	defer cancel()

	iter, err := f.bridgeFilterer.FilterMessageDelivered(
		&bind.FilterOpts{Context: timeoutCtx, Start: msg.parentBlock, End: &msg.parentBlock},
		[]*big.Int{new(big.Int).SetUint64(msg.messageIndex)},
		nil,
	)
	if err != nil {
		return false, fmt.Errorf("failed to re-check MessageDelivered for messageIndex=%d: %w", msg.messageIndex, err)
	}
	defer func() {
		if err := iter.Close(); err != nil {
			f.logger.Warn("failed to close message delivered iterator", "error", err)
		}
	}()

	for iter.Next() {
		if crypto.Keccak256Hash(msg.data) == common.Hash(iter.Event.MessageDataHash) {
			return true, nil
		}
	}
	return false, iter.Error()
}

// rewind clears the delayed message cache and rewinds the parent block number to the finalized block of the provided delayed message.
func (f *DelayedMessageFetcher) rewind(msg *delayedMessage) {
	f.logger.Warn(
		"failed to find a previously found delayed message (reorg?), rewinding delayed message fetcher",
		"message_index", msg.messageIndex,
		"original_parent_block", msg.parentBlock,
		"rewind_to", msg.finalizedBlock,
	)
	f.mu.Lock()
	f.delayedMessages = make(map[uint64]*delayedMessage)
	f.parentBlockNumber = msg.finalizedBlock
	f.mu.Unlock()
}

// fetchDelayedMessageFromParentChain queries the parent chain for Bridge MessageDelivered events
// For each MessageDelivered event, it then queries the respective Inbox contract for the message data
func (f *DelayedMessageFetcher) fetchDelayedMessageFromParentChain(
	ctx context.Context,
	endBlock uint64,
	finalized uint64,
) error {
	f.mu.RLock()
	startBlock := f.parentBlockNumber
	f.mu.RUnlock()
	delivered, err := fetchBridgeEvents(ctx, f.bridgeFilterer, startBlock, endBlock, f.logger)
	if err != nil {
		return err
	}
	if len(delivered) == 0 {
		f.logger.Debug(
			"no bridge message delivered events found, advancing parent block number",
			"old_parent_block_number", startBlock,
			"new_parent_block_number", endBlock+1,
		)
		f.updateParentBlockNumber(startBlock, endBlock+1)
		return nil
	}

	if err = f.fetchInboxData(ctx, delivered, startBlock, endBlock, finalized); err != nil {
		return err
	}
	f.updateParentBlockNumber(startBlock, endBlock+1)

	return nil
}

func (f *DelayedMessageFetcher) updateParentBlockNumber(previous uint64, target uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.parentBlockNumber != previous {
		return
	}
	f.parentBlockNumber = target
}

// fetchBridgeEvents queries the parent chain for Bridge MessageDelivered events for a given range
func fetchBridgeEvents(
	ctx context.Context,
	bridgeFilterer *nitroabi.BridgeFilterer,
	startBlock uint64,
	endBlock uint64,
	logger log.Logger,
) ([]*nitroabi.BridgeMessageDelivered, error) {
	messageDeliveredIter, err := bridgeFilterer.FilterMessageDelivered(
		&bind.FilterOpts{Context: ctx, Start: startBlock, End: &endBlock},
		nil,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to filter Bridge MessageDelivered logs: %w", err)
	}
	defer func() {
		if err := messageDeliveredIter.Close(); err != nil {
			// This should be unreachable as `Close()` doesnt actually return an error, but we log just in case
			logger.Warn("failed to close message delivered iterator", "error", err)
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

// fetchInboxData fetches the message data for the provided MessageDelivered events by querying the respective Inbox contracts.
// It then verifies the data against the MessageDataHash from the Bridge event, and if valid, stores it in the delayed message cache.
func (f *DelayedMessageFetcher) fetchInboxData(
	ctx context.Context,
	deliveredEvents []*nitroabi.BridgeMessageDelivered,
	startBlock uint64,
	endBlock uint64,
	finalized uint64,
) error {
	addresses := make([]common.Address, 0, len(deliveredEvents))
	seen := make(map[common.Address]bool)
	dataHashes := make(map[uint64]common.Hash, len(deliveredEvents))
	messageIds := make([]common.Hash, 0, len(deliveredEvents))

	for _, event := range deliveredEvents {
		// We dont need more than one of the same address
		if !seen[event.Inbox] {
			seen[event.Inbox] = true
			addresses = append(addresses, event.Inbox)
		}
		dataHashes[event.MessageIndex.Uint64()] = common.Hash(event.MessageDataHash)
		messageIds = append(messageIds, common.BigToHash(event.MessageIndex))
	}

	logs, err := f.parentChainClient.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(startBlock),
		ToBlock:   new(big.Int).SetUint64(endBlock),
		Addresses: addresses,
		Topics:    [][]common.Hash{{f.inboxMessageDeliveredTopic, f.inboxFromOriginTopic}, messageIds},
	})
	if err != nil {
		return fmt.Errorf("failed to filter Inbox logs: %w", err)
	}
	if len(logs) == 0 {
		f.logger.Info(
			"no Inbox events found for message, advancing parent block number",
			"old_parent_block_number", startBlock,
			"new_parent_block_number", endBlock+1,
		)
		return nil
	}

	// Create a filter for each address
	filterers := make(map[common.Address]*nitroabi.InboxFilterer, len(addresses))
	for _, addr := range addresses {
		filterer, err := nitroabi.NewInboxFilterer(addr, f.parentChainClient)
		if err != nil {
			return fmt.Errorf("failed to create inbox filterer for %s: %w", addr.Hex(), err)
		}

		filterers[addr] = filterer
	}

	return f.extractInboxData(ctx, filterers, dataHashes, logs, finalized)
}

// extractInboxData extracts the message data from the provided logs, verifies it against the provided data hashes,
// and stores it in the delayed message cache if valid.
func (f *DelayedMessageFetcher) extractInboxData(
	ctx context.Context,
	filterers map[common.Address]*nitroabi.InboxFilterer,
	dataHashes map[uint64]common.Hash,
	eventLogs []types.Log,
	finalized uint64,
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
		// Topic 0 is the event signature
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
			data:           data,
			parentBlock:    eventLog.BlockNumber,
			messageIndex:   msgIndex,
			finalizedBlock: finalized,
		}
		f.mu.Unlock()
	}
	return nil
}

// extractFromInboxMessageDelivered extracts the message data from an InboxMessageDelivered event log
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

// extractFromInboxFromOrigin extracts the message data for an InboxMessageDeliveredFromOrigin event by
// fetching the transaction calldata of the respective sendL2MessageFromOrigin transaction and parsing out the message data.
func (f *DelayedMessageFetcher) extractFromInboxFromOrigin(ctx context.Context, eventLog types.Log, msgIndex uint64) ([]byte, error) {
	tx, _, err := f.parentChainClient.TransactionByHash(ctx, eventLog.TxHash)
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

func (f *DelayedMessageFetcher) poll(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeoutPerCall)
	defer cancel()

	latestHeader, err := f.parentChainClient.HeaderByNumber(timeoutCtx, nil)
	if err != nil {
		f.logger.Warn("failed to fetch parent chain latest header", "error", err)
		return
	}
	finalizedHeader, err := f.parentChainClient.HeaderByNumber(timeoutCtx, big.NewInt(rpc.FinalizedBlockNumber.Int64()))
	if err != nil {
		f.logger.Warn("failed to fetch parent chain finalized header", "error", err)
		return
	}

	finalized := finalizedHeader.Number.Uint64()
	endBlock := latestHeader.Number.Uint64()
	f.mu.RLock()
	parentBlock := f.parentBlockNumber
	f.mu.RUnlock()

	if parentBlock > endBlock {
		return
	}
	if endBlock-parentBlock >= maxBlocksPerScan {
		endBlock = parentBlock + maxBlocksPerScan
	}
	if err := f.fetchDelayedMessageFromParentChain(timeoutCtx, endBlock, finalized); err != nil {
		f.logger.Warn("failed to fetch delayed messages", "error", err)
	}
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
			f.poll(ctx)
		}
	}
}

func (f *DelayedMessageFetcher) Start(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running {
		f.logger.Warn("Delayed message fetcher is already running")
		return
	}
	f.running = true

	log.Info("started Delayed Message fetcher")

	ctx, cancel := context.WithCancel(ctx)
	f.cancel = cancel
	f.runWg.Add(1)
	go f.run(ctx)
}

func (f *DelayedMessageFetcher) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running {
		f.logger.Warn("Delayed message fetcher is already running")
		return
	}
	f.running = false

	f.logger.Info("Stopping Delayed Message fetcher")
	if f.cancel != nil {
		f.cancel()
	}
	f.runWg.Wait()
}

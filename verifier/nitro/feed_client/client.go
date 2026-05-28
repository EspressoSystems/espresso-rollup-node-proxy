package feedclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ethereum/go-ethereum/log"
)

var ErrIncorrectChainId = errors.New("incorrect chain id")
var ErrMissingChainId = errors.New("missing chain id")

type FeedClient struct {
	feedWSURL  string
	chainID    uint64
	nextSeqNum uint64
	mu         sync.RWMutex
	messages   map[uint64]*BroadcastFeedMessage
	logger     log.Logger
}

const (
	headerRequestedSequenceNumber = "Arbitrum-Requested-Sequence-Number"
	headerFeedClientVersion       = "Arbitrum-Feed-Client-Version"
	headerChainID                 = "Arbitrum-Chain-Id"
	feedClientVersion             = "2"
	broadcastMessageVersion       = 1
	startBackOff                  = 1 * time.Second
	maxBackoff                    = 10 * time.Second
)

func NewFeedClient(feedWSURL string, chainID uint64, startSeqNum uint64, logger log.Logger) *FeedClient {
	return &FeedClient{
		feedWSURL:  feedWSURL,
		chainID:    chainID,
		nextSeqNum: startSeqNum,
		messages:   make(map[uint64]*BroadcastFeedMessage),
		logger:     logger,
	}
}

func (fc *FeedClient) GetMessage(seqNum uint64) *BroadcastFeedMessage {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.messages[seqNum]
}

func (fc *FeedClient) AdvanceTo(seqNum uint64) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	for i := fc.nextSeqNum; i < seqNum; i++ {
		delete(fc.messages, i)
	}
	fc.nextSeqNum = seqNum
}

func (fc *FeedClient) Advance() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	delete(fc.messages, fc.nextSeqNum)
	fc.nextSeqNum += 1
}

func (fc *FeedClient) Start(ctx context.Context) {
	go fc.readLoop(ctx)
}

func (fc *FeedClient) readLoop(ctx context.Context) {
	backoff := startBackOff
	for {
		select {
		case <-ctx.Done():
			log.Info("feed client read loop is closing.")
			return
		default:
		}

		header := http.Header{
			headerFeedClientVersion:       []string{feedClientVersion},
			headerRequestedSequenceNumber: []string{strconv.FormatUint(fc.nextSeqNum, 10)},
		}
		dialer := websocket.Dialer{EnableCompression: true}
		conn, resp, err := dialer.DialContext(ctx, fc.feedWSURL, header)
		if err != nil {
			fc.logger.Error("failed to connect to the feed", "url", fc.feedWSURL, "error", err)
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}

		if err := fc.verifyChainID(resp); errors.Is(err, ErrIncorrectChainId) || errors.Is(err, ErrMissingChainId) {
			closeErr := conn.Close()
			if closeErr != nil {
				fc.logger.Warn("error closing connection to feed", "error", closeErr)
			}
			fc.logger.Crit("feed chain id verification failed", "url", fc.feedWSURL, "error", err, "")
			return
		}

		fc.logger.Info("connected to the feed", "url", fc.feedWSURL)
		backoff = startBackOff
		fc.readMessages(ctx, conn)
		if err := conn.Close(); err != nil {
			fc.logger.Warn("error closing connection", "err", err)
		}
	}
}

func (fc *FeedClient) verifyChainID(resp *http.Response) error {
	if resp == nil {
		return ErrMissingChainId
	}
	val := resp.Header.Get(headerChainID)
	if val == "" {
		return ErrMissingChainId
	}
	chainID, err := strconv.ParseUint(val, 0, 64)
	if err != nil {
		return fmt.Errorf("malformed %s header: %w", headerChainID, err)
	}
	if chainID != fc.chainID {
		return fmt.Errorf("expected %d, got %d: %w", fc.chainID, chainID, ErrIncorrectChainId)
	}
	return nil
}

func (fc *FeedClient) readMessages(ctx context.Context, conn *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			log.Info("feed client read message loop is closing.")
			return
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			fc.logger.Error("error reading message from feed, will reconnect", "error", err)
			return
		}

		var feedMsg BroadcastMessage
		if err := json.Unmarshal(msg, &feedMsg); err != nil {
			fc.logger.Error("failed to unmarshal feed message", "error", err)
			continue
		}

		if feedMsg.Version != broadcastMessageVersion {
			fc.logger.Warn("received message with unexpected version", "expected", broadcastMessageVersion, "got", feedMsg.Version)
			continue
		}

		fc.mu.Lock()
		for _, feedMsg := range feedMsg.Messages {
			if feedMsg == nil {
				continue
			}
			if feedMsg.SequenceNumber < fc.nextSeqNum {
				continue
			}
			if fc.messages[feedMsg.SequenceNumber] != nil {
				continue
			}
			fc.messages[feedMsg.SequenceNumber] = feedMsg
		}
		fc.mu.Unlock()
	}
}

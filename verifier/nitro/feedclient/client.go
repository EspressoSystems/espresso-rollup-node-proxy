package feedclient

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/gorilla/websocket"
)

type FeedClient struct {
	feedWSURL  string
	nextSeqNum uint64
	mu         sync.RWMutex
	messages   map[uint64]*BroadcastFeedMessage

	reconnectBackoff time.Duration
	maxBackoff       time.Duration
}

const (
	headerRequestedSequenceNumber = "Arbitrum-Requested-Sequence-Number"
	headerFeedClientVersion       = "Arbitrum-Feed-Client-Version"
	feedClientVersion             = "2"
	broadcastMessageVersion       = 1
)

var (
	defaultBackoff    = 1 * time.Second
	defaultMaxBackoff = 30 * time.Second
)

func NewFeedClient(feedWSURL string, startSeqNum uint64, reconnectBackoff *time.Duration, maxBackoff *time.Duration) *FeedClient {
	if reconnectBackoff == nil {
		reconnectBackoff = &defaultBackoff
	}
	if maxBackoff == nil {
		maxBackoff = &defaultMaxBackoff
	}
	return &FeedClient{
		feedWSURL:        feedWSURL,
		nextSeqNum:       startSeqNum,
		messages:         make(map[uint64]*BroadcastFeedMessage),
		reconnectBackoff: *reconnectBackoff,
		maxBackoff:       *maxBackoff,
	}
}

func (fc *FeedClient) GetMessage(seqNum uint64) *BroadcastFeedMessage {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.messages[seqNum]
}

func (fc *FeedClient) Start(ctx context.Context) {
	go fc.readLoop(ctx)
}

func (fc *FeedClient) readLoop(ctx context.Context) {
	backoff := fc.reconnectBackoff

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		header := http.Header{
			headerFeedClientVersion:       []string{feedClientVersion},
			headerRequestedSequenceNumber: []string{strconv.FormatUint(fc.nextSeqNum, 10)},
		}
		dialer := websocket.Dialer{EnableCompression: true}
		conn, _, err := dialer.DialContext(ctx, fc.feedWSURL, header)
		if err != nil {
			log.Error("failed to connect to the feed", "url", fc.feedWSURL, "error", err)
			select {
			case <-time.After(backoff):
				if backoff < fc.maxBackoff {
					backoff *= 2
				}
			case <-ctx.Done():
				return
			}
			continue
		}

		log.Info("connected to the feed", "url", fc.feedWSURL)
		backoff = fc.reconnectBackoff
		fc.readMessages(ctx, conn)
		if err := conn.Close(); err != nil {
			log.Warn("error closing connection", "err", err)
		}
	}
}

func (fc *FeedClient) readMessages(ctx context.Context, conn *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Error("error reading message from feed", "error", err)
			return
		}

		var feedMsg BroadcastMessage
		if err := json.Unmarshal(msg, &feedMsg); err != nil {
			log.Error("failed to unmarshal feed message", "error", err)
			continue
		}

		if feedMsg.Version != broadcastMessageVersion {
			log.Warn("received message with unexpected version", "expected", broadcastMessageVersion, "got", feedMsg.Version)
			continue
		}

		fc.mu.Lock()
		for _, feedMsg := range feedMsg.Messages {
			if feedMsg == nil {
				continue
			}
			if fc.messages[feedMsg.SequenceNumber] != nil {
				log.Warn("received duplicate message", "seqNum", feedMsg.SequenceNumber)
				continue
			}
			fc.messages[feedMsg.SequenceNumber] = feedMsg
		}
		fc.mu.Unlock()
	}
}

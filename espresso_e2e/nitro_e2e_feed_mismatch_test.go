package espresso_e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	feedclient "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/nitro/feed_client"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// newMockFeedProxy starts a websocket proxy that sits between the verifier's feed client
// and the real Nitro feed. Every BroadcastFeedMessage is passed through modifyFunc before
// being forwarded, allowing the test to inject bad data into the feed.
func newMockFeedProxy(t *testing.T, upstream string, modifyFunc func(*feedclient.BroadcastFeedMessage)) (string, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedSeq := r.Header.Get("Arbitrum-Requested-Sequence-Number")
		upHeader := http.Header{
			"Arbitrum-Feed-Client-Version":       {"2"},
			"Arbitrum-Requested-Sequence-Number": {requestedSeq},
		}

		conn, resp, err := websocket.DefaultDialer.Dial(upstream, upHeader)
		if err != nil {
			t.Logf("mock feed proxy: upstream dial failed: %v", err)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Logf("mock feed proxy: error closing upstream connection: %v", err)
			}
		}()

		respHeader := http.Header{}
		if resp != nil {
			if v := resp.Header.Get("Arbitrum-Chain-Id"); v != "" {
				respHeader.Set("Arbitrum-Chain-Id", v)
			}
		}

		clientConn, err := upgrader.Upgrade(w, r, respHeader)
		if err != nil {
			t.Logf("error upgrading connection to ws: %v", err)
			return
		}
		defer func() {
			if err := clientConn.Close(); err != nil {
				t.Logf("mock feed proxy: error closing client connection: %v", err)
			}
		}()

		for {
			msgType, maybeModifiedMessage, err := conn.ReadMessage()
			if err != nil {
				t.Logf("error reading message: %v", err)
				return
			}

			var broadcast feedclient.BroadcastMessage
			err = json.Unmarshal(maybeModifiedMessage, &broadcast)
			if err != nil {
				t.Logf("error unmarshalling broadcast message: %v", err)
			}
			if broadcast.Version == 1 {
				for _, msg := range broadcast.Messages {
					if msg != nil {
						modifyFunc(msg)
					}
				}
				modifiedMessage, err := json.Marshal(broadcast)
				if err != nil {
					t.Logf("error modifying broadcast message: %v", err)
				} else {
					maybeModifiedMessage = modifiedMessage
				}
			}

			if err := clientConn.WriteMessage(msgType, maybeModifiedMessage); err != nil {
				t.Logf("mock feed error writing message: %v", err)
				return
			}
		}
	}))

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return wsURL, srv.Close
}

func TestNitroE2EFeedMismatch(t *testing.T) {
	t.Log("Starting Nitro rollup nodes")
	shutdown := runDockerCompose(nitroWorkingDir)
	defer shutdown()

	t.Log("Waiting for Nitro services to be ready")
	waitForNitroServicesReady(t)

	espressoStore := newTestStore(t, "nitro-feed-mismatch", 1, nitroNamespace)
	ctx := context.Background()

	t.Log("Starting in-process proxy")
	proxyURL, shutdownProxy := startTestProxy(ctx, t, nitroFullNodeURL, espressoStore, espressoTag)
	defer shutdownProxy()

	t.Run("store halts on feed mismatch and recovers when correct feed is restored", func(t *testing.T) {
		// Start store with a few real blocks before injecting the bad feed.
		t.Log("Starting store with mock verifier")

		const maliciousFeedAt = uint64(20)
		mockFeedURL, stopMockFeed := newMockFeedProxy(t, nitroFullNodeFeedURL, func(m *feedclient.BroadcastFeedMessage) {
			// start feed mismatch after 20
			if m.SequenceNumber > maliciousFeedAt {
				// incrementing delayed message is enough to cause feed mismatch
				m.Message.DelayedMessagesRead += 1
			}
		})
		defer stopMockFeed()

		mismatchLogger, mismatchCapturer := newCapturingLogger()
		v := startNitroVerifierWithLogger(ctx, t, mismatchLogger, espressoStore, mockFeedURL)

		pollUntil(t, 3*time.Minute, "store did not reach block 20", func() bool {
			return getStoredBlock(t, espressoStore) >= maliciousFeedAt
		})

		blockBeforeMismatch := getStoredBlock(t, espressoStore)

		// Tampering starts after message 20, so the first mismatch should be at maliciousFeedAt + 1.
		t.Log("Waiting for mismatch to be detected at msg_pos 21")
		pollUntil(t, 2*time.Minute, "verifier did not log a feed mismatch at msg_pos 21", func() bool {
			return matchLogAttrs(mismatchCapturer, "error verifying message", map[string]uint64{"msg_pos": maliciousFeedAt + 1})
		})
		t.Log("Feed mismatch detected at msg_pos 21")

		blockAfterMismatch := getStoredBlock(t, espressoStore)
		require.Equal(t, blockBeforeMismatch, blockAfterMismatch,
			"store should not advance while feed is tampered (before=%d, after=%d)",
			blockBeforeMismatch, blockAfterMismatch)
		t.Logf("Store correctly stuck at block %d during feed mismatch", blockAfterMismatch)

		// Stop the bad verifier and mock proxy, then restart with the real feed.
		v.Stop()
		stopMockFeed()

		t.Log("Restarting verifier with correct feed — expecting recovery")
		v2 := startNitroVerifier(ctx, t, espressoStore)
		defer v2.Stop()

		// Monitor for 1 minute or until 30 blocks gained — store must never go backwards.
		targetBlock := blockBeforeMismatch + 30
		previous := monitorStoredBlockProgress(t, espressoStore, blockBeforeMismatch, time.Minute, nitroFullNodeURL, func(current uint64) bool {
			return current >= targetBlock
		})
		require.Greater(t, previous, blockBeforeMismatch,
			"store did not advance after feed mismatch recovery (stuck at %d)", previous)
		t.Logf("Store recovered to block %d (target was %d)", previous, targetBlock)

		requireProxyTagMatchesDirectBlock(t, proxyURL, nitroFullNodeURL, espressoTag)
		t.Log("Proxy correctly serves espresso tag after feed mismatch recovery")
	})
}

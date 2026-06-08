package websocket_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"proxy/websocket"
	"proxy/websocket/websockettest"
	"proxy/websocket/websocketutil"

	"github.com/stretchr/testify/require"
)

// TestGorillaBasicSuite runs the basic suite of tests on the
// Gorilla WebSocket implementation. This is to ensure that the Gorilla
// implementation is compliant with the basic WebSocket protocol.
func TestGorillaBasicSuite(t *testing.T) {
	suite := websockettest.NewBasicSuite(websocket.GorillaUpgrader(), websocket.GorillaDialer())
	newServer := websockettest.TestServerCreator
	t.Run("ServerClose", suite.CreateBasicServerCloseTest(newServer, websocket.StatusGoingAway, "goodbye"))
	t.Run("ClientClose", suite.CreateBasicClientCloseTest(newServer, websocket.StatusNormalClosure, "goodbye"))
	t.Run("ClientWrite", suite.CreateBasicWriteMessageTest(newServer, websocket.MessageTypeText, []byte("hello there")))
	t.Run("ClientRead", suite.CreateBasicReadMessageTest(newServer, websocket.MessageTypeText, []byte("hello there")))
}

// TestGorillaReverseProxyBasicSuite runs the basic suite of tests on the
// a reverse proxy setup using the Gorilla WebSocket implementation.
// This is to ensure that the Gorilla reverse proxy implementation is
// compliant with the basic WebSocket protocol, and that the reverse proxy
// setup does not interfere with the basic WebSocket protocol behavior.
func TestGorillaReverseProxyBasicSuite(t *testing.T) {
	suite := websockettest.NewBasicSuite(websocket.GorillaUpgrader(), websocket.GorillaDialer())
	newServer := websocketutil.TestServerCreator(websocket.GorillaDialer())
	t.Run("ServerClose", suite.CreateBasicServerCloseTest(newServer, websocket.StatusGoingAway, "goodbye"))
	t.Run("ClientClose", suite.CreateBasicClientCloseTest(newServer, websocket.StatusNormalClosure, "goodbye"))
	t.Run("ClientWrite", suite.CreateBasicWriteMessageTest(newServer, websocket.MessageTypeText, []byte("hello there")))
	t.Run("ClientRead", suite.CreateBasicReadMessageTest(newServer, websocket.MessageTypeText, []byte("hello there")))
}

// TestGorillaServerReadTimeout tests that the Gorilla WebSocket
// implementation times out the weboskcet connection as expected when the
// context provided has a deadline.
func TestGorillaServerReadTimeout(t *testing.T) {
	const (
		readTimeout = 200 * time.Millisecond
	)
	ctx := t.Context()
	require := require.New(t)
	handler := websockettest.WebSocketHandlerFunc(func(conn websocket.Conn, err error) {
		require.NoError(err)
		ctx, cancel := context.WithTimeout(ctx, readTimeout)
		defer cancel()
		_, _, err = conn.Read(ctx)

		// Should be a Timeout error of some kind.
		require.Error(err)

		require.NoError(conn.Close(websocket.StatusNormalClosure, "goodbye"))
	})

	server := websockettest.NewServer(websocket.GorillaUpgrader(), handler)
	defer server.Close()

	conn, response, err := server.Connect(ctx, websocket.GorillaDialer())
	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	// Don't write anything for the full duration of the timeout + 1ms.

	time.Sleep(readTimeout)

	_ = conn.Write(ctx, websocket.MessageTypeText, []byte("hello there"))
}

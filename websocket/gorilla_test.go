package websocket_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	gorillawebsocket "github.com/gorilla/websocket"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket/websockettest"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket/websocketutil"

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
	var wg sync.WaitGroup
	handler := websockettest.WebSocketHandlerFunc(func(conn websocket.Conn, err error) {
		defer wg.Done()
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

	wg.Add(1)
	conn, response, err := server.Connect(ctx, websocket.GorillaDialer())
	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	// Don't write anything for the full duration of the timeout + 1ms.

	time.Sleep(readTimeout + 50*time.Millisecond)

	_ = conn.Write(ctx, websocket.MessageTypeText, []byte("hello there"))

	wg.Wait()
}

// ExampleGorillaCoder demonstrates how to use the AdaptCoder function to
// adapt a [*gorillawebsocket.Conn] connection to the [websocket.Conn]
// interface.
func ExampleAdaptGorilla() {
	ctx := context.Background()
	rawConn, _, err := gorillawebsocket.DefaultDialer.DialContext(ctx, "wss://echo.websocket.org/", nil)
	if err != nil {
		panic(err)
	}

	conn := websocket.AdaptGorilla(rawConn)
	_ = conn
}

// ExampleGorillaUpgrader demonstrates how to use the
// [websocket.GorillaUpgrader] to upgrade an WebSocket connection using the
// github.com/gorilla/websocket package.
func ExampleGorillaUpgrader() {
	upgrader := websocket.GorillaUpgrader()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r)

		_ = conn
		_ = err
	}))
	defer server.Close()
}

// ExampleGorillaDialer demonstrates how to use the CoderDialer to dial connect
// to a server endpoint that serves a Websocket connection.
func ExampleGorillaDialer() {
	dialer := websocket.GorillaDialer()
	conn, _, err := dialer.Dial(context.Background(), "wss://echo.websocket.org/")
	if err != nil {
		panic(err)
	}

	_ = conn
}

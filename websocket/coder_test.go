package websocket_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"proxy/websocket"
	"proxy/websocket/websockettest"
	"testing"

	coderwebsocket "github.com/coder/websocket"
)

// TestCoderBasicSuite runs the basic suite of tests on the Adapters
// for the WebSocket implementations.
func TestCoderBasicSuite(t *testing.T) {
	suite := websockettest.NewBasicSuite(websocket.CoderUpgrader(), websocket.CoderDialer())
	newServer := websockettest.TestServerCreator
	t.Run("ServerClose", suite.CreateBasicServerCloseTest(newServer, websocket.StatusGoingAway, "goodbye"))
	t.Run("ClientClose", suite.CreateBasicClientCloseTest(newServer, websocket.StatusNormalClosure, "goodbye"))
	t.Run("ClientWrite", suite.CreateBasicWriteMessageTest(newServer, websocket.MessageTypeText, []byte("hello there")))
	t.Run("ClientRead", suite.CreateBasicReadMessageTest(newServer, websocket.MessageTypeText, []byte("hello there")))
}

// ExampleAdaptCoder demonstrates how to use the AdaptCoder function to
// adapt a [*coderwebsocket.Conn] connection to the [websocket.Conn] interface.
func ExampleAdaptCoder() {
	ctx := context.Background()
	rawConn, _, err := coderwebsocket.Dial(ctx, "wss://echo.websocket.org/", nil)
	if err != nil {
		panic(err)
	}

	conn := websocket.AdaptCoder(rawConn)
	_ = conn
}

// ExampleCoderUpgrader demonstrates how to use the CoderUpgrader to upgrade
// an WebSocket connection using the coder/websocket package. This is useful
// for integrating with existing code that uses the coder/websocket package, and
// for testing/purposes.
func ExampleCoderUpgrader() {
	upgrader := websocket.CoderUpgrader()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r)

		_ = conn
		_ = err
	}))
	defer server.Close()
}

// ExampleCoderDialer demonstrates how to use the CoderDialer to dial connect
// to a server endpoint that serves a Websocket connection.
func ExampleCoderDialer() {
	dialer := websocket.CoderDialer()
	conn, _, err := dialer.Dial(context.Background(), "wss://echo.websocket.org/")
	if err != nil {
		panic(err)
	}

	_ = conn
}

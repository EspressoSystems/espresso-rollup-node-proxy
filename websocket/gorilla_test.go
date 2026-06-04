package websocket_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	proxywebsocket "proxy/websocket"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type TestWebSocketServerConfig struct {
	Upgrader websocket.Upgrader
	Dialer   websocket.Dialer
}

func NewGorillaWebsocketServer(handler func(conn proxywebsocket.Conn, err error)) (*url.URL, *httptest.Server) {
	var config TestWebSocketServerConfig
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := config.Upgrader.Upgrade(w, r, nil)
		handler(proxywebsocket.AdaptGorilla(conn), err)
	}))

	return &url.URL{
		Scheme: "ws",
		Host:   server.Listener.Addr().String(),
	}, server
}

func GorillaDial(u *url.URL) (proxywebsocket.Conn, *http.Response, error) {
	conn, response, err := websocket.DefaultDialer.Dial(u.String(), nil)
	return proxywebsocket.AdaptGorilla(conn), response, err
}

// TestGorillaWebsocketAdapterServerClosesSuccessfully tests that a
// connection of the WebSocket will be closed successfully, and that the
// reported reason and status code will be returned successfully when
// everything is setup correctly.
func TestGorillaWebsocketAdapterServerClosesSuccessfully(t *testing.T) {
	// Setup
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Configured constants for testings
	const (
		status  = proxywebsocket.StatusNormalClosure
		message = "test closing connection"
	)

	// This handler will immediately close any connection found
	handler := func(conn proxywebsocket.Conn, err error) {
		// We should not have an issue connecting to the server
		require.NoError(err)

		// Let's try to close the connection
		require.NoError(conn.Close(status, message))

		time.Sleep(time.Millisecond * 100)

		_, _, err = conn.Read(ctx)
		require.Error(err)
	}

	// Start the WebSocket server with a handler that will close the
	wsURL, wsServer := NewGorillaWebsocketServer(handler)
	// connection immediately
	defer wsServer.Close()

	// Connect to the Server
	conn, response, err := GorillaDial(wsURL)

	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	// Try to read something from the connection, to force the Close connection
	_, _, err = conn.Read(ctx)
	require.Error(err)

	closeErr, ok := conn.IsCloseError(err)
	require.True(ok, "error should be a close error")
	require.Equal(closeErr.Status, status)
	require.Equal(closeErr.Reason, message)

	require.Error(conn.Write(ctx, proxywebsocket.MessageTypeText, []byte("hello there")))
}

func TestGorillaWebsocketAdapterClientClosesSuccessfully(t *testing.T) {
	// Setup
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Configured constants for testings
	const (
		status  = proxywebsocket.StatusNormalClosure
		message = "test closing connection"
	)

	// This handler will immediately close any connection found
	handler := func(conn proxywebsocket.Conn, err error) {
		// We should not have an issue connecting to the server
		require.NoError(err)

		_, _, err = conn.Read(ctx)
		require.Error(err)
		closeErr, ok := conn.IsCloseError(err)
		require.True(ok, "err should be a close error")
		require.Equal(closeErr.Status, status)
		require.Equal(closeErr.Reason, message)

		require.NoError(conn.Close(status, message))
	}

	// Start the WebSocket server with a handler that will close the
	wsURL, wsServer := NewGorillaWebsocketServer(handler)
	// connection immediately
	defer wsServer.Close()

	// Connect to the Server
	conn, response, err := GorillaDial(wsURL)

	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	// Try to read something from the connection, to force the Close connection
	require.NoError(conn.Close(status, message))

	_, _, err = conn.Read(ctx)
	require.Error(err)
}

// TestGorillaWebSocketAdapterWritesMessageSuccessfully tests that a message
// can be written to the WebSocket connection successfully, and that the
// message is received on the other end of the connection.
func TestGorillaWebSocketAdapterWritesMessageSuccessfully(t *testing.T) {
	// Setup
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		expectedMessageType = proxywebsocket.MessageTypeText
		expectedMessage     = "hello there"
	)

	handler := func(conn proxywebsocket.Conn, err error) {
		require.NoError(err)

		// Ensure that we can read a message
		messageType, message, err := conn.Read(ctx)
		require.NoError(err)

		require.Equal(expectedMessageType, messageType)
		require.Equal(expectedMessage, string(message))
	}

	// Start the WebSocket server with a handler that will read a message and
	// and verify that it matches the expected message and message type
	wsURL, wsServer := NewGorillaWebsocketServer(handler)
	defer wsServer.Close()

	// Connect to the Server
	conn, response, err := GorillaDial(wsURL)

	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	// Let's write something
	require.NoError(conn.Write(ctx, expectedMessageType, []byte(expectedMessage)))
}

// TestGorillaWebSocketAdapterReadsMessagesSuccessfully tests that a message
// can be read from the WebSocket connection successfully, and that the
// server side will write the message successfully.
func TestGorillaWebSocketAdapterReadsMessagesSuccessfully(t *testing.T) {
	// Setup
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		expectedMessageType = proxywebsocket.MessageTypeText
		expectedMessage     = "hello there"
	)

	handler := func(conn proxywebsocket.Conn, err error) {
		require.NoError(err)

		// Ensure that we can write a message
		require.NoError(conn.Write(ctx, expectedMessageType, []byte(expectedMessage)))
	}

	// Start the WebSocket server with a handler that will read a message and
	// and verify that it matches the expected message and message type
	wsURL, wsServer := NewGorillaWebsocketServer(handler)
	defer wsServer.Close()

	// Connect to the Server
	conn, response, err := GorillaDial(wsURL)

	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	// Let's read something
	messageType, message, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(expectedMessageType, messageType)
	require.Equal(expectedMessage, string(message))
}

// TestGorillaAdapterPipe tests that the piping works as expected to send
// messages through the connection, and that the connection will be closed
// successfully
func TestGorillaAdapterPipe(t *testing.T) {
	// Setup
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		expectedMessageType = proxywebsocket.MessageTypeText
		expectedMessage     = "hello there"

		expectedCloseStatus     = proxywebsocket.StatusNormalClosure
		expectedCloseReasonText = "test closing connection"
	)

	var wg1 sync.WaitGroup
	var wg2 sync.WaitGroup

	wg1.Add(1)
	wsURL1, wsServer1 := NewGorillaWebsocketServer(func(conn proxywebsocket.Conn, err error) {
		defer wg1.Done()
		require.NoError(err)
		require.NoError(conn.Write(ctx, expectedMessageType, []byte(expectedMessage)))
		require.NoError(conn.Close(expectedCloseStatus, expectedCloseReasonText))
	})
	defer wsServer1.Close()

	wg2.Add(1)
	wsURL2, wsServer2 := NewGorillaWebsocketServer(func(conn proxywebsocket.Conn, err error) {
		defer wg2.Done()
		require.NoError(err)

		forwardConn, response, err := GorillaDial(wsURL1)
		require.NoError(err)
		require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

		// This might have an error, but that *should* be fine, I think.
		_ = proxywebsocket.Bridge(ctx, forwardConn, conn)
	})
	defer wsServer2.Close()

	conn, response, err := GorillaDial(wsURL2)
	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	messageType, message, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(expectedMessageType, messageType)
	require.Equal(expectedMessage, string(message))

	_, _, err = conn.Read(ctx)
	require.Error(err)

	closeErr, ok := conn.IsCloseError(err)
	require.True(ok, "error should be a close error")
	require.Equal(closeErr.Status, expectedCloseStatus)
	require.Equal(closeErr.Reason, expectedCloseReasonText)

	wg1.Wait()
	wg2.Wait()
}

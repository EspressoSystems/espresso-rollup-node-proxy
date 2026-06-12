package websockettest

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"proxy/websocket"

	"github.com/stretchr/testify/require"
)

type BasicSuite struct {
	websocket.Upgrader
	websocket.Dialer
}

func NewBasicSuite(upgrader websocket.Upgrader, dialer websocket.Dialer) *BasicSuite {
	return &BasicSuite{
		Upgrader: upgrader,
		Dialer:   dialer,
	}
}

// RunBasicServerCloseTest tests that a connection of the WebSocket will
// be closed successfully, and that the reported reason and status code will
// be returned successfully when everything is setup correctly.
func (b *BasicSuite) RunBasicServerCloseTest(t *testing.T, newServer ServerCreator, status websocket.Status, message string) {
	// Setup
	require := require.New(t)
	ctx := t.Context()
	var wg sync.WaitGroup

	// This handler will immediately close any connection found
	handler := WebSocketHandlerFunc(func(conn websocket.Conn, err error) {
		defer wg.Done()
		// We should not have an issue connecting to the server
		require.NoError(err)

		// Let's try to close the connection
		require.NoError(conn.Close(status, message))

		time.Sleep(time.Millisecond * 100)

		_, _, err = conn.Read(ctx)
		require.Error(err)
	})

	server := newServer(b, handler)
	defer server.Close()

	wg.Add(1)
	// Connect to the Server
	conn, response, err := server.Connect(ctx, b)

	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	// Try to read something from the connection, to force the Close connection
	_, _, err = conn.Read(ctx)
	require.Error(err)

	closeErr, ok := conn.IsCloseError(err)
	require.True(ok, "error should be a close error")
	require.Equal(closeErr.Status, status)
	require.Equal(closeErr.Reason, message)

	require.Error(conn.Write(ctx, websocket.MessageTypeText, []byte("hello there")))

	wg.Wait()
}

// RunBasicClientCloseTest tests that a connection of the WebSocket will
// setup and disconnect correctly when the client sends a Close request.
func (b *BasicSuite) RunBasicClientCloseTest(t *testing.T, newServer ServerCreator, status websocket.Status, message string) {
	// Setup
	require := require.New(t)
	ctx := t.Context()

	var wg sync.WaitGroup
	// This handler will immediately close any connection found
	handler := WebSocketHandlerFunc(func(conn websocket.Conn, err error) {
		defer wg.Done()
		// We should not have an issue connecting to the server
		require.NoError(err)

		_, _, err = conn.Read(ctx)
		require.Error(err)
		closeErr, ok := conn.IsCloseError(err)
		require.True(ok, "err should be a close error")
		require.Equal(closeErr.Status, status)
		require.Equal(closeErr.Reason, message)

		// 		require.NoError(conn.Close(status, message))
	})

	// Start the WebSocket server with a handler that will close the
	server := NewServer(b, handler)
	// connection immediately
	defer server.Close()

	wg.Add(1)
	// Connect to the Server
	conn, response, err := server.Connect(ctx, b)

	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	// Try to read something from the connection, to force the Close connection
	require.NoError(conn.Close(status, message))

	_, _, err = conn.Read(ctx)
	require.Error(err)

	wg.Wait()
}

// RunBasicWriteMessageTest tests that a message
// can be written to the WebSocket connection successfully, and that the
// message is received on the other end of the connection.
func (b *BasicSuite) RunBasicWriteMessageTest(t *testing.T, newServer ServerCreator, expectedMessageType websocket.MessageType, expectedMessage []byte) {
	// Setup
	require := require.New(t)
	ctx := t.Context()

	var wg sync.WaitGroup
	handler := WebSocketHandlerFunc(func(conn websocket.Conn, err error) {
		defer wg.Done()
		require.NoError(err)

		// Ensure that we can read a message
		messageType, message, err := conn.Read(ctx)
		require.NoError(err)

		require.Equal(expectedMessageType, messageType)
		require.Equal(expectedMessage, message)

		_ = conn.Close(websocket.StatusNormalClosure, "goodbye")
	})

	// Start the WebSocket server with a handler that will read a message and
	// and verify that it matches the expected message and message type
	server := NewServer(b, handler)
	defer server.Close()

	wg.Add(1)
	// Connect to the Server
	conn, response, err := server.Connect(ctx, b)

	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	// Let's write something
	require.NoError(conn.Write(ctx, expectedMessageType, []byte(expectedMessage)))

	require.NoError(conn.Close(websocket.StatusNormalClosure, "goodbye"))

	_, _, err = conn.Read(ctx)
	require.Error(err)

	wg.Wait()
}

// RunBasicReadMessageTest tests that a message
// server side will write the message successfully.
// can be read from the WebSocket connection successfully, and that the
func (b *BasicSuite) RunBasicReadMessageTest(t *testing.T, newServer ServerCreator, expectedMessageType websocket.MessageType, expectedMessage []byte) {
	// Setup
	require := require.New(t)
	ctx := t.Context()

	var wg sync.WaitGroup
	handler := WebSocketHandlerFunc(func(conn websocket.Conn, err error) {
		defer wg.Done()
		require.NoError(err)

		// Ensure that we can write a message
		require.NoError(conn.Write(ctx, expectedMessageType, []byte(expectedMessage)))

		// Close the connection after writing the message, to ensure that the
		// client can read the message before the connection is closed.
		require.NoError(conn.Close(websocket.StatusNormalClosure, "goodbye"))

		_, _, err = conn.Read(ctx)
		require.Error(err)
	})

	// Start the WebSocket server with a handler that will read a message and
	// and verify that it matches the expected message and message type
	server := NewServer(b, handler)
	defer server.Close()

	wg.Add(1)
	// Connect to the Server
	conn, response, err := server.Connect(ctx, b)

	require.NoError(err)
	require.Equal(response.StatusCode, http.StatusSwitchingProtocols)

	// Let's read something
	messageType, message, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(expectedMessageType, messageType)
	require.Equal(expectedMessage, message)

	wg.Wait()
}

// CreateBasicServerCloseTest creates a test function that will perform the
// [BasicSuite.RunBasicServerCloseTest] test with the provided status and
// reason.
func (b *BasicSuite) CreateBasicServerCloseTest(newServer ServerCreator, status websocket.Status, reason string) func(t *testing.T) {
	return func(t *testing.T) {
		b.RunBasicServerCloseTest(t, newServer, status, reason)
	}
}

// CreateBasicClientCloseTest creates a test function that will perform the
// [BasicSuite.RunBasicClientCloseTest] test with the provided status and
// reason.
func (b *BasicSuite) CreateBasicClientCloseTest(newServer ServerCreator, status websocket.Status, reason string) func(t *testing.T) {
	return func(t *testing.T) {
		b.RunBasicClientCloseTest(t, newServer, status, reason)
	}
}

// CreateBasicReadsMessagesTest creates a test function that will perform the
// [BasicSuite.RunBasicReadMessageTest] test with the provided message type
// and message.
func (b *BasicSuite) CreateBasicWriteMessageTest(newServer ServerCreator, messageType websocket.MessageType, message []byte) func(t *testing.T) {
	return func(t *testing.T) {
		b.RunBasicWriteMessageTest(t, newServer, messageType, message)
	}
}

// CreateBasicReadsMessagesTest creates a test function that will perform the
// [BasicSuite.RunBasicReadMessageTest] test with the provided message type
// and message.
func (b *BasicSuite) CreateBasicReadMessageTest(newServer ServerCreator, messageType websocket.MessageType, message []byte) func(t *testing.T) {
	return func(t *testing.T) {
		b.RunBasicReadMessageTest(t, newServer, messageType, message)
	}
}

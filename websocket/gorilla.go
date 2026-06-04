package websocket

import (
	"context"
	"errors"
	"time"

	"github.com/gorilla/websocket"
)

// closeMessageTimeout is the amount of time to wait for a close message to
// be sent and the connection to be closed before timing out and returning
// an error.
const closeMessageTimeout = 5 * time.Second

// gorillaAdapter is an adapter that allows a gorilla/websocket connection
// to be used as a Socket.
type gorillaAdapter struct {
	conn *websocket.Conn
}

// AdaptGorilla adapts a gorilla/websocket connection to the Socket interface.
func AdaptGorilla(conn *websocket.Conn) Conn {
	return &gorillaAdapter{conn: conn}
}

// Compile-time type check assertion to ensure interface adherence
var _ Conn = (*gorillaAdapter)(nil)

// Close implements [Conn].
func (a *gorillaAdapter) Close(code Status, reason string) error {
	now := time.Now()
	deadline := now.Add(closeMessageTimeout)
	closeHandler := a.conn.CloseHandler()
	closeCh := make(chan struct{})

	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	// Overwrite the Close Handler
	a.conn.SetCloseHandler(func(code int, text string) error {
		// Signal that we're done waiting
		close(closeCh)

		if closeHandler == nil {
			return nil
		}
		return closeHandler(code, text)
	})

	writeError := a.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(int(code), reason),
		deadline,
	)

	var closeError *websocket.CloseError
	if errors.As(writeError, &closeError) && closeError != nil {
		// We're already closed.
		// Nothing further to do.
		return nil
	}

	if writeError != nil {
		// Failed to send a graceful close message.
		// try to close anyway.

		return errors.Join(writeError, a.conn.Close())
	}

	var timeoutError error

	// Wait for Close
	select {
	case <-closeCh:
		// The connection closed successfully, so we can return without error.
		break

	case <-ctx.Done():
		// Close timed out
		timeoutError = ctx.Err()
		break
	}

	return errors.Join(timeoutError, a.conn.Close())
}

// Read implements [Conn].
//
// The passed context is ignored, lest we end up corrupting the underlying
// connection by sending a partial frame.
func (a *gorillaAdapter) Read(ctx context.Context) (mesageType MessageType, message []byte, err error) {
	mt, message, err := a.conn.ReadMessage()
	return MessageType(mt), message, err
}

// Write implements [Conn].
//
// The passed context is ignored, lest we end up corrupting the underlying
// connection by sending a partial frame.
func (a *gorillaAdapter) Write(ctx context.Context, messageType MessageType, message []byte) error {
	return a.conn.WriteMessage(int(messageType), message)
}

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

	writeError := a.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(int(code), reason),
		deadline,
	)

	if _, ok := a.IsCloseError(writeError); ok {
		// We're already closed.
		// Nothing further to do.
		return nil
	}

	if errors.Is(writeError, websocket.ErrCloseSent) {
		// Close already sent.  This is probably fine.
		return a.conn.Close()
	}

	if writeError != nil {
		// Failed to send a graceful close message.
		// try to close anyway.

		return errors.Join(writeError, a.conn.Close())
	}

	// Finally perform an actual close.
	return a.conn.Close()
}

func (a *gorillaAdapter) closeOnCloseError(err error) {
	if _, ok := a.IsCloseError(err); ok {
		// We received a message indicating that we're closed.
		// Let's make sure that our socket is closed.
		_ = a.conn.Close()
	}
}

// Read implements [Conn].
//
// The passed context is ignored, lest we end up corrupting the underlying
// connection by sending a partial frame.
func (a *gorillaAdapter) Read(ctx context.Context) (messageType MessageType, message []byte, err error) {
	select {
	default:
	case <-ctx.Done():
		return messageType, message, ctx.Err()
	}
	mt, message, err := a.conn.ReadMessage()
	return MessageType(mt), message, err
}

// Write implements [Conn].
//
// The passed context is ignored, lest we end up corrupting the underlying
// connection by sending a partial frame.
func (a *gorillaAdapter) Write(ctx context.Context, messageType MessageType, message []byte) error {
	select {
	default:
	case <-ctx.Done():
		return ctx.Err()
	}
	err := a.conn.WriteMessage(int(messageType), message)
	return err
}

// IsCloseError implements [ErrorChecker]
func (a *gorillaAdapter) IsCloseError(err error) (CloseError, bool) {
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr != nil {
		return CloseError{
			Status: Status(closeErr.Code),
			Reason: closeErr.Text,
		}, true
	}

	return CloseError{}, false
}

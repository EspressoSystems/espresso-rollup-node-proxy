package websocket

import (
	"context"
	"errors"

	"github.com/coder/websocket"
)

type coderAdapter struct {
	conn *websocket.Conn
}

var _ Conn = (*coderAdapter)(nil)

// Close implements [Conn].
func (a *coderAdapter) Close(code Status, reason string) error {
	return a.conn.Close(websocket.StatusCode(code), reason)
}

// Read implements [Conn].
func (a *coderAdapter) Read(ctx context.Context) (mesageType MessageType, message []byte, err error) {
	t, message, err := a.conn.Read(ctx)
	return MessageType(t), message, err
}

// Write implements [Conn].
func (a *coderAdapter) Write(ctx context.Context, messageType MessageType, message []byte) error {
	return a.conn.Write(ctx, websocket.MessageType(messageType), message)
}

// IsCloseError implements [Conn].
func (a *coderAdapter) IsCloseError(err error) (CloseError, bool) {
	var closeError websocket.CloseError
	if errors.As(err, &closeError) {
		return CloseError{
			Status: Status(closeError.Code),
			Reason: closeError.Reason,
		}, true
	}

	return CloseError{}, false
}

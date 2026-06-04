package websocket

import (
	"context"

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

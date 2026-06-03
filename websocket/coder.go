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
func (a *coderAdapter) Close(code int, reason string) error {
	return a.conn.Close(websocket.StatusCode(code), reason)
}

// Read implements [Conn].
func (a *coderAdapter) Read(ctx context.Context) (mesageType int, message []byte, err error) {
	t, message, err := a.conn.Read(ctx)
	return int(t), message, err
}

// Write implements [Conn].
func (a *coderAdapter) Write(ctx context.Context, messageType int, message []byte) error {
	return a.conn.Write(ctx, websocket.MessageType(messageType), message)
}

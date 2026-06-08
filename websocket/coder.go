package websocket

import (
	"context"
	"errors"
	"net/http"

	"github.com/coder/websocket"
)

type coderAdapter struct {
	conn *websocket.Conn
}

var _ Conn = (*coderAdapter)(nil)

// AdaptCoder adapts a coder/websocket connection to the Conn interface.
func AdaptCoder(conn *websocket.Conn) Conn {
	return &coderAdapter{conn: conn}
}

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

// SubProtocol implements [SubProtoRetriever]
func (a *coderAdapter) SubProtocol() string {
	return a.conn.Subprotocol()
}

type coderUpgrader struct{}

var _ Upgrader = (*coderUpgrader)(nil)

// Upgrade implements [Upgrader]
func (c *coderUpgrader) Upgrade(w http.ResponseWriter, r *http.Request, options ...UpgradeOption) (Conn, error) {
	config := UpgradeConfigWithOptions(options...)

	if config.Headers != nil {
		for key, values := range config.Headers {
			w.Header()[key] = values
		}
	}

	// Apply the config to the Accept options
	acceptOptions := websocket.AcceptOptions{
		Subprotocols: config.SubProtocols,
	}

	conn, err := websocket.Accept(w, r, &acceptOptions)
	if err != nil && conn == nil {
		return nil, err
	}
	return AdaptCoder(conn), err
}

func CoderUpgrader() Upgrader {
	return &coderUpgrader{}
}

type coderDialer struct{}

var _ Dialer = (*coderDialer)(nil)

// Dial implements [Dialer]
func (d *coderDialer) Dial(ctx context.Context, urlString string, options ...DialerOption) (Conn, *http.Response, error) {
	config := DialerConfigWithOptions(options...)

	// Apply the config to the Dialer

	dialOptions := websocket.DialOptions{
		HTTPHeader:   config.Headers,
		Subprotocols: config.SubProtocols,
	}

	conn, response, err := websocket.Dial(ctx, urlString, &dialOptions)
	if err != nil || conn == nil {
		return nil, response, err
	}

	return AdaptCoder(conn), response, err
}

func CoderDialer() Dialer {
	return &coderDialer{}
}

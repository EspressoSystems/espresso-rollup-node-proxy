package websocket

import (
	"context"
	"errors"
	"math"
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
	defer func() {
		// We're not concerned with this error, as should should work no
		// matter what.
		_ = a.conn.CloseNow()
	}()
	err := a.conn.Close(websocket.StatusCode(code), reason)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return nil
	}

	return err
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

type coderUpgrader struct {
	options []UpgradeOption
}

var _ Upgrader = (*coderUpgrader)(nil)

// Upgrade implements [Upgrader]
func (c *coderUpgrader) Upgrade(w http.ResponseWriter, r *http.Request, options ...UpgradeOption) (Conn, error) {
	config := UpgradeConfigWithOptions(c.options...)
	ApplyMultipleUpgradeOptions(options)(&config)

	// ReadSizeLimit larger than Max int64 is not supported by Gorilla,
	// so we should return an error if the user tries to set it to something
	// larger than that.
	if config.ReadSizeLimit > math.MaxInt64 {
		return nil, ErrSpecifiedReadSizeLimitTooLarge
	}

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
	if err != nil || conn == nil {
		return nil, err
	}

	if config.ReadSizeLimit > 0 {
		// This should be a safe cast to int64 as we checked the size already.
		conn.SetReadLimit(int64(config.ReadSizeLimit))
	}

	return AdaptCoder(conn), err
}

// CoderUpgrader returns an [Upgrader] that uses the
// github.com/coder/websocket to upgrader the http connection.
//
// The provided [UpgradeOption]s will automatically be applied to every
// invocation of [Upgrader.Upgrade] before the passed in [UpgradeOption]s
// meaning these can be overwritten if desired.
func CoderUpgrader(options ...UpgradeOption) Upgrader {
	return &coderUpgrader{
		options: options,
	}
}

type coderDialer struct {
	options []DialerOption
}

var _ Dialer = (*coderDialer)(nil)

// Dial implements [Dialer]
func (d *coderDialer) Dial(ctx context.Context, urlString string, options ...DialerOption) (Conn, *http.Response, error) {
	config := DialerConfigWithOptions(d.options...)
	ApplyMultipleDialerOptions(options)(&config)

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

// CoderDialer returns a Dialer that uses the github.com/coder/websocket
// package to dial WebSocket connections.
//
// The options supplied to the this [Dialer] will automatically be applied
// to all invocations of [Dialer.Dial] before the passed in [DialerOption]
// meaning they can be overwritten if desired.
func CoderDialer(options ...DialerOption) Dialer {
	return &coderDialer{
		options: options,
	}
}

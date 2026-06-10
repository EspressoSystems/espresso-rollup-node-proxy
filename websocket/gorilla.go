package websocket

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
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

	if errors.Is(writeError, websocket.ErrCloseSent) || errors.Is(writeError, net.ErrClosed) {
		// Close already sent or connection already closed.  This is fine.
		_ = a.conn.Close()
		return nil
	}

	if writeError != nil {
		// Failed to send a graceful close message.
		// try to close anyway.

		return errors.Join(writeError, a.conn.Close())
	}

	// Finally perform an actual close.
	return a.conn.Close()
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

	if ctx.Done() != nil {
		// Setup a goroutine for handling context cancellation on Read requests.
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				// Force a cancellation through the current deadline
				_ = a.conn.SetReadDeadline(time.Now())
			case <-done:
			}
		}()
	}

	mt, message, err := a.conn.ReadMessage()
	return MessageType(mt), message, err
}

// Write implements [Conn].
//
// The passed context is ignored, lest we end up corrupting the underlying
// connection by sending a partial frame.
func (a *gorillaAdapter) Write(ctx context.Context, messageType MessageType, message []byte) (err error) {
	select {
	default:
	case <-ctx.Done():
		return ctx.Err()
	}

	if ctx.Done() != nil {
		// Setup a goroutine for handling context cancellation on write requests.
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				// Force a cancellation through the current deadline
				_ = a.conn.SetWriteDeadline(time.Now())
			case <-done:
			}
		}()
	}

	err = a.conn.WriteMessage(int(messageType), message)
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

// SubProtocol implements [SubProtoRetriever]
func (a *gorillaAdapter) SubProtocol() string {
	return a.conn.Subprotocol()
}

// gorillaUpgrader is an Upgrader implementation that uses the Gorilla
// WebSocket implementation to perform the upgrade, and then adapts the
// resulting connection to the Conn interface using [AdaptGorilla].
type gorillaUpgrader struct {
	options []UpgradeOption
}

// Compile-time type check assertion to ensure interface adherence.
var _ Upgrader = (*gorillaUpgrader)(nil)

// Upgrade implements [Upgrader]
func (u *gorillaUpgrader) Upgrade(w http.ResponseWriter, r *http.Request, options ...UpgradeOption) (Conn, error) {
	config := UpgradeConfigWithOptions(u.options...)
	ApplyMultipleUpgradeOptions(options)(&config)

	// ReadSizeLimit larger than Max int64 is not supported by Gorilla,
	// so we should return an error if the user tries to set it to something
	// larger than that.
	if config.ReadSizeLimit > math.MaxInt64 {
		return nil, ErrSpecifiedReadSizeLimitTooLarge
	}

	var upgrader websocket.Upgrader

	// Apply the options to the Gorilla Upgrader
	upgrader.Subprotocols = config.SubProtocols

	conn, err := upgrader.Upgrade(w, r, config.Headers)
	if err != nil || conn == nil {
		return nil, err
	}

	if config.ReadSizeLimit > 0 {
		// This should be a safe cast to int64 as we checked the size already.
		conn.SetReadLimit(int64(config.ReadSizeLimit))
	}

	return AdaptGorilla(conn), err
}

// GorillaUpgrader creates an [Upgrader] that utilities the
// github.com/gorilla/websocket implementation to perform the upgrade.
//
// The [UpgradeOption]s passed to this function will be applied to any call of
// [Upgrader.Upgrade] before the pased in [UpgradeOption]s, so they can be
// overwritten if desired.
func GorillaUpgrader(options ...UpgradeOption) Upgrader {
	return &gorillaUpgrader{
		options: options,
	}
}

// gorillaDialer is a Dialer implementation that uses the Gorilla WebSocket
// library.
type gorillaDialer struct {
	options []DialerOption
}

// Compile-time type check assertion to ensure interface adherence.
var _ Dialer = (*gorillaDialer)(nil)

// Dial implements [Dialer]
func (d *gorillaDialer) Dial(ctx context.Context, urlString string, options ...DialerOption) (Conn, *http.Response, error) {
	config := DialerConfigWithOptions(d.options...)
	ApplyMultipleDialerOptions(options)(&config)
	dialer := *websocket.DefaultDialer

	// Apply the options to the Dialer, and whatever other functions we need.
	dialer.Subprotocols = config.SubProtocols

	conn, response, err := dialer.DialContext(ctx, urlString, config.Headers)
	if err != nil || conn == nil {
		return nil, response, err
	}
	return AdaptGorilla(conn), response, err
}

// GorillaDialer creates [Dialer] that utilies the github.com/gorilla/websocket
// package to perform the establishing of the WebSocket connection.
//
// These options will automatically be applied to any calls of [Dialer.Dial]
// and will be applied before incoming [DialerOption], so they can be
// overwritten if desired.
func GorillaDialer(options ...DialerOption) Dialer {
	return &gorillaDialer{
		options: options,
	}
}

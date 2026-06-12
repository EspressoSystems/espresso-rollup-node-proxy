package http_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	proxyhttp "proxy/http"
	"proxy/log/logutil"
	"proxy/websocket"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// fakeWebSocketUpgrader is a test utility type that implements the
// the [websocket.Upgrader] interface and allows for the injection of
// custom behavior with the handler
type fakeWebSocketUpgrader struct {
	websocket.Upgrader
	fn func(conn websocket.Conn, err error)
}

var _ websocket.Upgrader = (*fakeWebSocketUpgrader)(nil)

// Upgrade implements [websocket.Upgrader].
func (u *fakeWebSocketUpgrader) Upgrade(w http.ResponseWriter, r *http.Request, options ...websocket.UpgradeOption) (conn websocket.Conn, err error) {
	conn, err = u.Upgrader.Upgrade(w, r, options...)
	u.fn(conn, err)
	return conn, err
}

// FakeWebSocketUpgrader is a test utility function that creates a new
// [websocket.Upgrader] that allows for the injection of custom behavior with
// the given function that allows for inspection of the [websocket.Upgrader]
// result.
func FakeWebSocketUpgrader(upgrader websocket.Upgrader, fn func(conn websocket.Conn, err error)) websocket.Upgrader {
	return &fakeWebSocketUpgrader{
		Upgrader: upgrader,
		fn:       fn,
	}
}

// errConnClosed is a test utility type that implements the [error] interface
// used to signify a closed connection error
type errConnClosed struct{}

// Error implements [error].
func (*errConnClosed) Error() string {
	return "conn closed"
}

// fakeWebSocketConn is a test utility type that implements the
// [websocket.Conn] and [websocket.Upgrader] interfaces.
//
// The behavior of this implementation is determined by the constant
// behaviors listed below.
type fakeWebSocketConn int

const (
	// behaviorNormal is the default behavior of the fakeWebSocketConn, where
	//all operations succeed with their normal expected behavior.
	//
	// - CloseError on Write
	// - CloseError on Read calls
	// - No SubProtocol
	// - No error on Close calls
	// - Upgrade call succeeds with no error
	behaviorNormal fakeWebSocketConn = 1 << iota

	// behaviorReadErrEOF causes the Read method to return an EOF error,
	// indicating a non-standard error being returned.
	behaviorReadErrEOF

	// behaviorUpgradeErr causes the Upgrade method to return an error,
	// indicating that the Upgrade call did not succeed.
	behaviorUpgradeErr

	// behaviorCloseErr causes the Close method to return an error, indicating
	// that the Close call did not succeed
	behaviorCloseErr

	// behaviorCloseErrClosed causes the Close method to return an error that
	// the connection is already closed, returning a CloseError.
	behaviorCloseErrClosed
)

var (
	_ websocket.Conn     = fakeWebSocketConn(0)
	_ websocket.Upgrader = fakeWebSocketConn(0)
)

// Close implements [websocket.Conn].
func (f fakeWebSocketConn) Close(status websocket.Status, reason string) error {
	if f&behaviorCloseErr == behaviorCloseErr {
		return ErrCloseFailed
	}
	if f&behaviorCloseErrClosed == behaviorCloseErrClosed {
		return new(errConnClosed)
	}
	return nil
}

// IsCloseError implements [websocket.Conn].
func (f fakeWebSocketConn) IsCloseError(err error) (websocket.CloseError, bool) {
	var connClosedErr *errConnClosed
	if errors.As(err, &connClosedErr) {
		return websocket.CloseError{
			Status: websocket.StatusNormalClosure,
			Reason: "connection closed",
		}, true
	}

	return websocket.CloseError{}, false
}

// Read implements [websocket.Conn].
func (f fakeWebSocketConn) Read(ctx context.Context) (messageType websocket.MessageType, message []byte, err error) {
	if f&behaviorReadErrEOF == behaviorReadErrEOF {
		return messageType, message, io.EOF
	}

	return messageType, message, new(errConnClosed)
}

// SubProtocol implements [websocket.Conn].
func (f fakeWebSocketConn) SubProtocol() string {
	return ""
}

// Write implements [websocket.Conn].
func (f fakeWebSocketConn) Write(ctx context.Context, messageType websocket.MessageType, message []byte) error {
	return new(errConnClosed)
}

var ErrUpgradeFailed = errors.New("upgrade failed")

// Upgrade implements [websocket.Upgrader].
func (f fakeWebSocketConn) Upgrade(w http.ResponseWriter, r *http.Request, options ...websocket.UpgradeOption) (conn websocket.Conn, err error) {
	if f&behaviorUpgradeErr == behaviorUpgradeErr {
		return nil, ErrUpgradeFailed
	}

	return f, nil
}

// TestWebSocketBridgeSuccess tests the successful upgrade and handling of
// a WebSocket connection through the WebSocket bridge.
//
// This test ensures that the server side handling of the [websocket.Upgrader]
// behaves as expected.
func TestWebSocketBridgeSuccess(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	captureLogger := logutil.NewCaptureLogger(nil)
	handler := proxyhttp.WebSocketUpgrader(
		log.NewLogger(captureLogger),
		FakeWebSocketUpgrader(
			behaviorNormal,
			func(conn websocket.Conn, err error) {
				require.Equal(behaviorNormal, conn)
			},
		),
	)

	handler.ServeHTTP(recorder, &http.Request{})

	require.Len(captureLogger.Entries, 0, "expected no log entries")
}

// testWebSocketBridgeUpgradeError tests the behavior of the WebSocket bridge
// when the Upgrade call on [websocket.Upgrader] returns an error.
//
// In this case the error should be passed to the inspection function
// and the connection should be nil.
func TestWebSocketBridgeUpgradeError(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	captureLogger := logutil.NewCaptureLogger(nil)
	handler := proxyhttp.WebSocketUpgrader(
		log.NewLogger(captureLogger),
		FakeWebSocketUpgrader(
			behaviorUpgradeErr,
			func(conn websocket.Conn, err error) {
				require.Error(err)
				require.Nil(conn)
			},
		),
	)

	handler.ServeHTTP(recorder, &http.Request{})

	require.Len(captureLogger.Entries, 1, "expected a single log entry")
}

// TestWebSocketBridgeReadError tests the behavior of the WebSocket bridge
// when calls to [websocket.Conn.Read] return someting other than a CloseError.
//
// This should result in a log entry on the server side.
func TestWebSocketBridgeReadError(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	captureLogger := logutil.NewCaptureLogger(nil)
	handler := proxyhttp.WebSocketUpgrader(
		log.NewLogger(captureLogger),
		FakeWebSocketUpgrader(
			behaviorReadErrEOF,
			func(conn websocket.Conn, err error) {
				require.NoError(err)
				require.NotNil(conn)
			},
		),
	)

	handler.ServeHTTP(recorder, &http.Request{})

	require.Len(captureLogger.Entries, 1, "expected a single log entry")
}

// TestWebSocketBridgeReadError tests the behavior of the WebSocket bridge
// when the Close function returns an error that is not a CloseError.
//
// In this case, the error should be logged.
func TestWebSocketBridgeCloseError(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	captureLogger := logutil.NewCaptureLogger(nil)
	handler := proxyhttp.WebSocketUpgrader(
		log.NewLogger(captureLogger),
		FakeWebSocketUpgrader(
			behaviorCloseErr,
			func(conn websocket.Conn, err error) {
				require.NoError(err)
				require.NotNil(conn)
			},
		),
	)

	handler.ServeHTTP(recorder, &http.Request{})

	require.Len(captureLogger.Entries, 1, "expected a single log entry")
}

// TestWebSocketBridgeCloseErrorAlreadyClosed tests the behavior of the
// WebSocket bridge when the Close function returns a CloseError.
//
// In this case the error should *NOT* be logged, as it is expected.
func TestWebSocketBridgeCloseErrorAlreadyClosed(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	captureLogger := logutil.NewCaptureLogger(nil)
	handler := proxyhttp.WebSocketUpgrader(
		log.NewLogger(captureLogger),
		FakeWebSocketUpgrader(
			behaviorCloseErrClosed,
			func(conn websocket.Conn, err error) {
				require.NoError(err)
				require.NotNil(conn)
			},
		),
	)

	handler.ServeHTTP(recorder, &http.Request{})

	require.Len(captureLogger.Entries, 0, "expected no log entries")
}

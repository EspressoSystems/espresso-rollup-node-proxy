package websocket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// OpCode represents WebSocket OpCodes as defined in RFC 6455 Section 5.2, and
// within the Registry Section 11.8:
// https://www.rfc-editor.org/rfc/rfc6455.txt
type OpCode int

const (
	// OpCodeContinuation indicates that the message is a continuation message.
	// The payload is interpreted as a continuation of the previous message.
	//
	// NOTE: This should **NEVER** be used by the user, and is only included
	// for reference, and completeness.
	OpCodeContinuation OpCode = 0

	// OpCodeTextFrame indicates that the message is a text message. The message
	// payload is interpreted as a UTF-8 encoded text data.
	OpCodeTextFrame OpCode = 1

	// OpCodeBinaryFrame indicates that the message is a binary message. The
	// message payload is not considered to be any format in particular.
	OpCodeBinaryFrame OpCode = 2

	// OpCodeCloseFrame indicates a close control message. The message payload
	// is interpreted as UTF-8 encoded text data. The message payload is
	// optional and may be empty.
	OpCodeCloseFrame OpCode = 8

	// OpCodePingFrame indicates a ping control message.  The message payload
	// is interepreted as UTF-8 encoded text data.  The message payload is
	// optional and may be empty.
	OpCodePingFrame OpCode = 9

	// OpCodePongFrame indicates a pong control message. The message payload
	// is interpreted as UTF-8 encoded text data. The message payload is
	// optional and may be empty.
	OpCodePongFrame OpCode = 10
)

// MessageType is effectively an alias for OpCode, and is used to represent
// the type of a websocket message.  It is used in the [Conn.Read] and
// [Conn.Write] methods
type MessageType int

const (
	// MessageTypeContinuation is a message type that indicates that the message
	// is in a text.  The message payload should be in a UTF-8 encoded format.
	MessageTypeText = MessageType(OpCodeTextFrame)

	// MessageTypeBinary is a message type that indicates that the message is in
	// a binary format.  The message payload is not considered to be in any
	// format specifc format based on the implementation itself, but may match
	// the expected protocol format of the message.
	MessageTypeBinary = MessageType(OpCodeBinaryFrame)
)

// Status represents WebSocket Status Codes as defined in RFC 6455 Section
// 7.4:
// https://www.rfc-editor.org/rfc/rfc6455.txt
// https://datatracker.ietf.org/doc/html/rfc6455#section-7.4
type Status int

const (
	// StatusNormalClosure indicates a normal closure, meaning that the
	// connection was closed successfully and without any issues.
	StatusNormalClosure Status = 1000

	// StatusGoingAway indicates that the connection is "going away"", such
	// as a server going down or a browser having navigated away from a page.
	StatusGoingAway Status = 1001

	// StatusProtocolError indicates that an endpoint is terminating the
	// connection due to a protocol error.
	StatusProtocolError Status = 1002

	// StatusUnsupportedData indicates that an endpoint is terminating the
	// connection because it has received a type of data it cannot accept
	// (e.g., an endpoint that understand only text data MAY send this if
	// it receives a binary message).
	StatusUnsupportedData Status = 1003

	// StatusNoStatusReceived is a reserved value and MUST NOT be set as a
	// status code in a Close control by an endpoint.  It is designated for use
	// in applications expecting a status code to indicate that no status code
	// was actually present.
	StatusNoStatusReceived Status = 1005

	// StatusAbnormalClosure is a reserved value that MUST NOT be set as a
	// status code in a Close ocntrol frame by an endpoint. It is designated for
	// use in applications expecting a status code to indicate that the
	// connection was closed abnormally, e.g., without sending or receiving a
	// Close control
	StatusAbnormalClosure Status = 1006

	// StatusInvalidFramePayloadData indicates that an endpoint is terminating
	// the connection because it has received data within a message that was not
	// consistent with the type of the message (e.g., non-UTF-8 data within a
	// text message).
	StatusInvalidFramePayloadData Status = 1007

	// StatusPolicyViolation indicates that an endpoint is terminating the
	// connection because it has received a message that violates its policy.
	// This is a generic status code that can be returned when there is no
	// other more suitable status code (e.g., 1003 or 1009) or if there
	// is a need to hide specific details about the policy.
	StatusPolicyViolation Status = 1008

	// StatusMessageTooBig indicates that an endpoint is terminating the
	// connection because it has received a message that is too big for it to
	// process.
	StatusMessageTooBig Status = 1009

	// StatusMandatoryExtension indicates that an endpoint (client) is
	// terminating the connection because it has expected the server to
	// negotiate one or more extension, but the server didn't return them in
	// the response message of the WebSocket handshake.  The list of extensions
	// that are needed SHOULD appear in the /reason/ part of the Close frame.
	//
	// NOTE: that this status code is not used by the server, because it
	// can fail the WebSocket handshake instead.
	StatusMandatoryExtension Status = 1010

	// StatusInternalServerError indicates that a server is terminating the
	// connection because it encountered an unexpected condition that prevented
	// it from fulfilling the request.
	StatusInternalServerError Status = 1011

	// StatusTLSHandshake is a reserved value and MUST NOT be set as a status
	// code in a Close control frame by an endpoint.  It is designated for use
	// in applications expecting a status code to indicate that the connection
	// was closed due to a failure to perform a TLS handshake
	// (e.g., the server certificate can't be verified).
	StatusTLSHandshake Status = 1015
)

// Closer is a specific interface that defines the method for closing a
// WebSocket connection.
type Closer interface {
	// Close will close the underlying websocket connection.
	Close(status Status, reason string) error
}

// Writer is an interface that defines the methods for writin responses to a
// WebSocket connection in response to a request.
//
// NOTE: The handler can send any number of messages in response to a request,
// and is not limited to sending only a single message.  Additionally, the
// handler is not required to send a response message, and can choose to send
// no messages at all in response to a request.
type Writer interface {
	// Write writes a message with the given message type and payload.
	//
	// Allowed message types are [MessageTypeText] and [MessageTypeBinary]
	Write(ctx context.Context, messageType MessageType, message []byte) error
}

// Reader is an interface that defines the methods for reading messages from
// a WebSocket connection.  It is designed to read individual messages from
// the WebSocket connection.
type Reader interface {
	// Read reads a message from the websocket connection.  It returns the
	// message type, the message payload, and any error that occurred while
	// attempting to read the message.
	Read(ctx context.Context) (mesageType MessageType, message []byte, err error)
}

// CloseError represents an error that indicates that the WebSocket connection
// has been closed.
//
// This can be returned from [Reader.Read] and [Writer.Write] calls.
type CloseError struct {
	Status Status
	Reason string
}

// Error implements error
func (e CloseError) Error() string {
	return fmt.Sprintf("websocket connection closed with status %d: %s", e.Status, e.Reason)
}

// ErrorChecker is an interface that allows for various implementations of
// the WebSocket abstraction to inspect the error for specific types
type ErrorChecker interface {
	// IsCloseError checks if the given error is a [CloseError], and if so, it
	// will return the close error, and a boolean indicating that it was
	// a [CloseError].
	IsCloseError(err error) (CloseError, bool)
}

// SubProtoRetriever is an interface that allows for various implementations of
// the WebSocket abstraction to retrieve the negotiated subprotocol for the
// WebSocket connection.  This is useful for implementations that need to know
// the negotiated subprotocol in order to properly handle the messages being
// sent to the server.
type SubProtoRetriever interface {
	// SubProtocol returns the negotiated subprotocol for the WebSocket
	// connection.
	SubProtocol() string
}

// Conn represents the interactive interface that we wish to interact with
// for WebSocket connections.  It is designed to be a simple interface that
// can be easily implemented by various implementsations of WebSocket
// connections, and is intended to be used as the main interface for handling
// WebSocket connections in the library.
type Conn interface {
	Closer
	Writer
	Reader
	ErrorChecker
	SubProtoRetriever
}

// applyOptionsToConfig is a helper function that applies the given options
// to a fresh configuration type, and returns the resulting configuration
// type.
func applyOptionsToConfig[Config any, Option ~func(*Config)](options ...Option) (config Config) {
	applyOptionsToExistingConfig(&config, options)
	return config
}

// applyOptionsToExistingConfig is a helper function that applies the given
// options to the given pointer to a Config type.
func applyOptionsToExistingConfig[Config any, Option ~func(*Config)](config *Config, options []Option) {
	for _, opt := range options {
		opt(config)
	}
}

// UpgradeConfig represents the configuration options for upgrading an HTTP
// request to a WebSocket connection.
type UpgradeConfig struct {
	Headers       http.Header
	SubProtocols  []string
	ReadSizeLimit uint64
}

// UpgradeOption represents a functional option for configuring the
// [UpgradeConfig] when calling [Upgrader.Upgrade] on an [Upgrader].
type UpgradeOption func(c *UpgradeConfig)

// SetUpgradeHeaders sets the headers to be sent in the WebSocket handshake
// response.
func SetUpgradeHeaders(headers http.Header) UpgradeOption {
	return func(c *UpgradeConfig) {
		c.Headers = headers
	}
}

// SetUpgradeSubProtocols sets the subprotocols to list as being supported
// in the Upgrader for subprotocol negotiation when upgrading an HTTP request
// to a WebSocket.
func SetUpgradeSubProtocols(subProtocols []string) UpgradeOption {
	return func(c *UpgradeConfig) {
		c.SubProtocols = subProtocols
	}
}

// SetUpgradeReadSizeLimit sets the maximum size of a message that can be
// read in a single message.
//
// Exceeding this limit will result in the [Reader.Read] call failing with
// an error, and the connection being terminated.
func SetUpgradeReadSizeLimit(limit uint64) UpgradeOption {
	return func(c *UpgradeConfig) {
		c.ReadSizeLimit = limit
	}
}

// ApplyMultipleUpgradeOptions is a helper function that takes in a slice of
// multiple [UpgradeOption]s and returns an [UpgradeOption] that applies all
// of them.
func ApplyMultipleUpgradeOptions(options []UpgradeOption) UpgradeOption {
	return func(c *UpgradeConfig) {
		applyOptionsToExistingConfig(c, options)
	}
}

// UpgradeConfigWithOptions applies the given options to a new [UpgradeConfig]
// and returns the resulting [UpgradeConfig].
func UpgradeConfigWithOptions(options ...UpgradeOption) (config UpgradeConfig) {
	return applyOptionsToConfig(options...)
}

// Upgrader is an interface that defines the method to upgrade an HTTP
// connection to a WebSocket connection.
type Upgrader interface {
	// Upgrade upgrades the HTTP connection to a WebSocket connection. It returns
	// a [Conn] upon success, or any error encountered if it is not.
	Upgrade(w http.ResponseWriter, r *http.Request, options ...UpgradeOption) (Conn, error)
}

// DialerConfig represents the configuration options for dialing a WebSocket
// connection.
type DialerConfig struct {
	Headers      http.Header
	SubProtocols []string
}

// DialerOption represents a functional option for configuring the
// [DialerConfig] when calling [Dialer.Dial] on a [Dialer].
type DialerOption func(c *DialerConfig)

// SetDialerHeaders sets the headers to be sent in the WebSocket handshake
// request when dialing a WebSocket server.
func SetDialerHeaders(headers http.Header) DialerOption {
	return func(c *DialerConfig) {
		c.Headers = headers
	}
}

// SetDialerSubProtocols sets the subprotocols to be sent in the WebSocket
// request for subprotocol negotiation when dialing a WebSocket server.
func SetDialerSubProtocols(subProtocols []string) DialerOption {
	return func(c *DialerConfig) {
		c.SubProtocols = subProtocols
	}
}

// ApplyMultipleDialerOptions is a helper function that takes in a
// slice of [DialerOption]s and returns a [DialerOption] that will apply
// all of the options.
func ApplyMultipleDialerOptions(options []DialerOption) DialerOption {
	return func(c *DialerConfig) {
		applyOptionsToExistingConfig(c, options)
	}
}

// DialerConfigWithOptions applies the given options to a new [DialerConfig]
// and returns the resulting [DialerConfig].
func DialerConfigWithOptions(options ...DialerOption) (config DialerConfig) {
	return applyOptionsToConfig(options...)
}

// Dialer is an interface that allows for the connection to a WebSocket
// server.
type Dialer interface {
	// Dial performs a connection to an HTTP 1.1 Server with a WebSocket upgrade
	// request.
	Dial(ctx context.Context, urlString string, options ...DialerOption) (Conn, *http.Response, error)
}

// ErrSpecifiedReadSizeLimitTooLarge is an error that is returned when the
// user specifies a read size limit the exceeds the maximum supported
// size for the specific implementation.
//
// NOTE: In the case of Gorilla, and Coder, this limit is max int64.
var ErrSpecifiedReadSizeLimitTooLarge = errors.New("specified read size limit exceeds maximum supported the implementation")

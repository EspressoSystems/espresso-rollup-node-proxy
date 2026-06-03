package websocket

import "context"

const (
	// MessageTypeText indicates that the message is a text message. The message
	// payload is interpreted as a UTF-8 encoded text data.
	MessageTypeText = 1

	// MessageTypeBinary indicates that the message is a binary message. The
	// message payload is not considered to be any format in particular.
	MessageTypeBinary = 2

	// MessageTypeClose indicates a close control message. The message payload
	// is interpreted as UTF-8 encoded text data. The message payload is
	// optional and may be empty.
	MessageTypeClose = 8

	// MessageTypePing indicates a ping control message.  The message payload
	// is interepreted as UTF-8 encoded text data.  The message payload is
	// optional and may be empty.
	MessageTypePing = 9

	// MessageTypePong indicates a pong control message. The message payload
	// is interpreted as UTF-8 encoded text data. The message payload is
	// optional and may be empty.
	MessageTypePong = 10
)

// Closer is a specific interface that defines the method for closing a
// WebSocket connection.
type Closer interface {
	// Close will close the underlying websocket connection.
	Close(code int, reason string) error
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
	Write(ctx context.Context, messageType int, message []byte) error
}

// Reader is an interface that defines the methods for reading messages from
// a WebSocket connection.  It is designed to read individual messages from
// the WebSocket connection.
type Reader interface {
	// Read reads a message from the websocket connection.  It returns the
	// message type, the message payload, and any error that occurred while
	// attempting to read the message.
	Read(ctx context.Context) (mesageType int, message []byte, err error)
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
}

// Middleware is a function that takes a Connection and returns a Connection.
type Middleware func(Conn) Conn

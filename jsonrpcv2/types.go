package jsonrpcv2

import (
	"fmt"
)

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	Version = "2.0"
)

func invalidJSONRPCVersion(version string) error {
	return Error{
		Code:    CodeParseError,
		Message: fmt.Sprintf("invalid json-rpc version received: %s", version),
	}
}

// ID is a type alias for any.  This is for convenience, so we can potentially
// experiment with replacing the ID field in the future.
//
// ID is expected to be one of the following types:
// - nil
// - string
// - number (specifically an int)
type ID = any

// NotificationID is a type that is utilized to help differentiate the case
// of the ID on a JSON-RPC Request from an explicit `null` ID value.
//
// This object signifies that the Request is **NOT** an ID.
type NotificationID struct{}

// Params is a type alias for any.  This is for convenience, so we can
// potentially experiment with replacing the Params field in the future.
//
// Params is expected to be one of the following type:
// - nil
// - []any
// - map[string]any
type Params = any

// ExtraFields represents any additional fields that may be present in a
// JSON-RPC request or response, but are not part of the standard
// specification.
// This allows for extensibility and flexibility in handling custom fields
// without breaking the core functionality of the library.
type ExtraFields map[string]any

// Request represents a JSON-RPC 2.0 request
// https://www.jsonrpc.org/specification#request_object
type Request struct {
	// The ID field for the `Request` object is a little odd and has some
	// subtle nuances.
	//
	// In general, the `ID` field is expected to be either a `string` or a
	// `numeric` value.  However, there are two special cases which will
	// be difficult to disambiguate from each other.
	//
	// The first case if if the `Request` is a `Notification` instead of a
	// `Request`.  In this case, the `ID` field **SHOULD** be omitted enitrely
	// from the encoded JSON, informating the server that no response is desired
	// or warranted.
	//
	// The second case is setting it to `null`.  In this case, this does **NOT**
	// mean that this ia `Notification`, but it is primarily a hold-over due to
	// an edge-case in JSON-RPC `Response`s where we are uncertain of what the
	// ID should be, so we set it explicitly to `null`.
	ID     ID
	Method string
	Params Params
	ExtraFields
}

// Error represents a JSON-RPC 2.0 error object
// https://www.jsonrpc.org/specification#error_object
type Error struct {
	Code    int
	Message string
	Data    any
	ExtraFields
}

// Response represents a JSON-RPC 2.0 response object
// https://www.jsonrpc.org/specification#response_object
type Response struct {
	ID     ID
	Result any
	Error  *Error
	ExtraFields
}

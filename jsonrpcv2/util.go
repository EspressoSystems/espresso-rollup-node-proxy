package jsonrpcv2

import "encoding/json"

// CreateGeneralErrorResponse is a helper function that creates a JSON-RPC
// error response with the given id, code, and message.
//
// writeJSONRPCError writes a JSON-RPC error response with the given id, code, and message.
// If the id is nil, it defaults to "null" as per the JSON-RPC specification
// https://www.jsonrpc.org/specification#error_object
func CreateGeneralErrorResponse(id ID, code int, message string) Response {
	if id == nil {
		id = json.RawMessage("null")
	}
	return Response{
		ID: id,
		Error: &Error{
			Code:    code,
			Message: message,
		},
	}
}

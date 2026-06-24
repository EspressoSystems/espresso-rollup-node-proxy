package jsonrpcv2

import "context"

// JSONRPCHandler is an interface that defines a handler for JSON-RPC requests.
type JSONRPCHandler interface {
	// ServeJSONRPC serves a single JSON-RPC request and returns the response.
	ServeJSONRPC(ctx context.Context, request Request) (Response, error)
}

// JSONRPCBatchHandler is an interface that defines a handler for batches of
// of JSON-RPC Requests.
type JSONRPCBatchHandler interface {
	// ServeJSONRPCBatch serves a batch of JSON-RPC requests and returns the
	// responses.
	ServeJSONRPCBatch(ctx context.Context, requests []Request) ([]Response, error)
}

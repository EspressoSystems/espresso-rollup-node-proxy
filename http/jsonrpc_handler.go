package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"proxy/jsonrpcv2"

	"github.com/ethereum/go-ethereum/log"
)

// JSONRPCHandler is an interface that defines a handler for JSON-RPC requests.
type JSONRPCHandler interface {
	// ServeJSONRPC serves a single JSON-RPC request and returns the response.
	ServeJSONRPC(ctx context.Context, request jsonrpcv2.Request) (jsonrpcv2.Response, error)
}

// JSONRPCBatchHandler is an interface that defines a handler for batches of
// of JSON-RPC Requests.
type JSONRPCBatchHandler interface {
	// ServeJSONRPCBatch serves a batch of JSON-RPC requests and returns the
	// responses.
	ServeJSONRPCBatch(ctx context.Context, requests []jsonrpcv2.Request) ([]jsonrpcv2.Response, error)
}

// httpJSONRPCBridge is a bridge that allows us to serve JSON-RPC requests over
// HTTP utilizing the request Body.
type httpJSONRPCBridge struct {
	handler JSONRPCHandler
	logger  log.Logger
}

// ServeJSONRPC implements JSONRPCHandler
//
// This is accomplished by delegating to the underlying handler.
func (m *httpJSONRPCBridge) ServeJSONRPC(ctx context.Context, request jsonrpcv2.Request) (jsonrpcv2.Response, error) {
	return m.handler.ServeJSONRPC(ctx, request)
}

// ServeJSONRPCBatch implements JSONRPCBatchHandler
//
// This is accomplished by checking to see if our handler can natively
// support batch requests.  If able, it will delegate directly to the
// handler.
//
// If not, it will perform a fallback that will iterate through each of the
// requests, one-by-one, and passing them to the handler.
func (m *httpJSONRPCBridge) ServeJSONRPCBatch(ctx context.Context, requests []jsonrpcv2.Request) ([]jsonrpcv2.Response, error) {
	// Is our handler, also a batch handler?
	if cast, castOK := m.handler.(JSONRPCBatchHandler); castOK {
		// Awesome, we have native support for batch processing
		return cast.ServeJSONRPCBatch(ctx, requests)
	}

	return m.fallbackServeJSONRPCBatch(ctx, requests)
}

// fallbackServeJSONRPCBatch is a fallback implementation of batch processing
// that will delegate each individual request to the underlying handler one
// at a time.
//
// TODO: This is linear cost, we may be able to perform these requests in
// parallel.
func (m *httpJSONRPCBridge) fallbackServeJSONRPCBatch(ctx context.Context, requests []jsonrpcv2.Request) ([]jsonrpcv2.Response, error) {
	// Awe shucks, it doesn't natively support multiple batch requests at once.
	// No Matter, we'll process them one at a time.

	var responses []jsonrpcv2.Response
	for _, r := range requests {
		response, err := m.ServeJSONRPC(ctx, r)
		if err != nil {
			// We had a non-specifc error for this request.
			// We'll queue up the response as an error, and keep processing the
			// rest.
			responses = append(responses,
				jsonrpcv2.CreateGeneralErrorResponse(
					r.ID,
					jsonrpcv2.CodeInternalError,
					fmt.Sprintf("encountered an error processing request: %s", err),
				),
			)
			continue
		}

		// Otherwise, we'll add it to the list of responses to return.
		responses = append(responses, response)
	}

	return responses, nil
}

// serveHTTPJSONRPCSingleRequest serves a single JSON-RPC request over HTTP.
func (m *httpJSONRPCBridge) serveHTTPJSONRPCSingleRequest(ctx context.Context, w http.ResponseWriter, body json.RawMessage) {
	var request jsonrpcv2.Request
	if err := json.Unmarshal(body, &request); err != nil {
		WriteJSONRPCError(w, nil, jsonrpcv2.CodeParseError, fmt.Sprintf("unable to parse rpc request: %s", err))
		return
	}

	response, err := m.handler.ServeJSONRPC(ctx, request)
	if err != nil {
		WriteJSONRPCError(w, request.ID, jsonrpcv2.CodeInternalError, fmt.Sprintf("internal error: %s", err))
		return
	}

	encoder := json.NewEncoder(w)
	w.Header().Set("Content-Type", "application/json")
	if err := encoder.Encode(response); err != nil {
		// Fallback on transport error
		http.Error(w, "failed to send response", http.StatusInternalServerError)
		return
	}
}

// serveHTTPJSONRPCBatchRequest serves a batch of JSON-RPC requests over HTTP.
func (m *httpJSONRPCBridge) serveHTTPJSONRPCBatchRequest(ctx context.Context, w http.ResponseWriter, body json.RawMessage) {
	var requests []jsonrpcv2.Request
	if err := json.Unmarshal(body, &requests); err != nil {
		WriteJSONRPCError(w, nil, jsonrpcv2.CodeParseError, fmt.Sprintf("unable to parse rpc request: %s", err))
		return
	}

	responses, err := m.ServeJSONRPCBatch(ctx, requests)
	if err != nil {
		WriteJSONRPCError(w, nil, jsonrpcv2.CodeInternalError, fmt.Sprintf("failed to process batch requests: %s", err))
		return
	}

	encoder := json.NewEncoder(w)
	w.Header().Set("Content-Type", "application/json")
	if err := encoder.Encode(responses); err != nil {
		// Fallback on transport
		http.Error(w, "failed to send response", http.StatusInternalServerError)
		return
	}
}

// KeyContextHTTPHeader is a context key for the HTTP Headers of the incoming
// request to store the raw HTTP Headers passed in
type KeyContextHTTPHeader struct{}

// ServerHTTP implements http.Handler
//
// This method bridges the boundary between HTTP Transport and the JSON-RPC
// layer.
func (m *httpJSONRPCBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Write the Raw Headers into the Request
	ctx = context.WithValue(ctx, KeyContextHTTPHeader{}, r.Header)

	decoder := json.NewDecoder(r.Body)
	var body json.RawMessage
	if err := decoder.Decode(&body); err != nil && err != io.EOF {
		// The Contents could not be determined
		WriteJSONRPCError(w, nil, jsonrpcv2.CodeParseError, fmt.Sprintf("unable to parse json rpc request: %s", err))
		return
	}

	// This should never happen, but we'll guard against it anyway.
	if len(body) <= 0 {
		WriteJSONRPCError(w, nil, jsonrpcv2.CodeParseError, "empty request body")
		return
	}

	// Inspect the first character of the body to determine if this is a batch
	// request, or a single request.

	switch body[0] {
	default:
		// Invalid request
		WriteJSONRPCError(w, nil, jsonrpcv2.CodeParseError, "invalid json rpc request")

	case '{':
		// Single Request
		m.serveHTTPJSONRPCSingleRequest(ctx, w, body)

	case '[':
		// Batch Request
		m.serveHTTPJSONRPCBatchRequest(ctx, w, body)
	}
}

// RecoveryMiddleware is a middleware that cleanly handles panics that may
// occur.
func JSONRPCBridge(logger log.Logger, handler JSONRPCHandler) http.Handler {
	return &httpJSONRPCBridge{handler: handler, logger: logger}
}

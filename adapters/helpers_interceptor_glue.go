package adapters

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"
)

// isJSONSpace is a helper function that checks if the given rune is a valid
// space according to the JSON specification.  This includes the following
// characters:
// - ' '
// - '\n'
// - '\r'
// - '\t'
//
// See RFC 7159, Section 2 (JSON Grammar) for specifics
// https://www.rfc-editor.org/rfc/rfc7159.txt
// https://datatracker.ietf.org/doc/html/rfc7159#section-2
//
// See Golang's implementation for valid JSON Space characters:
// https://cs.opensource.google/go/go/+/refs/tags/go1.25.1:src/encoding/json/scanner.go;l=201-203;drc=0e17905793cb5e0acc323a0cdf3733199d93976a
func isJSONSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == '\n'
}

// Interceptor is an interface that defines the methods for intercepting
// JSON-RPC.
type Interceptor interface {
	// InterceptRequest takes a JSON-RPC request, and attempts to perform an
	// intercept on it.  This means that the request may be rewritten and
	// modified to be different.
	InterceptRequest(jsonrpcv2.Request) (jsonrpcv2.Request, error)

	// InterceptBatchRequests takes in a batch of JSON-RPC requests, and
	// attempts to perform an intercept on each request in the batch.
	InterceptBatchRequests([]jsonrpcv2.Request) ([]jsonrpcv2.Request, error)
}

// WrapErr is a helper function that wraps the given error with the provided
// message.
//
// If the passed error is nil, then this function returns nil.
func WrapErr(message string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s: %w", message, err)
}

// requestInterceptorGlue performs the interception of the given payload using
// the provided function.  It decodes the request, performs the intercept, and
// then re-encodes the request back to JSON.
//
// This enables the caller to perform the intercept without having to manually
// tie the decoding to a specific function call.
func requestInterceptorGlue[R any](payload []byte, intercept func(R) (R, error)) ([]byte, error) {
	var request R
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, WrapErr("failed to decode json rpc request", jsonrpcv2.Error{
			Code:    jsonrpcv2.CodeParseError,
			Message: fmt.Sprintf("failed to decode json rpc request: %s", err),
		})
	}

	request, err := intercept(request)
	if err != nil {
		return nil, WrapErr("failed to intercept json rpc request", err)
	}

	marshaledBytes, err := json.Marshal(request)
	return marshaledBytes, WrapErr("failed to encode json rpc request", err)
}

// PerformRequestIntercept is a helper function that performs the given
// intercept on the payload.
//
// This function is reseponsible for decoding the data and routing the
// request to the given interceptor based on the detected data type.
//
// This will return [ErrNoMessage] or [ErrUnrecognizedRequest] if the
// payload is not in the expected format for a JSON RPC Request, or Batch
// JSON RPC Request.
func PerformRequestIntercept(payload []byte, interceptor Interceptor) ([]byte, error) {
	payload = bytes.TrimLeftFunc(payload, isJSONSpace)

	if len(payload) <= 0 {
		// We have a problem, we expected a message but got nothing.
		return nil, jsonrpcv2.Error{
			Code:    jsonrpcv2.CodeParseError,
			Message: "received empty payload, with no content",
		}
	}

	switch payload[0] {
	default:
		return nil, jsonrpcv2.Error{
			Code:    jsonrpcv2.CodeParseError,
			Message: "unexpected format for json-rpc request",
		}

	case '{':
		// Single request
		return requestInterceptorGlue(payload, interceptor.InterceptRequest)

	case '[':
		// Batch Request
		return requestInterceptorGlue(payload, interceptor.InterceptBatchRequests)
	}
}

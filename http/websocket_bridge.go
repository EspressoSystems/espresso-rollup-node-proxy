package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/textproto"
	"time"

	"proxy/jsonrpcv2"

	"github.com/ethereum/go-ethereum/log"
	"github.com/gorilla/websocket"
)

// processingBranch is an enum that indicates the branch of processing that
// indicates the logic that should be considered when in the middle of
// websocket processing.
type processingBranch int

const (
	// keepGoing means to go to continue processing
	keepGoing processingBranch = iota

	// severed indicates that the connection was terminated and the processing
	// loop should be stopped
	severed
)

// websocketJSONRPCHTTPBridge is a bridge that allows us to serve JSON-RPC
// requests over
type websocketJSONRPCHTTPBridge struct {
	logger   log.Logger
	upgrader websocket.Upgrader
	handler  JSONRPCHandler
}

// Compile-time interface adherence assertions
var (
	_ http.Handler        = (*websocketJSONRPCHTTPBridge)(nil)
	_ JSONRPCHandler      = (*websocketJSONRPCHTTPBridge)(nil)
	_ JSONRPCBatchHandler = (*websocketJSONRPCHTTPBridge)(nil)
)

// ServeJSONRPC implements JSONRPCHandler
//
// This is accomplished by delegating to the underlying handler.
func (m *websocketJSONRPCHTTPBridge) ServeJSONRPC(ctx context.Context, request jsonrpcv2.Request) (jsonrpcv2.Response, error) {
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
func (m *websocketJSONRPCHTTPBridge) ServeJSONRPCBatch(ctx context.Context, requests []jsonrpcv2.Request) ([]jsonrpcv2.Response, error) {
	// Is our handler, also a batch handler?
	if cast, castOK := m.handler.(JSONRPCBatchHandler); castOK {
		// Awesome, we have native support for batch processing
		return cast.ServeJSONRPCBatch(ctx, requests)
	}

	return fallbackServeJSONRPCBatch(ctx, m, requests)
}

// severWebSocketConnection is a helper function that will attempt to close the
// provided websocket ocnnection, and automatically logs any error if that
// close should fail.
func severWebSocketConnection(logger log.Logger, conn *websocket.Conn) {
	// Sever the connection
	if err := conn.Close(); err != nil {
		logger.Warn("failed to close websocket connection", "error", err)
	}
}

// sendWebSocketResponse is a helper function that will attempt to write the
// provided response as a JSON encoded value.  Should the write fail, the
// error will be logged, and the connection will be severed automatically. The
// error is returned as a signal that this has occurred.
func sendWebSocketResponse(conn *websocket.Conn, logger log.Logger, response any) processingBranch {
	if err := conn.WriteJSON(response); err != nil {
		// Alright, we couldn't write a response, now we'll need to close
		// the connection.
		logger.Debug("failed to write response to websocket connection, severing connection", "error", err)
		severWebSocketConnection(logger, conn)
		return severed
	}

	return keepGoing
}

// genericWebsocketJSONRPCRequestHandler is an interface that defines a handler
// for JSON-RPC to specific request Bridges. This  exists soley as a helper
// interface to help bridge the gap between singular JSON-RPC requests and
// batches of multiple JSON-RPC requests.
type genericWebsocketJSONRPCRequestHandler interface {
	// bridgeWebSocketToJSONRPCRequest helps to bridge the gap between the
	// specific handlers of JSON-RPC request types to specific handlers.
	bridgeWebSocketToJSONRPCRequest(ctx context.Context, logger log.Logger, conn *websocket.Conn, payload []byte) processingBranch
}

// Compile-time interface adherence assertion
var _ genericWebsocketJSONRPCRequestHandler = (*websocketToJSONRPCRequestHandler[jsonrpcv2.Request, jsonrpcv2.Response])(nil)

// websocketToJSONRPCRequestHandler is a generic handler that can be used to
// handle the routing of requests to specific intended handlers.
type websocketToJSONRPCRequestHandler[T any, R any] struct {
	handler func(ctx context.Context, request T) (R, error)
}

// bridgeWebSocketToJSONRPCRequest implements genericWebsocketJSONRPCRequestHandler
func (h *websocketToJSONRPCRequestHandler[T, R]) bridgeWebSocketToJSONRPCRequest(
	ctx context.Context,
	logger log.Logger,
	conn *websocket.Conn,
	payload []byte,
) processingBranch {
	var request T
	dec := json.NewDecoder(bytes.NewBuffer(payload))
	if err := dec.Decode(&request); err != nil {
		response := jsonrpcv2.CreateGeneralErrorResponse(nil, jsonrpcv2.CodeParseError, fmt.Sprintf("unable to parse request: %s", err))
		return sendWebSocketResponse(conn, logger, response)
	}

	response, err := h.handler(ctx, request)
	if err != nil {
		response := jsonrpcv2.CreateGeneralErrorResponse(nil, jsonrpcv2.CodeInternalError, fmt.Sprintf("failed to process json rpc request: %s", err))
		return sendWebSocketResponse(conn, logger, response)
	}

	return sendWebSocketResponse(conn, logger, response)
}

// processWebsocketMessage is a helper function that will attempt to process the
// provided message, and route it to the appropriate handler.
func (h *websocketJSONRPCHTTPBridge) processWebsocketMessage(
	ctx context.Context,
	conn *websocket.Conn,
	payload []byte,
) (processing processingBranch) {
	// Attempt to parse the request
	var handler genericWebsocketJSONRPCRequestHandler
	switch payload[0] {
	default:
		// They sent us something we do not recognize. They aren't speaking
		// our language, so we will sever the connection.
		h.logger.Debug("unexpected JSON received by the server, severing connection")
		severWebSocketConnection(h.logger, conn)
		return severed

	case '[':
		handler = &websocketToJSONRPCRequestHandler[[]jsonrpcv2.Request, []jsonrpcv2.Response]{handler: h.ServeJSONRPCBatch}

	case '{':
		handler = &websocketToJSONRPCRequestHandler[jsonrpcv2.Request, jsonrpcv2.Response]{handler: h.ServeJSONRPC}
	}

	if handler.bridgeWebSocketToJSONRPCRequest(ctx, h.logger, conn, payload) == severed {
		return severed
	}

	return keepGoing
}

// isWebSocketHeader is a helper function utilized to help identify the
// Header keys that are specific to WebSockets.
//
// List comes from the following documentation:
// https://developer.mozilla.org/en-US/docs/Web/API/WebSockets_API
func isWebSocketHeader(headerKey string) bool {
	header := textproto.CanonicalMIMEHeaderKey(headerKey)

	// These are the relevant WebSocket Headers that we
	switch header {
	default:
		return false

	case "Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Protocol":
		return true
	}
}

// cloneHeadersIgnoringWebSocketHeaders is a helper function that will
// clone the provided http.Headers, filtering out all Websocket specific
// headers.
func cloneHeadersIgnoringWebSocketHeaders(headers http.Header) http.Header {
	next := http.Header{}
	for header, values := range headers {
		if isWebSocketHeader(header) {
			// skip the upgrade header
			continue
		}

		for _, value := range values {
			next.Add(header, value)
		}
	}

	return next
}

// ServeHTTP implements http.Handler
//
// This handler will convert the request to a WebSocket connection, and will
// start processing request messages from the websocket, returning responses
// where appropriate.
func (h *websocketJSONRPCHTTPBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ctx = context.WithValue(ctx, KeyContextHTTPHeader{}, cloneHeadersIgnoringWebSocketHeaders(r.Header))

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// This error is likely due to the request not actually being a valid
		// WebSocket request
		h.logger.Debug("failed to upgrade connection to websocket", "error", err)
		return
	}

	// We will now process all requests in a loop
	for {
		var raw json.RawMessage
		if err := conn.ReadJSON(&raw); err != nil {
			// They sent us something we do not recognize. They aren't speaking
			// our language, so we will sever the connection.
			h.logger.Debug("invalid JSON was received by the server, severing connection")
			severWebSocketConnection(h.logger, conn)
			return
		}

		if h.processWebsocketMessage(ctx, conn, raw) == severed {
			return
		}
	}
}

// WebSocketJSONRPCHTTPBridge is a helper function that creates a new WebSocket JSON-RPC
// Bridge http.Handler that will first translate the HTTP transport into a
// Websocket Transport, then facilitate the processing of the JSON RPC
// protocol on top of that websocket Transport.
func WebSocketJSONRPCHTTPBridge(logger log.Logger, handler JSONRPCHandler) http.Handler {
	return &websocketJSONRPCHTTPBridge{
		logger:  logger,
		handler: handler,
		upgrader: websocket.Upgrader{
			HandshakeTimeout: time.Second * 15,
		},
	}
}

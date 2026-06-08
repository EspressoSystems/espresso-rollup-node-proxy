package adapters

import (
	"context"
	"errors"
	"net/http"

	"proxy/jsonrpcv2"
	"proxy/websocket"
)

// webSocketJSONRPCDownstreamIntercept is a [websocket.Conn] that intercepts
// [websocket.Reader.Read] requests, decoding the relevant JSON-RPC requesets,
// and re-writing them as necessary.
type webSocketJSONRPCDownstreamIntercept struct {
	websocket.Conn
	interceptor Interceptor
}

// Compile-time assertion to ensure that [webSocketJSONRPCDownstreamIntercept]
// implements the websocket.Conn interface.
var _ websocket.Conn = (*webSocketJSONRPCDownstreamIntercept)(nil)

// NewWebsocketJSONRPCDownstreamIntercept creates a new [websocket.Conn] that
// ultimately forwards all requests to the given [websocket.Conn]. All
// [websocket.Conn.Read] messsages are intercepted and potentially rewritten
// by the given [Interceptor] before being returned to the caller.
func NewWebsocketJSONRPCDownstreamIntercept(conn websocket.Conn, interceptor Interceptor) websocket.Conn {
	return &webSocketJSONRPCDownstreamIntercept{
		Conn:        conn,
		interceptor: interceptor,
	}
}

// interceptorUpgrader is an implementation of [websocket.Upgrader] that
// is a Middleware for intercepting JSON-RPC requests on a WebSocket
// connection.
type interceptorUpgrader struct {
	interceptor Interceptor
	upgrader    websocket.Upgrader
}

// WebSocketUpgraderInterceptor is a function for quickly wrapping a
// [websocket.Upgrader] whose underlying [websocket.Conn] will intercept
// incoming websocket messages, and rewriting JSON RPC Requests.
func WebSocketUpgraderInterceptor(
	upgrader websocket.Upgrader,
	interceptor Interceptor,
) websocket.Upgrader {
	return &interceptorUpgrader{
		interceptor: interceptor,
		upgrader:    upgrader,
	}
}

// Upgrade implements [websocket.Upgrader].
func (u *interceptorUpgrader) Upgrade(w http.ResponseWriter, r *http.Request, options ...websocket.UpgradeOption) (websocket.Conn, error) {
	conn, err := u.upgrader.Upgrade(w, r, options...)
	if err != nil {
		return conn, err
	}

	return NewWebsocketJSONRPCDownstreamIntercept(conn, u.interceptor), nil
}

// ReadMessage implements [websocket.Conn].
//
// This method reads the message from the given connection, performs an
// intercept on the request, and then returns the intercepted data back
// to the handler.
func (i *webSocketJSONRPCDownstreamIntercept) Read(ctx context.Context) (messageType websocket.MessageType, message []byte, err error) {
	messageType, message, err = i.Conn.Read(ctx)
	if err != nil {
		return messageType, message, err
	}

	message, err = PerformRequestIntercept(message, i.interceptor)
	if err != nil {
		var jsonRPCError jsonrpcv2.Error
		if errors.As(err, &jsonRPCError) {
			WriteJSONRPCResponseToWebSocket(i.Conn, jsonrpcv2.Response{
				Error: &jsonRPCError,
			})

			// Make the error explicitly returned
			return messageType, message, err
		}

		WriteJSONRPCErrorToWebSocket(i.Conn, nil, jsonrpcv2.CodeInternalError, "failed to intercept request")
		return messageType, message, WrapErr("failed to intercept JSON-RPC request", err)
	}

	return messageType, message, nil
}

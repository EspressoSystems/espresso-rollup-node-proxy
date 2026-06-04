package adapters

import (
	"context"
	"errors"

	"proxy/jsonrpcv2"
	"proxy/websocket"
)

type webSocketJSONRPCDownstreamIntercept struct {
	websocket.Conn
	interceptor Interceptor
}

// Compile-time assertion to ensure that [webSocketJSONRPCDownstreamIntercept]
// implements the websocket.Conn interface.
var _ websocket.Conn = (*webSocketJSONRPCDownstreamIntercept)(nil)

// NewWebsocketJSONRPCDownstreamIntercept creates a new [websocket.Conn] that
// ultimately forwards all requests to the given [websocket.Conn]. All
// [websocket.Conn.Read] messsages are intercepted and potentially transformed
// by the given [Interceptor] before being returned to the caller.
func NewWebsocketJSONRPCDownstreamIntercept(conn websocket.Conn, interceptor Interceptor) websocket.Conn {
	return &webSocketJSONRPCDownstreamIntercept{
		Conn:        conn,
		interceptor: interceptor,
	}
}

// NewWebsocketJSONRPCDownstreamInterceptMiddleware creates a new
// [websocket.Middleware] that will intercept all [websocket.Conn.Read]
// requests with the given [Interceptor].
func NewWebsocketJSONRPCDownstreamInterceptMiddleware(interceptor Interceptor) websocket.Middleware {
	return func(conn websocket.Conn) websocket.Conn {
		return NewWebsocketJSONRPCDownstreamIntercept(conn, interceptor)
	}
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
			return
		}

		WriteJSONRPCErrorToWebSocket(i.Conn, nil, jsonrpcv2.CodeInternalError, "failed to intercept request")
		return messageType, message, WrapErr("failed to intercept JSON-RPC request", err)
	}

	return messageType, message, nil
}

package http

import (
	"net/http"
	"net/url"
	"time"

	proxywebsocket "proxy/websocket"

	"github.com/ethereum/go-ethereum/log"
	"github.com/gorilla/websocket"
)

// websocketJSONRPCHTTPBridge is a bridge that allows us to serve JSON-RPC
// requests over
type websocketJSONRPCHTTPBridge struct {
	logger              log.Logger
	upgrader            websocket.Upgrader
	dialer              websocket.Dialer
	upstreamURL         *url.URL
	clientMiddlewares   []proxywebsocket.Middleware
	upstreamMiddlewares []proxywebsocket.Middleware
}

// Compile-time interface adherence assertions
var (
	_ http.Handler = (*websocketJSONRPCHTTPBridge)(nil)
)

// ServeHTTP implements http.Handler
//
// This handler will convert the request to a WebSocket connection, and will
// start processing request messages from the websocket, returning responses
// where appropriate.
func (h *websocketJSONRPCHTTPBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// This error is likely due to the request not actually being a valid
		// WebSocket request
		h.logger.Debug("failed to upgrade connection to websocket", "error", err)
		return
	}

	defer func(conn *websocket.Conn) {
		if err := conn.Close(); err != nil {
			h.logger.Warn("failed to close websocket connection after upstream dial failure", "error", err)
		}
	}(conn)

	// Prune all WebSocket Headers specific to our generated request.
	proxyHeaders := proxywebsocket.CloneRequestHeadersForProxy(r.Header)

	// Establish an upstream connection to the WebSocket server.
	upstream, _, err := h.dialer.Dial(
		h.upstreamURL.String(),
		proxyHeaders,
	)
	// We failed to dial to the upstream
	// Close the connection
	if err != nil {
		h.logger.Warn("failed to dial upstream websocket server", "error", err)
		return
	}

	defer func(conn *websocket.Conn) {
		if err := conn.Close(); err != nil {
			h.logger.Warn("failed to close upstream websocket connection", "error", err)
		}
	}(upstream)

	clientConn := proxywebsocket.AdaptGorilla(conn)
	for _, m := range h.clientMiddlewares {
		clientConn = m(clientConn)
	}
	upstreamConn := proxywebsocket.AdaptGorilla(upstream)
	for _, m := range h.upstreamMiddlewares {
		upstreamConn = m(upstreamConn)
	}

	if err := proxywebsocket.Bridge(ctx, upstreamConn, clientConn); err != nil {
		h.logger.Info("bridged terminated with error", "err", err)
	}
}

// WebSocketJSONRPCHTTPBridge is a helper function that creates a new WebSocket JSON-RPC
// Bridge http.Handler that will first translate the HTTP transport into a
// Websocket Transport, then facilitate the processing of the JSON RPC
// protocol on top of that websocket Transport.
func WebSocketJSONRPCHTTPBridge(logger log.Logger, upstreamURL *url.URL, clientMiddlewares ...proxywebsocket.Middleware) http.Handler {
	return &websocketJSONRPCHTTPBridge{
		logger: logger,
		upgrader: websocket.Upgrader{
			HandshakeTimeout: time.Second * 15,
		},
		upstreamURL:       upstreamURL,
		clientMiddlewares: clientMiddlewares,
	}
}

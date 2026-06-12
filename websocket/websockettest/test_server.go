package websockettest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket"
)

// WebSocketHandler is an interface that represents a handler for WebSocket
// upgraded requests.
type WebSocketHandler interface {
	// ServeWebSocket is called with the results of performing a call on
	// [websocket.Upgrader.Upgrade]. The error may be populated, and if it
	// is, will mean that the provided [websocket.Conn] is not valid,
	// and should not be used.
	ServeWebSocket(conn websocket.Conn, err error)
}

// WebSocketHandlerFunc is a helper type that allows us to use a simple
// function as a [WebSocketHandler].
type WebSocketHandlerFunc func(websocket.Conn, error)

// Compile-time interface adherence assertion.
var _ WebSocketHandler = WebSocketHandlerFunc(nil)

// ServeWebSocket implements [WebSocketHandler] by calling the underlying
// funtion.
func (f WebSocketHandlerFunc) ServeWebSocket(conn websocket.Conn, err error) {
	f(conn, err)
}

// Server represents a test WebSocket server, which can be used to test
// WebSocket clients.
type Server struct {
	*httptest.Server
	Handler  WebSocketHandler
	Upgrader websocket.Upgrader
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := s.Upgrader.Upgrade(w, r)
	s.Handler.ServeWebSocket(conn, err)
}

// WSURL returns the WebSocket URL for the test server, which can be used to
// connect to the server using a WebSocket client.
func (s *Server) WSURL() *url.URL {
	return &url.URL{
		Scheme: "ws",
		Host:   s.Listener.Addr().String(),
	}
}

// Connect connects to the test server using the provided dialer, and
// returns thew new WebSocket connection.
func (s *Server) Connect(ctx context.Context, dialer websocket.Dialer) (websocket.Conn, *http.Response, error) {
	return dialer.Dial(ctx, s.WSURL().String())
}

// NewServer creates a new test WebSocket server with the given upgrader
// and handler.
//
// The Caller **SHOULD** call the [Server.Close] method when finished, to shut
// down the server.
func NewServer(upgrader websocket.Upgrader, handler WebSocketHandler) *Server {
	server := NewUnstartedServer(upgrader, handler)
	server.Start()
	return server
}

// NewUnstartedServer creates a new test WebSocket server with the given
// [websocket.Upgrader] and [WebSocketHandler], but does not start the server.
func NewUnstartedServer(upgrader websocket.Upgrader, handler WebSocketHandler) *Server {
	server := &Server{
		Upgrader: upgrader,
		Handler:  handler,
	}

	server.Server = httptest.NewUnstartedServer(server)
	return server
}

// TestServer represents a test WebSocket server, which can be used to test
// specific behavior with WebSocket servers.
type TestServer interface {
	// Connect connects to the test server using the provided dialer.
	Connect(ctx context.Context, dialer websocket.Dialer) (websocket.Conn, *http.Response, error)

	Close()
}

// TestServerCreator creates a new test server with the given
// [websocket.Upgrader] and [WebSocketHandler].
func TestServerCreator(upgrader websocket.Upgrader, handler WebSocketHandler) TestServer {
	return NewServer(upgrader, handler)
}

// ServerCreator represents a function that can be used to create a new test
type ServerCreator func(upgrader websocket.Upgrader, handler WebSocketHandler) TestServer

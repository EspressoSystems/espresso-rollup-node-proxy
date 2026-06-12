package websocketutil

import (
	"context"
	"fmt"
	"net/http"

	"proxy/websocket"
	"proxy/websocket/websockettest"
)

// ReverseProxyTestServer is a utility type that represents a test server
// that consists of an Upstream, and a Middle server that acts as a reverse
// proxy to the Upstream server.
type ReverseProxyTestServer struct {
	Upstream *websockettest.Server
	Middle   *websockettest.Server
}

// proxyConsumeAllRequests is a [websockettest.WebSocketHandler] that consumes
// all messages from the connection, and does nothing with them.
type proxyConsumeAllRequests struct{}

// ServeWebSocket implements [websockettest.WebSocketHandler] by consuming
// all messages.
func (*proxyConsumeAllRequests) ServeWebSocket(conn websocket.Conn, err error) {
	if err != nil {
		panic(fmt.Sprintf("encountered error upgrading connection: %s", err))
	}

	_ = ReadAllMessages(context.Background(), conn)
}

// NewTestServer creates a new test server that consists of an upstream server,
// and a middle server that acts as a reverse proxy to the upstream server.
func NewTestServer(dialer websocket.Dialer, upgrader websocket.Upgrader, handler websockettest.WebSocketHandler) *ReverseProxyTestServer {
	server1 := websockettest.NewServer(upgrader, handler)
	reverseProxy := NewReverseProxy(server1.WSURL(), dialer, upgrader)
	server2 := websockettest.NewServer(reverseProxy, new(proxyConsumeAllRequests))

	return &ReverseProxyTestServer{
		Upstream: server1,
		Middle:   server2,
	}
}

func (s *ReverseProxyTestServer) Connect(ctx context.Context, dialer websocket.Dialer) (websocket.Conn, *http.Response, error) {
	return s.Middle.Connect(ctx, dialer)
}

func (s *ReverseProxyTestServer) Close() {
	s.Middle.Close()
	s.Upstream.Close()
}

func TestServerCreator(dialer websocket.Dialer) websockettest.ServerCreator {
	return func(upgrader websocket.Upgrader, handler websockettest.WebSocketHandler) websockettest.TestServer {
		return NewTestServer(dialer, upgrader, handler)
	}
}

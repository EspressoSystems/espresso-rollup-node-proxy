package websocket_test

import (
	"net/http"
	"proxy/websocket"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCloneRequestHeadersForProxy tests that the CloneRequestHeadersForProxy
// function that ensures that we omit headers for proxy requests that impose
// restrictions on the WebSocket connection establishment efforts.
func TestCloneRequestHeadersForProxy(t *testing.T) {
	require := require.New(t)
	header := http.Header{
		websocket.HeaderOrigin:                 []string{"example.com"},
		websocket.HeaderSecWebSocketKey:        []string{"dGhlIHNhbXBsZSBub25jZQ=="},
		websocket.HeaderSecWebSocketVersion:    []string{"13"},
		websocket.HeaderSecWebSocketProtocol:   []string{"chat, superchat"},
		websocket.HeaderSecWebSocketExtensions: []string{"permessage-deflate; client_max_window_bits"},
		websocket.HeaderUpgrade:                []string{"websocket"},

		"Test": []string{"value"},
	}

	cloned := websocket.CloneRequestHeadersForProxy(header)

	require.Equal(
		http.Header{
			"Test": []string{"value"},
		}, cloned,
	)
}

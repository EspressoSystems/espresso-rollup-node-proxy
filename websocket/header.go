package websocket

import (
	"net/http"
	"net/textproto"
)

// These are headers that apply to general http requests that would need to
// be removed in a proxy upstream.
const (
	HeaderHost   = "Host"
	HeaderOrigin = "Origin"
)

// These are headers that are specific to WebSocket connection upgrade
// negotioation.  These headers are defined as part of the WebSocket protocol.
// Reference:
// https://developer.mozilla.org/en-US/docs/Web/API/WebSockets_API/Writing_WebSocket_servers#the_websocket_handshake
const (
	HeaderUpgrade                = "Upgrade"
	HeaderSecWebSocketAccept     = "Sec-Websocket-Accept"
	HeaderSecWebSocketKey        = "Sec-Websocket-Key"
	HeaderSecWebSocketVersion    = "Sec-Websocket-Version"
	HeaderSecWebSocketProtocol   = "Sec-Websocket-Protocol"
	HeaderSecWebSocketExtensions = "Sec-Websocket-Extensions"
)

// IsWebsocketHeader returns true if the provided header is a
// WebSocket-specific header.
//
// This only returns true for headers that have "Sec-Websocket" prefix.
func IsWebsocketHeader(h string) bool {
	switch textproto.CanonicalMIMEHeaderKey(h) {
	default:
		return false

	case HeaderSecWebSocketAccept, HeaderSecWebSocketKey,
		HeaderSecWebSocketVersion, HeaderSecWebSocketProtocol,
		HeaderSecWebSocketExtensions:
		return true

	}
}

// ShouldPruneForProxy returns true if the provided header should be pruned
// for request forwarding.
func ShouldPruneForProxy(h string) bool {
	switch textproto.CanonicalMIMEHeaderKey(h) {
	default:
		return false

	case HeaderUpgrade, HeaderHost, HeaderOrigin, HeaderSecWebSocketKey,
		HeaderSecWebSocketVersion, HeaderSecWebSocketExtensions:
		return true
	}
}

// CloneRequestHeadersForProxy clones the provided headers, omitting any
// headers that should not be generated or utilized in the request.
func CloneRequestHeadersForProxy(h http.Header) http.Header {
	cloned := http.Header{}

	for header, values := range h {
		if ShouldPruneForProxy(header) {
			continue
		}

		for _, value := range values {
			cloned.Add(header, value)
		}
	}

	return cloned
}

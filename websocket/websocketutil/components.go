package websocketutil

import "proxy/websocket"

// components is a utility struct that implements all of the main
// websocket interfaces by embedding them.
type components struct {
	websocket.Reader
	websocket.Writer
	websocket.Closer
	websocket.ErrorChecker
	websocket.SubProtoRetriever
}

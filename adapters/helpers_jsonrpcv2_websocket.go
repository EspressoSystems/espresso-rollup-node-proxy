package adapters

import (
	"context"
	"encoding/json"

	"proxy/jsonrpcv2"
	"proxy/websocket"

	"github.com/ethereum/go-ethereum/log"
)

// WriteJSONRPCResponseToWebSocket is a helper function that writes the given JSON-RPC
// response
func WriteJSONRPCResponseToWebSocket(conn websocket.Conn, response jsonrpcv2.Response) {
	data, err := json.Marshal(response)
	if err != nil {
		log.Error("failed to encode JSON-RPC response", "error", err)
		return
	}

	if err := conn.Write(context.Background(), websocket.MessageTypeText, data); err != nil {
		log.Error("failed to send JSON-RPC message to websocket", "error", err)

		// We're expecting the Server to have successuflly sent a response in this
		// case.  Yet our encoding failed.
		// Fallback to a Transport error

		if err := conn.Close(websocket.StatusProtocolError, "failed to send JSON-RPC response"); err != nil {
			log.Error("failed to close websocket", "error", err)
		}

	}

	// Everything was send without issue
}

// WriteJSONRPCErrorToWebSocket is a convenience function that creates a
// JSON-RPC error response with the given id, code, and message, and writes it
// to the [websocket.Conn].
func WriteJSONRPCErrorToWebSocket(conn websocket.Conn, id jsonrpcv2.ID, code int, message string) {
	WriteJSONRPCResponseToWebSocket(conn, jsonrpcv2.CreateGeneralErrorResponse(id, code, message))
}

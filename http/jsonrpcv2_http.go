package http

import (
	"encoding/json"
	"net/http"

	"proxy/jsonrpcv2"

	"github.com/ethereum/go-ethereum/log"
)

// WriteJSONRPCResponse is a helper function that writes the given JSON-RPC
// response
func WriteJSONRPCResponse(w http.ResponseWriter, response jsonrpcv2.Response) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(response); err != nil {
		log.Error("failed to encode JSON-RPC response", "error", err)

		// We're expecting the Server to have successuflly sent a response in this
		// case.  Yet our encoding failed.
		// Fallback to a Transport error
		http.Error(w, "failed to send response", http.StatusInternalServerError)
	}

	// Everything was send without issue
}

// WriteJSONRPCError is a convenience function that creates a JSON-RPC error
// response with the given id, code, and message, and writes it to the
// http.ResponseWriter using WriteJSONRPCResponse.
func WriteJSONRPCError(w http.ResponseWriter, id jsonrpcv2.ID, code int, message string) {
	WriteJSONRPCResponse(w, jsonrpcv2.CreateGeneralErrorResponse(id, code, message))
}

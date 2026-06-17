package adapters

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"

	"github.com/ethereum/go-ethereum/log"
)

// WriteJSONRPCResponseToHTTPResponseWriter is a helper function that writes
// the given JSON-RPC response
func WriteJSONRPCResponseToHTTPResponseWriter(w http.ResponseWriter, response jsonrpcv2.Response) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(response); err != nil {
		log.Error("failed to encode JSON-RPC response", "error", err)
		http.Error(w, "failed to send response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buf.Bytes())
}

// WriteJSONRPCErrorToHTTPResponseWriter is a convenience function that
// creates a JSON-RPC error response with the given id, code, and message,
// and writes it to the [http.ResponseWriter] using WriteJSONRPCResponse.
func WriteJSONRPCErrorToHTTPResponseWriter(w http.ResponseWriter, id jsonrpcv2.ID, code int, message string) {
	WriteJSONRPCResponseToHTTPResponseWriter(w, jsonrpcv2.CreateGeneralErrorResponse(id, code, message))
}

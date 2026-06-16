package http

import (
	"mime"
	"net/http"
)

// httpEnsureContentTypeIsJSONRPCMiddleware is a middleware that checks the
// the Content-Type of the incoming request, and ensures that it matches
// the expected value for the JSON-RPC request.
type httpEnsureContentTypeIsJSONRPCMiddleware struct {
	handler http.Handler
}

// ServeHTTP implemetns http.Handler
//
// This middleware checks the Content-Type header, and ensures that the value
// from it matches an expected and acceptable value for the JSON-RPC protocol.
//
// NOTE: Since JSON RPC 2.0 is Transport agnostic, there's no real MIME type
// that is expected or accepted here.
//
// There is a historical specification for JSON-RPC 1.0 over HTTP that
// specifies three acceptable MIME types for JSON-RPC 1.0.  So we might as
// well check for any of them.
// Specification https://www.jsonrpc.org/historical/json-rpc-over-http.html
func (m *httpEnsureContentTypeIsJSONRPCMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, "unable to determine Content-Type", http.StatusUnsupportedMediaType)
		return
	}

	switch mediaType {
	default:
		http.Error(w, "invalid media type requested", http.StatusUnsupportedMediaType)
		return

	case "application/json", "application/json-rpc", "application/jsonrequest":
		// These are allowable based on the JSON-RPC 1.0 over HTTP specification.
		// Since JSON-RPC 2.0 is transport agnostic, this content type is
		// ultimately dictated by our discretion, and by whatever Ethereum
		// allows.
		break
	}
	m.handler.ServeHTTP(w, r)
}

// ContentTypeIsJSONRPCMiddleware is a middleware that checks the Content-Type
// of the incoming request, and only allows accepted values through.
func ContentTypeIsJSONRPCMiddleware(next http.Handler) http.Handler {
	return &httpEnsureContentTypeIsJSONRPCMiddleware{handler: next}
}

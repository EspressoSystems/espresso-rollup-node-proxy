package adapters

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"proxy/jsonrpcv2"
)

type httpJSONRPCInterceptor struct {
	handler     http.Handler
	interceptor Interceptor
}

// NewHTTPJSONRPCInterceptor creates a [http.Handler] that will perform
// request intercept.
func NewHTTPJSONRPCInterceptor(handler http.Handler, interceptor Interceptor) http.Handler {
	return &httpJSONRPCInterceptor{
		handler:     handler,
		interceptor: interceptor,
	}
}

// ServeHTTP implements http.Handler
//
// This method takes the Body payload, parses it as a JSON RPC Request,
// and passes it to the interceptor.  The Interceptor will modify the
// request as needed, and we will replace the original request body with
// the new request body.
func (i *httpJSONRPCInterceptor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Let's decode the body of the request and pass it to the interceptor.
	// We will replace the the body of the request, and forward it through

	originalBody := r.Body
	data, err := io.ReadAll(r.Body)
	if err != nil {
		WriteJSONRPCErrorToHTTPResponseWriter(w, nil, jsonrpcv2.CodeParseError, fmt.Sprintf("failed to parse request: %s", err))
		return
	}

	data, err = PerformRequestIntercept(data, i.interceptor)
	if err != nil {
		var jsonRPCError jsonrpcv2.Error
		if errors.As(err, &jsonRPCError) {
			// This is a JSON RPC Error, so we'll forward it to the client as a
			// JSON RPC Error response.
			WriteJSONRPCResponseToHTTPResponseWriter(w, jsonrpcv2.Response{
				Error: &jsonRPCError,
			})
			return
		}

		WriteJSONRPCErrorToHTTPResponseWriter(w, nil, jsonrpcv2.CodeInternalError, fmt.Sprintf("failed to intercept request: %s", err))
		return
	}

	// Replace the Request Body with the intercepted data.
	r.Body = readCloser{
		Reader: bytes.NewBuffer(data),
		Closer: originalBody,
	}

	// The content length may have changed, so we need to remove the header
	// in order to prevent errors.
	r.ContentLength = int64(len(data))

	// forward the request through the handler
	i.handler.ServeHTTP(w, r)
}

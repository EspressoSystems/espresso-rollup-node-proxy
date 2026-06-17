package adapters

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"

	"github.com/ethereum/go-ethereum/log"
)

type httpJSONRPCInterceptor struct {
	handler     http.Handler
	interceptor Interceptor
	logger      log.Logger
}

// NewHTTPJSONRPCInterceptor creates a [http.Handler] that will perform
// request intercept.
func NewHTTPJSONRPCInterceptor(logger log.Logger, handler http.Handler, interceptor Interceptor) http.Handler {
	return &httpJSONRPCInterceptor{
		handler:     handler,
		interceptor: interceptor,
		logger:      logger,
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
		i.logger.Error("failed to read JSON-RPC request body", "error", err)
		WriteJSONRPCErrorToHTTPResponseWriter(w, nil, jsonrpcv2.CodeParseError, "failed to parse request")
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

		i.logger.Error("failed to intercept JSON-RPC request", "error", err)
		WriteJSONRPCErrorToHTTPResponseWriter(w, nil, jsonrpcv2.CodeInternalError, "failed to intercept request")
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

package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	espressoStore "proxy/store"

	"github.com/ethereum/go-ethereum/log"
)

const (
	PARSE_ERROR_CODE    = -32700
	INTERNAL_ERROR_CODE = -32603
)

type Proxy struct {
	interceptor  *Interceptor
	reverseProxy *httputil.ReverseProxy
}

func NewProxy(fullNodeExecutionRPC string, store *espressoStore.EspressoStore, espressoTag string) *Proxy {
	target, err := url.Parse(fullNodeExecutionRPC)
	if err != nil {
		log.Crit("failed to parse full node execution RPC URL", "url", fullNodeExecutionRPC, "error", err)
	}

	p := &Proxy{
		interceptor: NewInterceptor(store, espressoTag),
	}

	p.reverseProxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
	}

	return p
}

func (p *Proxy) Serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error("failed to read request body", "error", err)
		writeJSONRPCError(w, nil, PARSE_ERROR_CODE, "failed to read request body")
		return
	}

	interceptedBody, err := p.interceptor.Intercept(body)
	if err != nil {
		log.Error("failed to intercept request", "error", err)
		writeJSONRPCError(w, nil, INTERNAL_ERROR_CODE, "failed to intercept request")
		return
	}

	r.Body = io.NopCloser(bytes.NewReader(interceptedBody))
	r.ContentLength = int64(len(interceptedBody))
	p.reverseProxy.ServeHTTP(w, r)
}

// writeJSONRPCError writes a JSON-RPC error response with the given id, code, and message.
// If the id is nil, it defaults to "null" as per the JSON-RPC specification
// https://www.jsonrpc.org/specification#error_object
func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	resp := JSONRPCResponse{
		Version: "2.0",
		ID:      id,
		Error:   &JSONRPCError{Code: code, Message: msg},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	err := json.NewEncoder(w).Encode(resp)
	if err != nil {
		log.Error("failed to encode json rpc error", "error", err)
	}
}

package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	espressoStore "proxy/store"

	"github.com/ethereum/go-ethereum/log"
)

const (
	PARSE_ERROR_CODE          = -32700
	INVALID_REQUEST_CODE      = -32600
	INTERNAL_ERROR_CODE       = -32603
	DefaultMaxBatchSize       = 1000
	DefaultMaxRequestBodySize = 5 * 1024 * 1024 // 5MB, matches go-ethereum defaultBodyLimit
)

type ProxyConfig struct {
	FullNodeExecutionRPC string
	EspressoTag          string
	MaxBatchSize         int
	MaxRequestBodySize   int
}

type Proxy struct {
	interceptor        *Interceptor
	reverseProxy       *httputil.ReverseProxy
	maxRequestBodySize int
}

func NewProxy(cfg *ProxyConfig, store *espressoStore.EspressoStore) *Proxy {
	target, err := url.Parse(cfg.FullNodeExecutionRPC)
	if err != nil {
		log.Crit("failed to parse full node execution RPC URL", "url", cfg.FullNodeExecutionRPC, "error", err)
	}

	p := &Proxy{
		interceptor:        NewInterceptor(store, cfg.EspressoTag, cfg.MaxBatchSize),
		maxRequestBodySize: cfg.MaxRequestBodySize,
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
	if r.Body != nil {
		defer func() {
			err := r.Body.Close()
			if err != nil {
				log.Warn("failed to close request body", "error", err)
			}
		}()
	}

	reader := io.Reader(r.Body)
	if p.maxRequestBodySize > 0 {
		reader = io.LimitReader(r.Body, int64(p.maxRequestBodySize)+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		log.Error("failed to read request body", "error", err)
		writeJSONRPCError(w, nil, PARSE_ERROR_CODE, "failed to read request body")
		return
	}

	if p.maxRequestBodySize > 0 && len(body) > p.maxRequestBodySize {
		writeJSONRPCError(w, nil, INVALID_REQUEST_CODE, "content length too large")
		return
	}

	interceptedBody, err := p.interceptor.Intercept(body)
	if err != nil {
		var batchErr *BatchTooLargeError
		if errors.As(err, &batchErr) {
			writeJSONRPCError(w, nil, INVALID_REQUEST_CODE, "batch too large")
			return
		}
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

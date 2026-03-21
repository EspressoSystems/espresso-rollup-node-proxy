package proxy

import (
	"bytes"
	"io"
	"net/http"
	espressoStore "proxy/store"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

type Proxy struct {
	fullNodeExecutionRPC string
	interceptor          *Interceptor
	httpClient           *http.Client
}

func NewProxy(fullNodeExecutionRPC string, store *espressoStore.EspressoStore, espressoTag string) *Proxy {
	return &Proxy{
		fullNodeExecutionRPC: fullNodeExecutionRPC,
		interceptor:          NewInterceptor(store, espressoTag),
		httpClient:           &http.Client{Timeout: 30 * time.Second},
	}
}

// Serve handles incoming HTTP requests, intercepts them using the Interceptor
// , forwards them to the full node execution RPC, and returns the response back to the client.
func (p *Proxy) Serve(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if err := r.Body.Close(); err != nil {
			log.Error("failed to close request body", "error", err)
		}
	}()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error("failed to read request body", "error", err)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	interceptedBody, err := p.interceptor.Intercept(body)
	if err != nil {
		log.Error("failed to intercept request", "error", err)
		http.Error(w, "failed to intercept request", http.StatusInternalServerError)
		return
	}
	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, p.fullNodeExecutionRPC, bytes.NewReader(interceptedBody))
	if err != nil {
		log.Error("failed to create upstream request", "error", err)
		http.Error(w, "failed to create upstream request", http.StatusInternalServerError)
		return
	}
	upstreamReq.Header = r.Header.Clone()

	resp, err := p.httpClient.Do(upstreamReq)
	if err != nil {
		log.Error("failed to send request to full node execution RPC", "error", err)
		http.Error(w, "failed to send request to full node execution RPC", http.StatusBadGateway)
		return
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error("failed to close response body", "error", err)
		}
	}()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Error("failed to copy response body", "error", err)
	}
}

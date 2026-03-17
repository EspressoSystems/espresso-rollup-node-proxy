package proxy

import (
	"bytes"
	"io"
	"net/http"

	"proxy/interceptor"

	"github.com/ethereum/go-ethereum/log"
)

type Proxy struct {
	fullNodeURL string
	interceptor *interceptor.Interceptor
	httpClient  *http.Client
}

func NewProxy(fullNodeURL string, blockNumberProvider interceptor.BlockNumberProvider) *Proxy {
	return &Proxy{
		fullNodeURL: fullNodeURL,
		interceptor: interceptor.NewInterceptor(blockNumberProvider),
		httpClient:  &http.Client{},
	}
}

func (p *Proxy) Serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error("failed to read request body", "error", err)
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	intercepted, err := p.interceptor.Intercept(body)
	if err != nil {
		log.Error("interception failed", "err", err)
		http.Error(w, "failed to process request: "+err.Error(), http.StatusBadRequest)
		return
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.fullNodeURL, bytes.NewReader(intercepted))
	if err != nil {
		log.Error("failed to create upstream request", "error", err)
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	upstreamReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(upstreamReq)
	if err != nil {
		log.Error("failed to send request to upstream", "error", err)
		http.Error(w, "failed to send request to upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}

	w.WriteHeader(resp.StatusCode)

	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Error("failed to copy response body", "error", err)
	}
}

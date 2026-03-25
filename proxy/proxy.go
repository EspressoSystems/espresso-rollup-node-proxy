package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	espressoStore "proxy/store"

	"github.com/ethereum/go-ethereum/log"
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
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	interceptedBody, err := p.interceptor.Intercept(body)
	if err != nil {
		log.Error("failed to intercept request", "error", err)
		http.Error(w, "failed to intercept request", http.StatusInternalServerError)
		return
	}

	r.Body = io.NopCloser(bytes.NewReader(interceptedBody))
	r.ContentLength = int64(len(interceptedBody))
	p.reverseProxy.ServeHTTP(w, r)
}

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	proxyhttp "proxy/http"
	"proxy/jsonrpcv2"
	espressoStore "proxy/store"

	"github.com/ethereum/go-ethereum/log"
)

const (
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
	interceptor             *Interceptor
	fullNodeExecutionRPCURL *url.URL
	client                  *http.Client
	maxRequestBodySize      int
}

func NewProxy(cfg *ProxyConfig, store *espressoStore.EspressoStore) *Proxy {
	target, err := url.Parse(cfg.FullNodeExecutionRPC)
	if err != nil {
		log.Crit("failed to parse full node execution RPC URL", "url", cfg.FullNodeExecutionRPC, "error", err)
	}

	p := &Proxy{
		interceptor:             NewInterceptor(store, cfg.EspressoTag, cfg.MaxBatchSize),
		maxRequestBodySize:      cfg.MaxRequestBodySize,
		fullNodeExecutionRPCURL: target,
		client:                  http.DefaultClient,
	}

	return p
}

func forwardRequest[Req any, Res any](ctx context.Context, p *Proxy, req Req) (res Res, err error) {
	body := new(bytes.Buffer)
	enc := json.NewEncoder(body)
	if err := enc.Encode(req); err != nil {
		return res, err
	}

	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.fullNodeExecutionRPCURL.String(), body)
	if err != nil {
		return res, err
	}

	rawHeadersRetrieval := ctx.Value(proxyhttp.KeyContextHTTPHeader{})
	if cast, castOK := rawHeadersRetrieval.(http.Header); castOK {
		for k, values := range cast {
			r.Header[k] = values
		}
	}

	if r.Header.Get("Content-Type") == "" {
		r.Header.Add("Content-Type", "application/json")
	}

	response, err := p.client.Do(r)
	if err != nil {
		return res, err
	}

	dec := json.NewDecoder(response.Body)
	if err := dec.Decode(&res); err != nil {
		return res, err
	}

	return res, nil
}

func (p *Proxy) ServeJSONRPC(ctx context.Context, rawRequest jsonrpcv2.Request) (jsonrpcv2.Response, error) {
	request, err := p.interceptor.InterceptRequest(rawRequest)
	if err != nil {
		return jsonrpcv2.Response{}, err
	}
	return forwardRequest[jsonrpcv2.Request, jsonrpcv2.Response](ctx, p, request)
}

func (p *Proxy) ServerJSONRPCBatch(ctx context.Context, rawRequests []jsonrpcv2.Request) ([]jsonrpcv2.Response, error) {
	requests, err := p.interceptor.InterceptBatchRequests(rawRequests)
	if err != nil {
		return nil, err
	}
	return forwardRequest[[]jsonrpcv2.Request, []jsonrpcv2.Response](ctx, p, requests)
}

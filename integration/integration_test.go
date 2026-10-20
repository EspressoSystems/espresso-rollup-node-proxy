package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/adapters"
	proxyhttp "github.com/EspressoSystems/espresso-rollup-node-proxy/http"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/proxy"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/store/storetest"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

const (
	DefaultMaxBatchSize       = proxy.DefaultMaxBatchSize
	DefaultMaxRequestBodySize = proxy.DefaultMaxRequestBodySize
)

// newTestReverseProxyHandler wires a store whose Espresso-finalized L2 block
// is l2BlockNumber into an interceptor in front of a reverse proxy to
// upstreamURL.
func newTestReverseProxyHandler(t *testing.T, upstreamURL *url.URL, l2BlockNumber uint64, espressoTags []string, maxBatchSize int) http.Handler {
	t.Helper()
	reverseProxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	interceptor := proxy.NewInterceptor(nil, storetest.NewAtBlock(t, l2BlockNumber), espressoTags, maxBatchSize)
	return adapters.NewHTTPJSONRPCInterceptor(log.Root(), reverseProxy, interceptor)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// TestServe contains various tests ensuring that the full http server to
// Proxy, Interceptor, and Reverse Proxy behaves as expected in various
// scenarios.
func TestServe(t *testing.T) {
	// This test ensures that string requests to the ETH JSON RPC doesn't
	// get modified by the Interceptor when the tag does not match
	// the configured espresso tag value.
	t.Run("doesnt replace requests without espresso tag", func(t *testing.T) {
		upstreamURL, seen := newRecordingUpstream(t)
		reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, 100, []string{"espresso"}, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		rec := serveJSON(handler, `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","latest"]}`)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		require.Equal(t, []string{"0xabc", "latest"}, seen(), "upstream must receive the params unchanged")

		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Equal(t, upstreamResult, resp["result"])
	})

	// This test simulates a successful request utilizing the "espresso" tag
	// as a request parameter.
	//
	// The interceptor is configured with the "espresso" tag, and it should
	// be replaced as expected.
	t.Run("replaces espresso tag before forwarding", func(t *testing.T) {
		upstreamURL, seen := newRecordingUpstream(t)
		reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, 100, []string{"espresso"}, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		rec := serveJSON(handler, `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","espresso"]}`)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, []string{"0xabc", "0x64"}, seen(), "upstream should receive the replaced block number")
	})

	// This test simulates a failure to read the body of the request
	// which should result in a parse error, as the hanlder was unable
	// to parse valid JSON.
	t.Run("returns parse error when body read fails", func(t *testing.T) {
		reverseProxyHandler := newTestReverseProxyHandler(t, nil, 100, []string{"espresso"}, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		req := httptest.NewRequest(http.MethodPost, "/", &errReader{})
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var resp jsonrpcv2.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Error)
		require.Equal(t, jsonrpcv2.CodeParseError, resp.Error.Code)
		require.JSONEq(t, "null", jsonrpcv2.IDToString(resp.ID))
	})

	// This test ensures that if our request to the upstream source fails, that
	// we handle the error and return an internal error as expected.
	//
	// This is facilitated by utilizing a non-existent URL for the upstream.
	t.Run("returns internal error when upstream request fails", func(t *testing.T) {
		reverseProxyHandler := newTestReverseProxyHandler(t, nil, 100, []string{"espresso"}, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxBatchSize, reverseProxyHandler)
		reqBody := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]}`
		rec := serveJSON(handler, reqBody)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var resp jsonrpcv2.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Error)
		require.Equal(t, jsonrpcv2.CodeInternalError, resp.Error.Code)
		require.JSONEq(t, "null", jsonrpcv2.IDToString(resp.ID))
	})

	// This test ensures that invalid json submitted to the server will result
	// in a parse error.
	t.Run("returns parse error when the request is not valid json", func(t *testing.T) {
		reverseProxyHandler := newTestReverseProxyHandler(t, nil, 100, []string{"espresso"}, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		rec := serveJSON(handler, "not valid json")

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var resp jsonrpcv2.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Error)
		require.Equal(t, jsonrpcv2.CodeParseError, resp.Error.Code)
		require.JSONEq(t, "null", jsonrpcv2.IDToString(resp.ID))
	})

	// This test checks the interceptor's configuration when it comes to maximum
	// batch requests.
	//
	// If the user submits more requests than the interceptor is configured to
	// allow, then it should result in an error with nothing being processed.
	t.Run("rejects batch exceeding max batch size", func(t *testing.T) {
		reverseProxyHandler := newTestReverseProxyHandler(t, nil, 100, []string{"espresso"}, 2)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		body := `[{"jsonrpc":"2.0","id":1,"method":"eth_chainId"},{"jsonrpc":"2.0","id":2,"method":"eth_chainId"},{"jsonrpc":"2.0","id":3,"method":"eth_chainId"}]`
		rec := serveJSON(handler, body)

		var resp jsonrpcv2.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Error)
		require.Equal(t, jsonrpcv2.CodeInvalidRequest, resp.Error.Code)
	})

	// This test is utilized to determine the behavior of a successful request
	// with the interceptor setup to use the string "finalized" as the
	// espressoTag to replace.
	//
	// This should replace the "finalized" string as expected.
	t.Run("replaces finalized tag when configured", func(t *testing.T) {
		upstreamURL, seen := newRecordingUpstream(t)
		reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, 100, []string{"finalized"}, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		rec := serveJSON(handler, `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","finalized"]}`)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, []string{"0xabc", "0x64"}, seen(), "upstream should receive the replaced block number")
	})
}

// upstreamResult is the result newRecordingUpstream returns when the request
// is a single JSON-RPC object rather than an array of requests.
const upstreamResult = "0x1"

// stringParams collects every string value in the positional params of each
// request in body — a block tag is one of them, whatever its position — and
// reports whether body was an array of requests rather than a single one.
func stringParams(t *testing.T, body []byte) (params []string, isArray bool) {
	t.Helper()
	// Decode both shapes through one path by wrapping a single request in a
	// one-element array.
	isArray = bytes.HasPrefix(bytes.TrimSpace(body), []byte("["))
	if !isArray {
		body = append(append([]byte("["), body...), ']')
	}
	var reqs []jsonrpcv2.Request
	require.NoError(t, json.Unmarshal(body, &reqs))

	for _, req := range reqs {
		positional, _ := req.Params.([]any)
		for _, param := range positional {
			if s, ok := param.(string); ok {
				params = append(params, s)
			}
		}
	}
	return params, isArray
}

// newRecordingUpstream starts a fake full node that records the string params
// of every request it receives and answers with a fixed result. The returned
// function returns what has been seen so far.
func newRecordingUpstream(t *testing.T) (*url.URL, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		params, isArray := stringParams(t, body)

		mu.Lock()
		seen = append(seen, params...)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if isArray {
			_, _ = w.Write([]byte(`[]`))
		} else {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + upstreamResult + `"}`))
		}
	}))
	t.Cleanup(upstream.Close)

	recorded := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
	return &url.URL{Scheme: "http", Host: upstream.Listener.Addr().String()}, recorded
}

// serveJSON posts body to handler and returns the recorded response.
func serveJSON(handler http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestServeMultipleTags verifies through the full HTTP stack (middlewares →
// interceptor → reverse proxy) that every configured tag in a request array
// is rewritten before the request reaches the full node, while unconfigured tags
// and plain block numbers are forwarded verbatim. Which strings match is
// exhaustively unit-tested in the proxy package.
func TestServeMultipleTags(t *testing.T) {
	const blockNumber uint64 = 100
	const want = "0x64"

	getBlock := func(id int, tag string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"eth_getBlockByNumber","params":[%q,false]}`, id, tag)
	}

	upstreamURL, seen := newRecordingUpstream(t)
	reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, blockNumber, []string{"safe", "finalized", "espresso"}, DefaultMaxBatchSize)
	handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

	sent := []string{"safe", "finalized", "espresso", "latest", "0x1"}
	// The three configured tags resolve to the block number; "latest" is not
	// configured and a hex block number is not a tag, so both go through as-is.
	expected := []string{want, want, want, "latest", "0x1"}

	var reqs []string
	for i, tag := range sent {
		reqs = append(reqs, getBlock(i, tag))
	}

	rec := serveJSON(handler, "["+strings.Join(reqs, ",")+"]")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, expected, seen())
}

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
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/adapters"
	proxyhttp "github.com/EspressoSystems/espresso-rollup-node-proxy/http"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/proxy"
	espressoStore "github.com/EspressoSystems/espresso-rollup-node-proxy/store"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

const (
	DefaultMaxBatchSize       = proxy.DefaultMaxBatchSize
	DefaultMaxRequestBodySize = proxy.DefaultMaxRequestBodySize
)

func newTestReverseProxyHandler(t *testing.T, upstreamURL *url.URL, l2BlockNumber uint64, espressoTags []string, maxBatchSize int) http.Handler {
	t.Helper()
	fp := filepath.Join(t.TempDir(), "state.json")
	store, err := espressoStore.NewEspressoStore(fp, 1)
	require.NoError(t, err)
	updated, err := store.UpdateIfGreater(l2BlockNumber, 1)
	require.True(t, updated)
	require.NoError(t, err)
	reverseProxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	interceptor := proxy.NewInterceptor(nil, store, espressoTags, maxBatchSize)
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
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var req jsonrpcv2.Request
			require.NoError(t, json.Unmarshal(body, &req))

			cast, castOK := req.Params.([]any)
			require.True(t, castOK)
			params := cast
			require.Equal(t, "latest", params[1])

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
		}))

		defer upstream.Close()
		upstreamURL := &url.URL{
			Scheme: "http",
			Host:   upstream.Listener.Addr().String(),
		}
		reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, 100, []string{"espresso"}, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)
		reqBody := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","latest"]}`
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Equal(t, "0x1", resp["result"])
	})

	// This test simulates a successful request utilizing the "espresso" tag
	// as a request parameter.
	//
	// The interceptor is configured with the "espresso" tag, and it should
	// be replaced as expected.
	t.Run("replaces espresso tag before forwarding", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var req jsonrpcv2.Request
			require.NoError(t, json.Unmarshal(body, &req))

			cast, castOK := req.Params.([]any)
			require.True(t, castOK)
			params := cast
			require.Equal(t, "0x64", params[1], "upstream should receive the replaced block number")

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x2"}`))
		}))
		defer upstream.Close()

		upstreamURL := &url.URL{
			Scheme: "http",
			Host:   upstream.Listener.Addr().String(),
		}
		reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, 100, []string{"espresso"}, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","espresso"]}`
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
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
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

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

		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("not valid json"))
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

	// This test checks the interceptor's configuration when it comes to maximum
	// batch requests.
	//
	// If the user submits more requests than the interceptor is configured to
	// allow, then it should result in an error with nothing being processed.
	t.Run("rejects batch exceeding max batch size", func(t *testing.T) {
		fp := filepath.Join(t.TempDir(), "state.json")
		store, err := espressoStore.NewEspressoStore(fp, 1)
		require.NoError(t, err)
		updated, err := store.UpdateIfGreater(100, 1)
		require.True(t, updated)
		require.NoError(t, err)
		reverseProxyHandler := newTestReverseProxyHandler(t, nil, 100, []string{"espresso"}, 2)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		body := `[{"jsonrpc":"2.0","id":1,"method":"eth_chainId"},{"jsonrpc":"2.0","id":2,"method":"eth_chainId"},{"jsonrpc":"2.0","id":3,"method":"eth_chainId"}]`
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
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
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var req jsonrpcv2.Request
			require.NoError(t, json.Unmarshal(body, &req))

			cast, castOK := req.Params.([]any)
			require.True(t, castOK)
			params := cast
			require.Equal(t, "0x64", params[1])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x3"}`))
		}))
		defer upstream.Close()

		upstreamURL := &url.URL{
			Scheme: "http",
			Host:   upstream.Listener.Addr().String(),
		}
		reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, 100, []string{"finalized"}, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","finalized"]}`
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
	})
}

// standardBlockTags are the block tags defined by the Ethereum JSON-RPC spec
// plus the proxy's default tag.
var standardBlockTags = []string{"earliest", "latest", "pending", "safe", "finalized", "espresso"}

// blockParams extracts params[0] as a string from every request in body,
// which may be a single request or a batch.
func blockParams(t *testing.T, body []byte) []string {
	t.Helper()
	var reqs []jsonrpcv2.Request
	if err := json.Unmarshal(body, &reqs); err != nil {
		var req jsonrpcv2.Request
		require.NoError(t, json.Unmarshal(body, &req))
		reqs = []jsonrpcv2.Request{req}
	}
	out := make([]string, 0, len(reqs))
	for _, req := range reqs {
		params, ok := req.Params.([]any)
		require.True(t, ok, "params must be a positional array")
		require.NotEmpty(t, params)
		s, ok := params[0].(string)
		require.True(t, ok, "params[0] must be a string")
		out = append(out, s)
	}
	return out
}

// newRecordingUpstream starts a fake full node that records the block
// parameter of every request it receives. The returned function drains and
// returns what was seen so far.
func newRecordingUpstream(t *testing.T) (*url.URL, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		mu.Lock()
		seen = append(seen, blockParams(t, body)...)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
			_, _ = w.Write([]byte(`[]`))
		} else {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
		}
	}))
	t.Cleanup(upstream.Close)

	drain := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := append([]string(nil), seen...)
		seen = nil
		return out
	}
	return &url.URL{Scheme: "http", Host: upstream.Listener.Addr().String()}, drain
}

// serveJSON posts body to handler and returns the recorded response.
func serveJSON(handler http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestServeAnyTag verifies through the full HTTP stack (middlewares →
// interceptor → reverse proxy) that interception is not limited to a fixed set
// of tags: whatever tag is configured — any standard block tag or a custom
// string — is what gets rewritten before the request reaches the full node,
// and every other tag is forwarded verbatim.
func TestServeAnyTag(t *testing.T) {
	const blockNumber uint64 = 100
	const want = "0x64"

	getBlock := func(id int, tag string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"eth_getBlockByNumber","params":[%q,false]}`, id, tag)
	}

	allTags := append(append([]string{}, standardBlockTags...), "my-custom-tag")

	for _, tag := range allTags {
		t.Run("replaces "+tag+" and forwards the rest when configured alone", func(t *testing.T) {
			upstreamURL, drain := newRecordingUpstream(t)
			reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, blockNumber, []string{tag}, DefaultMaxBatchSize)
			handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

			rec := serveJSON(handler, getBlock(1, tag))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, []string{want}, drain(), "upstream must receive the Espresso block number for %q", tag)

			for _, other := range allTags {
				if other == tag {
					continue
				}
				rec := serveJSON(handler, getBlock(1, other))
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, []string{other}, drain(), "upstream must receive %q untouched when only %q is configured", other, tag)
			}
		})
	}

	t.Run("replaces every standard tag in one batch when all are configured", func(t *testing.T) {
		upstreamURL, drain := newRecordingUpstream(t)
		reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, blockNumber, standardBlockTags, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		var reqs []string
		var expected []string
		for i, tag := range standardBlockTags {
			reqs = append(reqs, getBlock(i, tag))
			expected = append(expected, want)
		}
		// A hex block number is not a tag and must reach the upstream as-is.
		reqs = append(reqs, getBlock(99, "0x1"))
		expected = append(expected, "0x1")

		rec := serveJSON(handler, "["+strings.Join(reqs, ",")+"]")
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, expected, drain())
	})

	t.Run("forwards configured tags unchanged while espresso state is unknown", func(t *testing.T) {
		upstreamURL, drain := newRecordingUpstream(t)

		fp := filepath.Join(t.TempDir(), "state.json")
		store, err := espressoStore.NewEspressoStore(fp, 1)
		require.NoError(t, err)
		reverseProxy := httputil.NewSingleHostReverseProxy(upstreamURL)
		interceptor := proxy.NewInterceptor(nil, store, standardBlockTags, DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize,
			adapters.NewHTTPJSONRPCInterceptor(log.Root(), reverseProxy, interceptor))

		rec := serveJSON(handler, getBlock(1, "finalized"))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, []string{"finalized"}, drain())
	})
}

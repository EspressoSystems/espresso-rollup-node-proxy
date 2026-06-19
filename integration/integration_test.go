package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
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

func newTestReverseProxyHandler(t *testing.T, upstreamURL *url.URL, l2BlockNumber uint64, espressoTag string, maxBatchSize int) http.Handler {
	t.Helper()
	fp := filepath.Join(t.TempDir(), "state.json")
	store, err := espressoStore.NewEspressoStore(fp, 1)
	require.NoError(t, err)
	updated, err := store.UpdateIfGreater(l2BlockNumber, 1)
	require.True(t, updated)
	require.NoError(t, err)
	reverseProxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	interceptor := proxy.NewInterceptor(nil, store, espressoTag, maxBatchSize)
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
		reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, 100, "espresso", DefaultMaxBatchSize)
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
		reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, 100, "espresso", DefaultMaxBatchSize)
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
		reverseProxyHandler := newTestReverseProxyHandler(t, nil, 100, "espresso", DefaultMaxBatchSize)
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
		reverseProxyHandler := newTestReverseProxyHandler(t, nil, 100, "espresso", DefaultMaxBatchSize)
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
		reverseProxyHandler := newTestReverseProxyHandler(t, nil, 100, "espresso", DefaultMaxBatchSize)
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
		reverseProxyHandler := newTestReverseProxyHandler(t, nil, 100, "espresso", 2)
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
		reverseProxyHandler := newTestReverseProxyHandler(t, upstreamURL, 100, "finalized", DefaultMaxBatchSize)
		handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), DefaultMaxRequestBodySize, reverseProxyHandler)

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","finalized"]}`
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
	})
}

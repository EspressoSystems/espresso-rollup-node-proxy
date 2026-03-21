package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	espressoStore "proxy/store"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestProxy(t *testing.T, upstreamURL string, l2BlockNumber uint64, espressoTag string) *Proxy {
	t.Helper()
	fp := filepath.Join(t.TempDir(), "state.json")
	store, err := espressoStore.NewEspressoStore(fp, 1, l2BlockNumber)
	require.NoError(t, err)
	return NewProxy(upstreamURL, store, espressoTag)
}

func TestServe(t *testing.T) {
	t.Run("doesnt replace requests without espresso tag", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var req JSONRPCRequest
			require.NoError(t, json.Unmarshal(body, &req))

			var params []string
			require.NoError(t, json.Unmarshal(req.Params, &params))
			require.Equal(t, "latest", params[1])

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
		}))

		defer upstream.Close()
		proxy := newTestProxy(t, upstream.URL, 100, "espresso")

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","latest"]}`
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		proxy.Serve(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Equal(t, "0x1", resp["result"])
	})

	t.Run("replaces espresso tag before forwarding", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var req JSONRPCRequest
			require.NoError(t, json.Unmarshal(body, &req))

			var params []string
			require.NoError(t, json.Unmarshal(req.Params, &params))
			require.Equal(t, "0x64", params[1], "upstream should receive the replaced block number")

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x2"}`))
		}))
		defer upstream.Close()

		proxy := newTestProxy(t, upstream.URL, 100, "espresso")

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","espresso"]}`
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		proxy.Serve(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("replaces finalized tag when configured", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var req JSONRPCRequest
			require.NoError(t, json.Unmarshal(body, &req))

			var params []string
			require.NoError(t, json.Unmarshal(req.Params, &params))
			require.Equal(t, "0x64", params[1])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x3"}`))
		}))
		defer upstream.Close()

		proxy := newTestProxy(t, upstream.URL, 100, "finalized")

		reqBody := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","finalized"]}`
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		proxy.Serve(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
	})

}

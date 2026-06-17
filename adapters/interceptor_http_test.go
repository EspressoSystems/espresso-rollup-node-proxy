package adapters_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/adapters"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// errorInterceptor is a test interceptor that returns a pre-configured error.
type errorInterceptor struct{ err error }

func (e *errorInterceptor) InterceptRequest(r jsonrpcv2.Request) (jsonrpcv2.Request, error) {
	return r, e.err
}

func (e *errorInterceptor) InterceptBatchRequests(reqs []jsonrpcv2.Request) ([]jsonrpcv2.Request, error) {
	return nil, e.err
}

// TestHTTPInterceptor_JSONRPCErrorExtractedFromJoinedChain verifies that when
// the interceptor returns a jsonrpcv2.Error wrapped inside an errors.Join chain,
// the HTTP handler correctly extracts it via errors.As and forwards it as a
// JSON-RPC error response rather than a generic internal error.
func TestHTTPInterceptor_JSONRPCErrorExtractedFromJoinedChain(t *testing.T) {
	sentinel := errors.New("sentinel")
	rpcErr := jsonrpcv2.Error{Code: jsonrpcv2.CodeInvalidRequest, Message: "bad request"}
	joined := fmt.Errorf("wrapped: %w", errors.Join(sentinel, rpcErr))

	handler := adapters.NewHTTPJSONRPCInterceptor(
		log.Root(),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("upstream handler should not be called on interceptor error")
		}),
		&errorInterceptor{err: joined},
	)

	body := `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var decoded jsonrpcv2.Response
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))
	require.NotNil(t, decoded.Error)
	require.Equal(t, jsonrpcv2.CodeInvalidRequest, decoded.Error.Code)
}

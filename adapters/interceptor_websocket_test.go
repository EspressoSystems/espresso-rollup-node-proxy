package adapters_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/adapters"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket"
	"github.com/stretchr/testify/require"
)

// mockWSConn is a test double for websocket.Conn. It returns a pre-configured
// Read result and records all messages passed to Write.
type mockWSConn struct {
	readMsg []byte
	readErr error
	written [][]byte
}

func (m *mockWSConn) Read(_ context.Context) (websocket.MessageType, []byte, error) {
	return websocket.MessageTypeText, m.readMsg, m.readErr
}

func (m *mockWSConn) Write(_ context.Context, _ websocket.MessageType, p []byte) error {
	m.written = append(m.written, append([]byte(nil), p...))
	return nil
}

func (m *mockWSConn) Close(_ websocket.Status, _ string) error { return nil }

func (m *mockWSConn) IsCloseError(err error) (websocket.CloseError, bool) {
	var ce websocket.CloseError
	return ce, errors.As(err, &ce)
}

func (m *mockWSConn) SubProtocol() string { return "" }

// TestWebSocketIntercept_ReadErrorPassthrough verifies that an error returned
// by the underlying connection's Read is returned directly to the caller
// without any write to the connection.
func TestWebSocketIntercept_ReadErrorPassthrough(t *testing.T) {
	readErr := errors.New("connection reset")
	conn := &mockWSConn{readErr: readErr}
	intercept := adapters.NewWebsocketJSONRPCDownstreamIntercept(conn, &errorInterceptor{})

	_, _, err := intercept.Read(context.Background())

	require.ErrorIs(t, err, readErr)
	require.Empty(t, conn.written, "no write should occur when the underlying Read fails")
}

// TestWebSocketIntercept_JSONRPCErrorWrittenToConn verifies that when the
// interceptor returns a jsonrpcv2.Error, a JSON-RPC error response containing
// that error is written back to the downstream connection before the error is
// returned to the caller.
func TestWebSocketIntercept_JSONRPCErrorWrittenToConn(t *testing.T) {
	rpcErr := jsonrpcv2.Error{Code: jsonrpcv2.CodeInvalidRequest, Message: "bad request"}
	validMsg := `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`

	conn := &mockWSConn{readMsg: []byte(validMsg)}
	intercept := adapters.NewWebsocketJSONRPCDownstreamIntercept(conn, &errorInterceptor{err: rpcErr})

	_, _, err := intercept.Read(context.Background())

	require.Error(t, err)
	require.Len(t, conn.written, 1, "JSON-RPC error response should be written to the connection")

	var resp jsonrpcv2.Response
	require.NoError(t, json.Unmarshal(conn.written[0], &resp))
	require.NotNil(t, resp.Error)
	require.Equal(t, jsonrpcv2.CodeInvalidRequest, resp.Error.Code)
}

// TestWebSocketIntercept_InternalErrorWrittenAsGenericResponse verifies that
// when the interceptor returns a non-jsonrpcv2.Error, a generic internal error
// JSON-RPC response is written to the downstream connection and the error is
// returned to the caller.
func TestWebSocketIntercept_InternalErrorWrittenAsGenericResponse(t *testing.T) {
	internalErr := errors.New("unexpected internal failure")
	validMsg := `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`

	conn := &mockWSConn{readMsg: []byte(validMsg)}
	intercept := adapters.NewWebsocketJSONRPCDownstreamIntercept(conn, &errorInterceptor{err: internalErr})

	_, _, err := intercept.Read(context.Background())

	require.Error(t, err)
	require.Len(t, conn.written, 1, "generic error response should be written to the connection")

	var resp jsonrpcv2.Response
	require.NoError(t, json.Unmarshal(conn.written[0], &resp))
	require.NotNil(t, resp.Error)
	require.Equal(t, jsonrpcv2.CodeInternalError, resp.Error.Code)
}

// TestWebSocketIntercept_SuccessPassesMessageThrough verifies that a valid
// JSON-RPC message that the interceptor accepts is returned unmodified without
// writing any response to the connection.
func TestWebSocketIntercept_SuccessPassesMessageThrough(t *testing.T) {
	validMsg := `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`

	conn := &mockWSConn{readMsg: []byte(validMsg)}
	intercept := adapters.NewWebsocketJSONRPCDownstreamIntercept(conn, &passthroughInterceptor{})

	_, msg, err := intercept.Read(context.Background())

	require.NoError(t, err)
	require.JSONEq(t, validMsg, string(msg))
	require.Empty(t, conn.written, "no write should occur on a successful intercept")
}

// passthroughInterceptor is a test interceptor that returns the request unchanged.
type passthroughInterceptor struct{}

func (p *passthroughInterceptor) InterceptRequest(r jsonrpcv2.Request) (jsonrpcv2.Request, error) {
	return r, nil
}

func (p *passthroughInterceptor) InterceptBatchRequests(reqs []jsonrpcv2.Request) ([]jsonrpcv2.Request, error) {
	return reqs, nil
}

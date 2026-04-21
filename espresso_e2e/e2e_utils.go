package espresso_e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"proxy/proxy"
	verifier "proxy/verifier/op"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	espressostore "proxy/store"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	opStreamer "github.com/EspressoSystems/espresso-streamers/op"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// For e2e tests we are using the mock client as there currently is an issue with light client in espresso dev node
// Eventually we will fix it and remove this.
type mockLightClient struct {
	client *espressoClient.Client
	last   uint64
}

func (m *mockLightClient) FinalizedState(_ *bind.CallOpts) (opStreamer.FinalizedState, error) {
	current, err := m.client.FetchLatestBlockHeight(context.Background())
	result := m.last
	if err == nil {
		// Make sure finalized state is back enough blocks
		m.last = 0
		if current > finalizedBlocks {
			m.last = current - finalizedBlocks
		}
	}
	return opStreamer.FinalizedState{
		BlockHeight:   result,
		ViewNum:       0,
		BlockCommRoot: big.NewInt(0),
	}, nil
}

const (
	rollupWorkingDir               = "./op"
	l1GethURL                      = "http://127.0.0.1:8545"
	espressoURL                    = "http://127.0.0.1:24000"
	opGethSeqURL                   = "http://127.0.0.1:8546"
	opGethFullNode                 = "http://127.0.0.1:8555"
	opNodeSeqURL                   = "http://127.0.0.1:9545"
	opNodeFullNode                 = "http://127.0.0.1:9548"
	mockBeaconURL                  = "http://127.0.0.1:5052"
	p2pAttackUrl                   = "http://127.0.0.1:8560"
	L2_CHAIN_ID                    = 22266222
	espressoTag                    = "espresso"
	finalizedBlocks                = 100
	batchAuthenticatorAddress      = "0x4826533b4897376654bb4d4ad88b7fafd0c98528"
	batchAuthenticatorOwnerAddress = "0x90F79bf6EB2c4f870365E785982E1f101E93b906"
)

func startVerifier(ctx context.Context, t *testing.T, logger log.Logger, store *espressostore.EspressoStore) *verifier.OPEspressoBatchVerifier {
	t.Helper()
	l1Client, err := ethclient.DialContext(ctx, l1GethURL)
	if err != nil {
		t.Fatalf("failed to create L1 client: %v", err)
	}
	v := verifier.NewOPEspressoBatchVerifier(ctx, logger, store,
		l1Client,
		&mockLightClient{client: espressoClient.NewClient(espressoURL)},
		&verifier.OPEspressoBatchVerifierConfig{
			FullNodeExecutionRPC:      opGethFullNode,
			FullNodeConsensusRPC:      opNodeFullNode,
			VerificationInterval:      250 * time.Millisecond,
			QueryServiceURL:           espressoURL,
			BatcherAddress:            "0x976EA74026E726554dB657fA54763abd0C3a0aa9",
			BatchAuthenticatorAddress: batchAuthenticatorAddress,
		},
	)
	v.Start(ctx)
	return v
}

func runDockerCompose(workingDir string, services ...string) func() {
	return runDockerComposeFile(workingDir, "", services...)
}

func runDockerComposeFile(workingDir string, composeFile string, services ...string) func() {
	fileArgs := []string{}
	if composeFile != "" {
		fileArgs = []string{"-f", composeFile}
	}

	shutdown := func() {
		args := append([]string{"compose"}, fileArgs...)
		args = append(args, "--profile", "fallback", "down", "--volumes", "--remove-orphans")
		p := exec.Command("docker", args...)
		p.Dir = workingDir
		if out, err := p.CombinedOutput(); err != nil {
			log.Error("docker compose down failed", "error", err, "output", string(out))
		}
	}

	shutdown()

	invocation := append([]string{"compose"}, fileArgs...)
	invocation = append(invocation, "up", "-d", "--pull", "always")
	invocation = append(invocation, services...)
	cmd := exec.Command("docker", invocation...)
	cmd.Dir = workingDir

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Error("docker compose up failed", "error", err, "output", string(out))
		panic(fmt.Sprintf("docker compose up failed: %v\n%s", err, string(out)))
	}

	return shutdown
}

func dockerComposeStop(t *testing.T, workingDir string, service string) {
	t.Helper()
	cmd := exec.Command("docker", "compose", "stop", service)
	cmd.Dir = workingDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose stop %s failed: %v\n%s", service, err, string(out))
	}
}

func dockerComposeStart(t *testing.T, workingDir string, profiles []string, services ...string) {
	t.Helper()
	args := []string{"compose"}
	for _, p := range profiles {
		args = append(args, "--profile", p)
	}
	args = append(args, "up", "-d")
	args = append(args, services...)
	cmd := exec.Command("docker", args...)
	cmd.Dir = workingDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose start %v failed: %v\n%s", services, err, string(out))
	}
}

func switchBatcher(t *testing.T) {
	t.Helper()
	batchAuthenticatorABI, err := abi.JSON(strings.NewReader(`[{"inputs":[],"name":"switchBatcher","outputs":[],"stateMutability":"nonpayable","type":"function"}]`))
	require.NoError(t, err)
	callData, err := batchAuthenticatorABI.Pack("switchBatcher")
	require.NoError(t, err)

	txHashRaw := jsonRPCCall(t, l1GethURL, "eth_sendTransaction", jsonMarshal(t, []map[string]string{{
		"from": batchAuthenticatorOwnerAddress,
		"to":   batchAuthenticatorAddress,
		"data": "0x" + hex.EncodeToString(callData),
	}}))

	var txHash string
	require.NoError(t, json.Unmarshal(txHashRaw, &txHash))

	deadline := time.Now().Add(30 * time.Second)
	for {
		require.True(t, time.Now().Before(deadline), "switchBatcher transaction was not mined within timeout")
		receiptResp := jsonRPCCallRaw(t, l1GethURL, "eth_getTransactionReceipt", jsonMarshal(t, []any{txHash}))
		if receiptResp.Result == nil || string(receiptResp.Result) == "null" {
			time.Sleep(250 * time.Millisecond)
			continue
		}

		var receipt struct {
			Status string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(receiptResp.Result, &receipt))
		require.Equal(t, "0x1", receipt.Status, "switchBatcher transaction failed")
		return
	}
}

func getHotshotHeight(t *testing.T) uint64 {
	t.Helper()
	resp, err := http.Get(espressoURL + "/v0/status/block-height")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var height uint64
	require.NoError(t, json.Unmarshal(body, &height))
	return height
}

func waitForHTTPReady(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("HTTP service at %s did not become ready within %s", url, timeout)
}

func waitForRollupServicesReady(t *testing.T) {
	t.Helper()
	waitForHTTPReady(t, l1GethURL, 1*time.Minute)
	waitForHTTPReady(t, espressoURL+"/v0/status/block-height", 1*time.Minute)
	waitForHTTPReady(t, opGethSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opNodeSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opGethFullNode, 1*time.Minute)
	waitForHTTPReady(t, opNodeFullNode, 1*time.Minute)
}

type JSONRPCResponse struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func getBlockByTag(t *testing.T, url string, tag string) uint64 {
	t.Helper()
	result := jsonRPCCall(t, url, "eth_getBlockByNumber", jsonMarshal(t, []any{tag, false}))
	var block struct {
		Number string `json:"number"`
	}
	require.NoError(t, json.Unmarshal(result, &block))
	num, err := strconv.ParseUint(strings.TrimPrefix(block.Number, "0x"), 16, 64)
	require.NoError(t, err)
	return num
}

// jsonRPCCallRaw performs a JSON-RPC call and returns the full response
// without failing on JSON-RPC errors. Useful for comparing error responses
// between proxy and direct node.
func jsonRPCCallRaw(t *testing.T, url, method string, params json.RawMessage) JSONRPCResponse {
	t.Helper()
	req := proxy.JSONRPCRequest{
		Version: "2.0",
		ID:      json.RawMessage("1"),
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	require.NoError(t, err)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var rpcResp JSONRPCResponse
	err = json.Unmarshal(respBody, &rpcResp)
	require.NoError(t, err)
	return rpcResp
}

func jsonRPCCall(t *testing.T, url, method string, params json.RawMessage) json.RawMessage {
	t.Helper()
	rpcResp := jsonRPCCallRaw(t, url, method, params)
	if rpcResp.Error != nil && string(rpcResp.Error) != "null" {
		t.Fatalf("JSON-RPC call returned error: %s", string(rpcResp.Error))
	}
	return rpcResp.Result
}

func requireJSONRPCEqual(t *testing.T, expected, actual JSONRPCResponse, method string) {
	t.Helper()
	expectedHasErr := expected.Error != nil && string(expected.Error) != "null"
	actualHasErr := actual.Error != nil && string(actual.Error) != "null"

	// If they both have erorrs, check if the errors match
	// Otherwise fail the test
	if expectedHasErr != actualHasErr {
		t.Fatalf("method %s response type mismatch: direct error=%s result=%s, proxy error=%s result=%s",
			method, string(expected.Error), string(expected.Result), string(actual.Error), string(actual.Result))
	}

	if expectedHasErr {
		require.JSONEq(t, string(expected.Error), string(actual.Error),
			"method %s error response mismatch", method)
	} else {
		require.JSONEq(t, string(expected.Result), string(actual.Result),
			"method %s result response mismatch", method)
	}
}

type batchEntry struct {
	method string
	params json.RawMessage
}

func jsonRPCBatchCallRaw(t *testing.T, url string, entries []batchEntry) []JSONRPCResponse {
	t.Helper()
	var batch []proxy.JSONRPCRequest
	for i, e := range entries {
		batch = append(batch, proxy.JSONRPCRequest{
			Version: "2.0",
			ID:      json.RawMessage(fmt.Sprintf("%d", i+1)),
			Method:  e.method,
			Params:  e.params,
		})
	}

	body, err := json.Marshal(batch)
	require.NoError(t, err)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var rpcResps []JSONRPCResponse
	require.NoError(t, json.Unmarshal(respBody, &rpcResps), "batch response: %s", string(respBody))
	require.Len(t, rpcResps, len(entries), "batch response count mismatch")
	return rpcResps
}

func startTestProxy(ctx context.Context, t *testing.T, backendURL string, store *espressostore.EspressoStore, tag string) (proxyURL string, shutdown func()) {
	t.Helper()
	p := proxy.NewProxy(backendURL, store, tag)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyURL = "http://" + listener.Addr().String()
	server := &http.Server{Handler: http.HandlerFunc(p.Serve)}
	go func() { _ = server.Serve(listener) }()
	t.Logf("proxy listening on %s", proxyURL)
	return proxyURL, func() { _ = server.Shutdown(ctx) }
}

func pollUntil(t *testing.T, timeout time.Duration, failMsg string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		require.True(t, time.Now().Before(deadline), failMsg)
		if condition() {
			return
		}
		time.Sleep(time.Second)
	}
}

func newTestStore(t *testing.T, name string, hotshotHeight uint64) *espressostore.EspressoStore {
	t.Helper()
	stateFile := t.TempDir() + "/" + name + ".json"
	store, err := espressostore.NewEspressoStore(stateFile, hotshotHeight)
	require.NoError(t, err)
	return store
}

func newDefaultLogger() log.Logger {
	logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stdout, log.LevelInfo, true))
	log.SetDefault(logger)
	return logger
}

func replaceTag(data json.RawMessage, oldTag, newTag string) json.RawMessage {
	return json.RawMessage(
		bytes.ReplaceAll(data, []byte(fmt.Sprintf(`"%s"`, oldTag)), []byte(fmt.Sprintf(`"%s"`, newTag))),
	)
}

func getStoredBlock(t *testing.T, store *espressostore.EspressoStore) uint64 {
	t.Helper()
	state := store.GetState()
	return state.L2BlockNumber
}

func getStoredHotshotHeight(t *testing.T, store *espressostore.EspressoStore) uint64 {
	t.Helper()
	state := store.GetState()
	return state.FallbackHotshotHeight
}

func jsonMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

type logCapturer struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *logCapturer) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (c *logCapturer) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
	return nil
}
func (c *logCapturer) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *logCapturer) WithGroup(_ string) slog.Handler      { return c }

func requireLogAttrs(t *testing.T, capturer *logCapturer, msg string, expected map[string]uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if matchLogAttrs(capturer, msg, expected) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected log record %q with attrs %v not found in captured logs", msg, expected)
}

func requireLogStringAttrs(t *testing.T, capturer *logCapturer, msg string, expected map[string]string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if matchLogStringAttrs(capturer, msg, expected) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected log record %q with attrs %v not found in captured logs", msg, expected)
}

func matchLogStringAttrs(capturer *logCapturer, msg string, expected map[string]string) bool {
	capturer.mu.Lock()
	defer capturer.mu.Unlock()
	for _, r := range capturer.records {
		if r.Message != msg {
			continue
		}
		actual := make(map[string]string)
		r.Attrs(func(a slog.Attr) bool {
			if _, ok := expected[a.Key]; ok {
				actual[a.Key] = a.Value.String()
			}
			return true
		})
		allMatch := len(actual) == len(expected)
		if allMatch {
			for k, v := range expected {
				if actual[k] != v {
					allMatch = false
					break
				}
			}
		}
		if allMatch {
			return true
		}
	}
	return false
}

func matchLogAttrs(capturer *logCapturer, msg string, expected map[string]uint64) bool {
	capturer.mu.Lock()
	defer capturer.mu.Unlock()
	for _, r := range capturer.records {
		if r.Message != msg {
			continue
		}
		actual := make(map[string]uint64)
		r.Attrs(func(a slog.Attr) bool {
			if _, ok := expected[a.Key]; ok {
				actual[a.Key] = a.Value.Uint64()
			}
			return true
		})
		allMatch := len(actual) == len(expected)
		if allMatch {
			for k, v := range expected {
				if actual[k] != v {
					allMatch = false
					break
				}
			}
		}
		if allMatch {
			return true
		}
	}
	return false
}

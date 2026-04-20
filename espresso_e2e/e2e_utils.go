package espresso_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
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
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
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
	rollupWorkingDir = "./op"
	l1GethURL        = "http://127.0.0.1:8545"
	espressoURL      = "http://127.0.0.1:24000"
	opGethSeqURL     = "http://127.0.0.1:8546"
	opGethFullNode   = "http://127.0.0.1:8555"
	opNodeSeqURL     = "http://127.0.0.1:9545"
	opNodeFullNode   = "http://127.0.0.1:9548"
	mockBeaconURL    = "http://127.0.0.1:5052"
	p2pAttackUrl     = "http://127.0.0.1:8560"
	L2_CHAIN_ID      = 22266222
	espressoTag      = "espresso"
	finalizedBlocks  = 100
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
			BatchAuthenticatorAddress: "0x4826533b4897376654bb4d4ad88b7fafd0c98528",
		},
	)
	v.Start(ctx)
	return v
}

func runDockerCompose(workingDir string, services ...string) func() {
	return runDockerComposeFile(workingDir, "", services...)
}

func dockerComposeExec(t *testing.T, workingDir, composeFile, action, service string) {
	t.Helper()
	args := []string{"compose", "-f", composeFile, action, service}
	cmd := exec.Command("docker", args...)
	cmd.Dir = workingDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %s %s failed: %v\n%s", action, service, err, out)
	}
}

func runDockerComposeFile(workingDir string, composeFile string, services ...string) func() {
	fileArgs := []string{}
	if composeFile != "" {
		fileArgs = []string{"-f", composeFile}
	}

	shutdown := func() {
		args := append([]string{"compose"}, fileArgs...)
		args = append(args, "down", "--volumes", "--remove-orphans")
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

func startLoadGen(ctx context.Context, t *testing.T, rpcURL string) func() {
	t.Helper()
	const loadGenKey = "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	privateKey, err := crypto.HexToECDSA(loadGenKey)
	require.NoError(t, err)

	client, err := ethclient.DialContext(ctx, rpcURL)
	require.NoError(t, err)

	sender := crypto.PubkeyToAddress(privateKey.PublicKey)
	to := common.HexToAddress("0x0000000000000000000000000000000000000001")
	chainID := big.NewInt(L2_CHAIN_ID)

	stopCh := make(chan struct{})
	var once sync.Once
	go func() {
		nonce, err := client.PendingNonceAt(ctx, sender)
		if err != nil {
			t.Logf("load gen: failed to get nonce: %v", err)
			return
		}
		t.Logf("load gen: starting from nonce %d", nonce)
		for {
			select {
			case <-stopCh:
				t.Logf("load gen: stopped")
				return
			case <-ctx.Done():
				return
			default:
			}
			tx := types.NewTx(&types.DynamicFeeTx{
				ChainID:   chainID,
				Nonce:     nonce,
				To:        &to,
				Value:     big.NewInt(1),
				Gas:       21000,
				GasTipCap: big.NewInt(1),
				GasFeeCap: big.NewInt(1),
			})
			signed, err := types.SignTx(tx, types.NewLondonSigner(chainID), privateKey)
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			if err := client.SendTransaction(ctx, signed); err != nil {
				t.Logf("load gen: tx nonce=%d failed: %v — re-querying nonce", nonce, err)
				if n, err := client.PendingNonceAt(ctx, sender); err == nil {
					nonce = n
				}
			} else {
				// t.Logf("load gen: tx nonce=%d hash=%s sent", nonce, signed.Hash())
				nonce++
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	return func() { once.Do(func() { close(stopCh) }) }
}

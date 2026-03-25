package espresso_e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"proxy/proxy"
	"strconv"
	"strings"
	"testing"
	"time"

	espressostore "proxy/store"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

func runDockerCompose(workingDir string, services ...string) func() {
	shutdown := func() {
		p := exec.Command("docker", "compose", "down", "--volumes", "--remove-orphans")
		p.Dir = workingDir
		if out, err := p.CombinedOutput(); err != nil {
			log.Error("docker compose down failed", "error", err, "output", string(out))
		}
	}

	shutdown()

	invocation := []string{"compose", "up", "-d", "--pull", "always"}
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

func getBlockNum(t *testing.T, url string) uint64 {
	latestResult := jsonRPCCall(t, url, "eth_blockNumber", nil)
	var latestHex string
	require.NoError(t, json.Unmarshal(latestResult, &latestHex))
	block, err := strconv.ParseUint(strings.TrimPrefix(latestHex, "0x"), 16, 64)
	require.NoError(t, err)
	return block
}

func jsonRPCCall(t *testing.T, url, method string, params json.RawMessage) json.RawMessage {
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
	if rpcResp.Error != nil && string(rpcResp.Error) != "null" {
		t.Fatalf("JSON-RPC call returned error: %s", string(rpcResp.Error))
	}
	return rpcResp.Result
}

func storeBlock(t *testing.T, store *espressostore.EspressoStore) uint64 {
	t.Helper()
	state, err := store.GetState()
	require.NoError(t, err)
	return state.L2BlockNumber
}

func jsonMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

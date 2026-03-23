package espresso_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"proxy/proxy"
	espressostore "proxy/store"
	verifier "proxy/verifier/op"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

const (
	rollupWorkingDir = "./op"
	l1GethURL        = "http://127.0.0.1:8545"
	espressoURL      = "http://127.0.0.1:24000"
	opGethSeqURL     = "http://127.0.0.1:8546"
	opGethFullNode   = "http://127.0.0.1:8555"
	opNodeSeqURL     = "http://127.0.0.1:9545"
	opNodeFullNode   = "http://127.0.0.1:9548"
	mockBeaconURL    = "http://127.0.0.1:5052"
	L2_CHAIN_ID      = 22266222
	espressoTag      = "espresso"
)

// storeBlock returns the current L2 block number from the espresso store.
func storeBlock(t *testing.T, store *espressostore.EspressoStore) uint64 {
	t.Helper()
	state, err := store.GetState()
	require.NoError(t, err)
	return state.L2BlockNumber
}

// mustMarshal marshals v to JSON, failing the test on error.
func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func startVerifier(ctx context.Context, t *testing.T, logger log.Logger, store *espressostore.EspressoStore) *verifier.OPEspressoBatchVerifier {
	t.Helper()
	v := verifier.NewOPEspressoBatchVerifier(ctx, logger, store, &verifier.OPEspressoBatchVerifierConfig{
		L1RPC:                l1GethURL,
		FullNodeExecutionRPC: opGethFullNode,
		FullNodeConsensusRPC: opNodeFullNode,
		VerificationInterval: time.Second,
		QueryServiceURL:      espressoURL,
		LightClientAddress:   "0x703848f4c85f18e3acd8196c8ec91eb0b7bd0797",
		BatcherAddress:       "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
	})
	v.Start(ctx)
	return v
}

func TestOPE2ERollupEspressoProxy(t *testing.T) {
	t.Log("Starting rollup nodes")
	shutdown := runDockerCompose(rollupWorkingDir)
	defer shutdown()

	// Wait for services to come up
	t.Log("waiting for services to be ready")

	waitForHTTPReady(t, l1GethURL, 1*time.Minute)
	waitForHTTPReady(t, espressoURL+"/v0/status/block-height", 1*time.Minute)
	waitForHTTPReady(t, opGethSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opNodeSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opGethFullNode, 1*time.Minute)
	waitForHTTPReady(t, opNodeFullNode, 1*time.Minute)

	stateFile := t.TempDir() + "/espresso-state.json"
	espressoStore, err := espressostore.NewEspressoStore(stateFile, 1, 0)
	require.NoError(t, err)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	p := proxy.NewProxy(opGethFullNode, espressoStore, espressoTag)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyURL := "http://" + listener.Addr().String()
	server := &http.Server{Handler: http.HandlerFunc(p.Serve)}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Shutdown(ctx) }()
	t.Logf("proxy listening on %s", proxyURL)

	t.Log("Starting OP Verifier")
	logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true))
	log.SetDefault(logger)

	v := startVerifier(ctx, t, logger, espressoStore)
	defer v.Stop()

	const targetBlockNum = uint64(10)

	t.Log("Waiting for block 10 to be produced on OP Geth verifier")
	deadline := time.Now().Add(3 * time.Minute)
	for {
		require.True(t, time.Now().Before(deadline), "block 10 not produced within timeout")
		result := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", mustMarshal(t, []any{"0xa", false}))
		if string(result) != "null" {
			break
		}
		time.Sleep(time.Second)
	}

	t.Log("Waiting for verifier to update espresso store past block 10")
	deadline = time.Now().Add(3 * time.Minute)
	for {
		require.True(t, time.Now().Before(deadline), "verifier did not reach block 10 within timeout")
		if storeBlock(t, espressoStore) >= targetBlockNum {
			break
		}
		time.Sleep(time.Second)
	}

	verifiedBlock := storeBlock(t, espressoStore)
	verifiedBlockHex := fmt.Sprintf("0x%x", verifiedBlock)
	t.Logf("Espresso store at block %d (%s)", verifiedBlock, verifiedBlockHex)

	proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", mustMarshal(t, []any{espressoTag, false}))
	directResult := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", mustMarshal(t, []any{verifiedBlockHex, false}))
	require.JSONEq(t, string(directResult), string(proxyResult))
	t.Log("Proxy espresso tag response matches direct verifier response")
}

func TestOPE2ERollupEspressoProxyL1Reorg(t *testing.T) {
	t.Log("Starting rollup nodes")
	shutdown := runDockerCompose(rollupWorkingDir)
	defer shutdown()

	// Wait for services to come up
	t.Log("waiting for services to be ready")

	waitForHTTPReady(t, l1GethURL, 1*time.Minute)
	waitForHTTPReady(t, espressoURL+"/v0/status/block-height", 1*time.Minute)
	waitForHTTPReady(t, opGethSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opNodeSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opGethFullNode, 1*time.Minute)
	waitForHTTPReady(t, opNodeFullNode, 1*time.Minute)

	stateFile := t.TempDir() + "/espresso-state.json"
	espressoStore, err := espressostore.NewEspressoStore(stateFile, 1, 0)
	require.NoError(t, err)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	p := proxy.NewProxy(opNodeFullNode, espressoStore, espressoTag)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyURL := "http://" + listener.Addr().String()
	server := &http.Server{Handler: http.HandlerFunc(p.Serve)}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Shutdown(ctx) }()
	t.Logf("proxy listening on %s", proxyURL)

	t.Log("Starting OP Verifier")
	logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true))
	log.SetDefault(logger)

	v := startVerifier(ctx, t, logger, espressoStore)
	defer v.Stop()

	// Wait for the verifier to make initial progress so we have a baseline
	t.Log("Waiting for verifier to make initial progress")
	deadline := time.Now().Add(3 * time.Minute)
	for {
		require.True(t, time.Now().Before(deadline), "verifier did not make initial progress within timeout")
		if storeBlock(t, espressoStore) > 0 {
			break
		}
		time.Sleep(time.Second)
	}

	// Wait for L1 to reach block 60 before triggering the reorg
	const reorgTriggerL1Block = uint64(60)
	t.Logf("Waiting for L1 to reach block %d", reorgTriggerL1Block)
	deadline = time.Now().Add(3 * time.Minute)
	for {
		require.True(t, time.Now().Before(deadline), "L1 did not reach block %d within timeout", reorgTriggerL1Block)
		result := jsonRPCCall(t, l1GethURL, "eth_blockNumber", nil)
		var blockHex string
		require.NoError(t, json.Unmarshal(result, &blockHex))
		l1Block, err := strconv.ParseUint(strings.TrimPrefix(blockHex, "0x"), 16, 64)
		require.NoError(t, err)
		if l1Block >= reorgTriggerL1Block {
			break
		}
		time.Sleep(time.Second)
	}

	// Get the current L1 safe block number to use as the reorg point
	safeResult := jsonRPCCall(t, l1GethURL, "eth_getBlockByNumber", mustMarshal(t, []any{"safe", false}))
	var safeBlock map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(safeResult, &safeBlock))
	var safeNumHex string
	require.NoError(t, json.Unmarshal(safeBlock["number"], &safeNumHex))
	safeBlockNum, err := strconv.ParseUint(strings.TrimPrefix(safeNumHex, "0x"), 16, 64)
	require.NoError(t, err)
	t.Logf("L1 safe block before reorg: %d", safeBlockNum)

	blockBeforeReorg := storeBlock(t, espressoStore)
	t.Logf("Proxy at L2 block %d before triggering reorg", blockBeforeReorg)

	// Trigger the L1 reorg via the mock beacon
	t.Logf("Triggering L1 reorg at safe block %d", safeBlockNum)
	forkBody, err := json.Marshal(map[string]uint64{"blockNum": safeBlockNum})
	require.NoError(t, err)
	resp, err := http.Post(mockBeaconURL+"/fork", "application/json", bytes.NewReader(forkBody))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "mock beacon fork request failed with status %d", resp.StatusCode)
	t.Log("L1 reorg triggered successfully")

	// Poll for 1 minute asserting the verified L2 block never moves backwards,
	// and that the espresso-tagged block never exceeds the verifier's latest block.
	t.Log("Monitoring proxy block number for backwards movement during and after reorg")
	previous := blockBeforeReorg
	deadline = time.Now().Add(1 * time.Minute)
	for {
		current := storeBlock(t, espressoStore)
		require.GreaterOrEqual(t, current, previous,
			"proxy block moved backwards: was %d, now %d", previous, current)
		if current > previous {
			t.Logf("Proxy advanced to L2 block %d", current)
			previous = current
		}

		// The espresso-tagged block must not be ahead of the verifier's latest block
		latestResult := jsonRPCCall(t, opGethFullNode, "eth_blockNumber", nil)
		var latestHex string
		require.NoError(t, json.Unmarshal(latestResult, &latestHex))
		latestBlock, err := strconv.ParseUint(strings.TrimPrefix(latestHex, "0x"), 16, 64)
		require.NoError(t, err)
		require.LessOrEqual(t, current, latestBlock,
			"proxy espresso block %d is ahead of verifier latest block %d", current, latestBlock)

		if time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}

	verifiedBlock := storeBlock(t, espressoStore)
	require.Greater(t, verifiedBlock, blockBeforeReorg,
		"proxy did not advance past block %d after reorg resolved", blockBeforeReorg)
	verifiedBlockHex := fmt.Sprintf("0x%x", verifiedBlock)
	t.Logf("Proxy at L2 block %d (%s) after reorg, block never moved backwards", verifiedBlock, verifiedBlockHex)

	proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", mustMarshal(t, []any{espressoTag, false}))
	directResult := jsonRPCCall(t, opNodeFullNode, "eth_getBlockByNumber", mustMarshal(t, []any{verifiedBlockHex, false}))
	require.JSONEq(t, string(directResult), string(proxyResult))
	t.Log("Proxy espresso tag response matches direct verifier response after reorg")
}

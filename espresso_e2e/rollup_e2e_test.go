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
	rollupWorkingDir  = "./rollup"
	l1GethURL         = "http://127.0.0.1:8545"
	espressoURL       = "http://127.0.0.1:24000"
	opGethSeqURL      = "http://127.0.0.1:8546"
	opGethVerifierURL = "http://127.0.0.1:8547"
	opNodeSeqURL      = "http://127.0.0.1:9545"
	opNodeVerifierURL = "http://127.0.0.1:9546"
	mockBeaconURL     = "http://127.0.0.1:5052"
)

func startVerifier(ctx context.Context, t *testing.T, logger log.Logger, store *espressostore.EspressoStore) *verifier.OpVerifier {
	t.Helper()
	v := verifier.NewVerifier(ctx, logger, store, &verifier.OpVerifierConfig{
		L1RPC:                l1GethURL,
		FullNodeExecutionRPC: opGethVerifierURL,
		FullNodeConsensusRPC: opNodeVerifierURL,
		VerificationInterval: time.Second,
		QueryServiceURL:      espressoURL,
		LightClientAddress:   "", // TODO: set deployed light client contract address
		BatcherAddress:       "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
	})
	v.Start(ctx)
	return v
}

func TestE2ERollupEspressoProxy(t *testing.T) {
	t.Log("Starting docker compose - setting up rollup containers")
	shutdown := runDockerCompose(rollupWorkingDir)
	defer shutdown()

	// Wait for services to come up
	t.Log("waiting for services to be ready")

	waitForHTTPReady(t, l1GethURL, 1*time.Minute)
	waitForHTTPReady(t, espressoURL+"/v0/status/block-height", 1*time.Minute)
	waitForHTTPReady(t, opGethSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opGethVerifierURL, 1*time.Minute)
	waitForHTTPReady(t, opNodeSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opNodeVerifierURL, 1*time.Minute)

	// Now lets create the proxy
	stateFile := t.TempDir() + "/espresso-state.json"
	espressoStore, err := espressostore.NewEspressoStore(stateFile, 1, 0)
	if err != nil {
		t.Fatalf("failed to create espresso store: %v", err)
	}

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxy := proxy.NewProxy(opGethVerifierURL, espressoStore)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyURL := "http://" + listener.Addr().String()
	server := &http.Server{Handler: http.HandlerFunc(proxy.Serve)}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Shutdown(ctx) }()
	t.Logf("proxy listening on %s", proxyURL)

	// Start OP Verifier
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
		result := jsonRPCCall(t, opGethVerifierURL, "eth_getBlockByNumber", []interface{}{"0xa", false})
		if string(result) != "null" {
			break
		}
		time.Sleep(time.Second)
	}

	t.Log("Waiting for verifier to update espresso store past block 10")
	deadline = time.Now().Add(3 * time.Minute)
	for {
		require.True(t, time.Now().Before(deadline), "verifier did not reach block 10 within timeout")
		if espressoStore.GetBlockNumber() >= targetBlockNum {
			break
		}
		time.Sleep(time.Second)
	}

	verifiedBlock := espressoStore.GetBlockNumber()
	verifiedBlockHex := fmt.Sprintf("0x%x", verifiedBlock)
	t.Logf("Espresso store at block %d (%s)", verifiedBlock, verifiedBlockHex)

	proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", []interface{}{"espresso", false})
	directResult := jsonRPCCall(t, opGethVerifierURL, "eth_getBlockByNumber", []interface{}{verifiedBlockHex, false})
	require.JSONEq(t, string(directResult), string(proxyResult))
	t.Log("Proxy espresso tag response matches direct verifier response")
}

func TestE2ERollupEspressoProxyL1Reorg(t *testing.T) {
	t.Log("Starting docker compose - setting up rollup containers")
	shutdown := runDockerCompose(rollupWorkingDir)
	defer shutdown()

	// Wait for services to come up
	t.Log("waiting for services to be ready")

	waitForHTTPReady(t, l1GethURL, 1*time.Minute)
	waitForHTTPReady(t, espressoURL+"/v0/status/block-height", 1*time.Minute)
	waitForHTTPReady(t, opGethSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opGethVerifierURL, 1*time.Minute)
	waitForHTTPReady(t, opNodeSeqURL, 3*time.Minute)
	waitForHTTPReady(t, opNodeVerifierURL, 3*time.Minute)

	// Now lets create the proxy
	stateFile := t.TempDir() + "/espresso-state.json"
	espressoStore, err := espressostore.NewEspressoStore(stateFile, 1, 0)
	if err != nil {
		t.Fatalf("failed to create espresso store: %v", err)
	}

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxy := proxy.NewProxy(opGethVerifierURL, espressoStore)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyURL := "http://" + listener.Addr().String()
	server := &http.Server{Handler: http.HandlerFunc(proxy.Serve)}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Shutdown(ctx) }()
	t.Logf("proxy listening on %s", proxyURL)

	// Start OP Verifier
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
		if espressoStore.GetBlockNumber() > 0 {
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

	// Record the proxy's current verified L2 block before the reorg
	prevL2Block := espressoStore.GetBlockNumber()
	t.Logf("Proxy at L2 block %d before triggering reorg", prevL2Block)

	// Trigger the L1 reorg via the mock beacon
	t.Logf("Triggering L1 reorg %d", reorgTriggerL1Block-5)
	forkBody, err := json.Marshal(map[string]uint64{"blockNum": reorgTriggerL1Block - 5})
	require.NoError(t, err)
	resp, err := http.Post(mockBeaconURL+"/fork", "application/json", bytes.NewReader(forkBody))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "mock beacon fork request failed with status %d", resp.StatusCode)
	t.Log("L1 reorg triggered successfully")

	t.Log("Monitoring proxy block number for backwards movement during and after reorg")
	previous := prevL2Block
	deadline = time.Now().Add(1 * time.Minute)
	for {
		current := espressoStore.GetBlockNumber()
		require.GreaterOrEqual(t, current, previous,
			"proxy block moved backwards: was %d, now %d", previous, current)
		if current > previous {
			t.Logf("Proxy advanced to L2 block %d", current)
			previous = current
		}

		latestResult := jsonRPCCall(t, opGethVerifierURL, "eth_blockNumber", nil)
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

	verifiedBlock := espressoStore.GetBlockNumber()
	require.Greater(t, verifiedBlock, prevL2Block, "proxy did not advance past block %d after reorg resolved", prevL2Block)
	verifiedBlockHex := fmt.Sprintf("0x%x", verifiedBlock)
	t.Logf("Proxy at L2 block %d (%s) after reorg, block never moved backwards", verifiedBlock, verifiedBlockHex)

	proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", []interface{}{"espresso", false})
	directResult := jsonRPCCall(t, opGethVerifierURL, "eth_getBlockByNumber", []interface{}{verifiedBlockHex, false})
	require.JSONEq(t, string(directResult), string(proxyResult))
	t.Log("Proxy espresso tag response matches direct verifier response after reorg")
}

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
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

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
	logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stdout, log.LevelInfo, true))
	log.SetDefault(logger)

	defaultCapturer := &logCapturer{}
	v := startVerifier(ctx, t, log.NewLogger(defaultCapturer), espressoStore)
	defer v.Stop()

	t.Run("basic proxy advances", func(t *testing.T) {
		const targetBlockNum = uint64(10)

		t.Log("Waiting for block 10 to be produced on OP Geth full node")
		deadline := time.Now().Add(2 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "block 10 not produced within timeout")
			result := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{"0xa", false}))
			if string(result) != "null" {
				break
			}
			time.Sleep(time.Second)
		}

		t.Log("Waiting for OP verifer to update espresso store past block 10")
		deadline = time.Now().Add(1 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "OP verifier did not reach block 10 within timeout")
			if getStoredBlock(t, espressoStore) >= targetBlockNum {
				break
			}
			time.Sleep(time.Second)
		}

		verifiedBlock := getStoredBlock(t, espressoStore)
		t.Logf("Espresso store at block %d", verifiedBlock)

		proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", verifiedBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult))
		t.Log("Proxy espresso tag response matches direct OP geth full node response")
	})

	t.Run("rpc compatibility", func(t *testing.T) {
		userAddr := "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
		hash := "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

		methodsWithNoEspressoTag := []struct {
			method string
			params any
		}{
			{"eth_blockNumber", nil},
			{"eth_chainId", nil},
			{"eth_syncing", nil},
			{"eth_gasPrice", nil},
			{"eth_maxPriorityFeePerGas", nil},
			{"eth_accounts", nil},
			{"eth_getBlockByHash", []any{hash, false}},
			{"eth_getBlockTransactionCountByHash", []any{hash}},
			{"eth_getUncleCountByBlockHash", []any{hash}},
			{"eth_getTransactionByHash", []any{hash}},
			{"eth_getTransactionReceipt", []any{hash}},
			{"eth_getTransactionByBlockHashAndIndex", []any{hash, "0x0"}},
			{"eth_getUncleByBlockHashAndIndex", []any{hash, "0x0"}},
			{"eth_getHeaderByHash", []any{hash}},
			{"net_version", nil},
			{"net_listening", nil},
			{"net_peerCount", nil},
			{"web3_clientVersion", nil},
			{"web3_sha3", []any{"0x68656c6c6f"}},
			{"txpool_content", nil},
			{"txpool_status", nil},
			{"txpool_inspect", nil},
			{"eth_sendRawTransaction", []any{"0x00"}},
			{"eth_subscribe", []any{"newHeads"}},
			{"eth_unsubscribe", []any{"0x1"}},
		}

		for _, tc := range methodsWithNoEspressoTag {
			t.Run(tc.method, func(t *testing.T) {
				proxyResp := jsonRPCCallRaw(t, proxyURL, tc.method, jsonMarshal(t, tc.params))
				directResp := jsonRPCCallRaw(t, opGethFullNode, tc.method, jsonMarshal(t, tc.params))
				requireJSONRPCEqual(t, directResp, proxyResp, tc.method)
			})
		}

		espressoTagMethods := []struct {
			method string
			params []any
		}{
			{"eth_getBalance", []any{userAddr, espressoTag}},
			{"eth_getCode", []any{userAddr, espressoTag}},
			{"eth_getStorageAt", []any{userAddr, "0x0", espressoTag}},
			{"eth_getTransactionCount", []any{userAddr, espressoTag}},
			{"eth_call", []any{map[string]any{"to": userAddr, "data": "0x"}, espressoTag}},
			{"eth_getBlockByNumber", []any{espressoTag, false}},
			{"eth_getBlockTransactionCountByNumber", []any{espressoTag}},
			{"eth_getUncleCountByBlockNumber", []any{espressoTag}},
			{"eth_getTransactionByBlockNumberAndIndex", []any{espressoTag, "0x0"}},
			{"eth_getUncleByBlockNumberAndIndex", []any{espressoTag, "0x0"}},
			{"eth_getLogs", []any{map[string]any{"fromBlock": espressoTag, "toBlock": espressoTag}}},
			{"eth_feeHistory", []any{"0x4", espressoTag, []any{25, 75}}},
			{"eth_getHeaderByNumber", []any{espressoTag}},
			{"eth_createAccessList", []any{map[string]any{"from": userAddr, "to": userAddr, "data": "0x"}, espressoTag}},
			{"eth_simulateV1", []any{map[string]any{"blockStateCalls": []any{}}, espressoTag}},
		}
		// freeze store so espresso tag resolves to a stable block
		v.Stop()
		verifiedBlock := getStoredBlock(t, espressoStore)
		blockHex := fmt.Sprintf("0x%x", verifiedBlock)

		for _, tc := range espressoTagMethods {
			t.Run(tc.method, func(t *testing.T) {
				proxyParams := jsonMarshal(t, tc.params)
				directParams := json.RawMessage(
					bytes.ReplaceAll(proxyParams, []byte(fmt.Sprintf(`"%s"`, espressoTag)), []byte(fmt.Sprintf(`"%s"`, blockHex))),
				)
				proxyResp := jsonRPCCallRaw(t, proxyURL, tc.method, proxyParams)
				directResp := jsonRPCCallRaw(t, opGethFullNode, tc.method, directParams)
				requireJSONRPCEqual(t, directResp, proxyResp, tc.method)
			})
		}

		t.Run("batch_request", func(t *testing.T) {
			proxyBatch := []batchEntry{
				{"eth_chainId", nil},
				{"eth_blockNumber", nil},
				{"eth_getBalance", jsonMarshal(t, []any{userAddr, espressoTag})},
				{"eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false})},
				{"eth_gasPrice", nil},
				{"eth_getTransactionCount", jsonMarshal(t, []any{userAddr, espressoTag})},
			}

			directBatch := make([]batchEntry, len(proxyBatch))
			for i, e := range proxyBatch {
				directBatch[i] = batchEntry{method: e.method}
				if e.params != nil {
					directBatch[i].params = json.RawMessage(
						bytes.ReplaceAll(e.params, []byte(fmt.Sprintf(`"%s"`, espressoTag)), []byte(fmt.Sprintf(`"%s"`, blockHex))),
					)
				}
			}

			proxyResps := jsonRPCBatchCallRaw(t, proxyURL, proxyBatch)
			directResps := jsonRPCBatchCallRaw(t, opGethFullNode, directBatch)

			for i, entry := range proxyBatch {
				requireJSONRPCEqual(t, directResps[i], proxyResps[i], entry.method)
			}
		})

	})

	t.Run("proxy restart resumes from persisted state", func(t *testing.T) {
		const initialHotshotHeight = uint64(1)
		const targetBlockNum = uint64(10)

		initialStateFile := t.TempDir() + "/initial-state.json"
		finalizedL2Block := getBlockByTag(t, opGethFullNode, "finalized")
		initialStore, err := espressostore.NewEspressoStore(initialStateFile, initialHotshotHeight, finalizedL2Block)
		require.NoError(t, err)

		firstCapturer := &logCapturer{}

		nodeProxy := proxy.NewProxy(opGethFullNode, initialStore, espressoTag)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		proxyURL := "http://" + listener.Addr().String()
		server := &http.Server{Handler: http.HandlerFunc(nodeProxy.Serve)}
		go func() { _ = server.Serve(listener) }()
		t.Logf("Proxy listening on %s", proxyURL)

		// Before the verifier starts the streamer, the espresso tag should be initialized to the finalized block
		proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", finalizedL2Block), false}))
		require.JSONEq(t, string(directResult), string(proxyResult), "espresso tag should resolve to preRestartBlock before verifier starts")

		// Now we start the verifier and check if it starts with finalizedL2Block and initialHotshotHeight, and that it advances the store past block 10.
		verifier := startVerifier(ctx, t, log.NewLogger(firstCapturer), initialStore)
		requireLogAttrs(t, firstCapturer, "Starting OP Verifier", map[string]uint64{
			"start block number":               finalizedL2Block,
			"starting fallback_hotshot_height": initialHotshotHeight,
		})

		t.Log("Waiting for OP verifer to update espresso store past block 10")
		deadline := time.Now().Add(3 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "OP verifier did not reach block 10 within timeout")
			if getStoredBlock(t, initialStore) >= finalizedL2Block+targetBlockNum {
				break
			}
			time.Sleep(time.Second)
		}

		verifier.Stop()

		// After shutting down proxy and verifier, we
		// retrieve the l2 block and hotshot height from the store
		// and check that its greater than the values initially supplied
		preRestartBlock := getStoredBlock(t, initialStore)
		t.Logf("Espresso store at block %d before restart", preRestartBlock)
		preRestartHotshotHeight := getStoredHotshotHeight(t, initialStore)
		t.Logf("Espresso store hotshot height %d before restart", preRestartHotshotHeight)

		proxyResult = jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult = jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", preRestartBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult), "espresso tag should resolve to preRestartBlock before verifier starts")

		require.Greater(t, preRestartHotshotHeight, initialHotshotHeight, "store did not advance past initial hotshot height")
		require.Greater(t, preRestartBlock, finalizedL2Block, "store did not advance past finalized block")
		_ = server.Shutdown(ctx)
		t.Log("proxy and verifier stopped")

		// Now that proxy has advanced to a higher block number and hotshot height,
		// we will restart the proxy with with the same state file, and assert it resumes from the persisted state correctly.
		newStore, err := espressostore.NewEspressoStore(initialStateFile, initialHotshotHeight, finalizedL2Block)
		require.NoError(t, err)

		newProxy := proxy.NewProxy(opGethFullNode, newStore, espressoTag)
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		proxyURL = "http://" + listener.Addr().String()
		server = &http.Server{Handler: http.HandlerFunc(newProxy.Serve)}
		go func() { _ = server.Serve(listener) }()
		t.Logf("New proxy listening on %s", proxyURL)
		defer func() { _ = server.Shutdown(ctx) }()

		resumedBlock := getStoredBlock(t, newStore)
		require.Equal(t, preRestartBlock, resumedBlock, "new store should resume from persisted block")
		// Verify that the espresso tag also resolves to the preRestartBlock
		proxyResult = jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult = jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", preRestartBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult), "espresso tag should resolve to preRestartBlock")

		secondCapturer := &logCapturer{}
		verifier = startVerifier(ctx, t, log.NewLogger(secondCapturer), newStore)
		defer verifier.Stop()
		// Check that the verifier starts with the block number and hotshot height from before the restart
		requireLogAttrs(t, secondCapturer, "Starting OP Verifier", map[string]uint64{
			"start block number":               preRestartBlock,
			"starting fallback_hotshot_height": preRestartHotshotHeight,
		})
		t.Log("Verified that verifier and proxy started with correct hotshot height and L2 block number after restart")

	})

}

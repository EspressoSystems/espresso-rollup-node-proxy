package espresso_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"proxy/proxy"
	espressostore "proxy/store"
	verifier "proxy/verifier/op"
	"testing"
	"time"

	opStreamer "github.com/EspressoSystems/espresso-streamers/op"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
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
	finalizedBlocks  = 60
)

func startVerifier(ctx context.Context, t *testing.T, logger log.Logger, store *espressostore.EspressoStore) *verifier.OPEspressoBatchVerifier {
	t.Helper()
	l1Client, err := ethclient.DialContext(ctx, l1GethURL)
	if err != nil {
		logger.Crit("failed to create L1 client", "error", err)
	}
	v := verifier.NewOPEspressoBatchVerifier(ctx, logger, store,
		l1Client,
		&mockLightClient{client: espressoClient.NewClient(espressoURL)},
		&verifier.OPEspressoBatchVerifierConfig{
			FullNodeExecutionRPC:      opGethFullNode,
			FullNodeConsensusRPC:      opNodeFullNode,
			VerificationInterval:      1 * time.Millisecond,
			QueryServiceURL:           espressoURL,
			BatcherAddress:            "0x976EA74026E726554dB657fA54763abd0C3a0aa9",
			BatchAuthenticatorAddress: "0x4826533b4897376654bb4d4ad88b7fafd0c98528",
		},
	)
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
	espressoStore, err := espressostore.NewEspressoStore(stateFile, 1)
	require.NoError(t, err)
	err = espressoStore.Update(1, 1)
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

	t.Run("basic proxy advances", func(t *testing.T) {
		t.Log("Starting OP Verifier")
		logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stdout, log.LevelInfo, true))
		log.SetDefault(logger)

		v := startVerifier(ctx, t, logger, espressoStore)
		defer v.Stop()
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

	t.Run("proxy does not go backwords in case of l1 reorg", func(t *testing.T) {
		t.Log("Starting OP Verifier")
		logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stdout, log.LevelInfo, true))
		log.SetDefault(logger)
		err := espressoStore.Update(0, 1)
		require.NoError(t, err)

		v := startVerifier(ctx, t, logger, espressoStore)
		defer v.Stop()

		// Wait for L1 to advance 10 l1 blocks
		latestL1BlockNum := getBlockByTag(t, l1GethURL, "latest")
		const reorgTriggerL1Block = uint64(10)
		t.Logf("Waiting for L1 to reach block %d, currently at %d", latestL1BlockNum+reorgTriggerL1Block, latestL1BlockNum)
		deadline := time.Now().Add(3 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "L1 did not reach block %d within timeout", reorgTriggerL1Block)
			l1Block := getBlockByTag(t, l1GethURL, "latest")
			if l1Block >= latestL1BlockNum+reorgTriggerL1Block {
				break
			}
			time.Sleep(time.Second)
		}

		// Get the current L1 block number to use as the reorg point
		latestL1BlockNum = getBlockByTag(t, l1GethURL, "latest")
		t.Logf("L1 latest block before reorg: %d", latestL1BlockNum)

		blockBeforeReorg := getStoredBlock(t, espressoStore)
		t.Logf("Proxy at L2 block %d before triggering reorg", blockBeforeReorg)

		// Trigger the L1 reorg via the mock beacon
		const reorgBlocks = 3
		t.Logf("Triggering L1 reorg at block %d", latestL1BlockNum-reorgBlocks)
		forkBody, err := json.Marshal(map[string]uint64{"blockNum": latestL1BlockNum - reorgBlocks})
		require.NoError(t, err)
		resp, err := http.Post(mockBeaconURL+"/fork", "application/json", bytes.NewReader(forkBody))
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "mock beacon fork request failed with status %d", resp.StatusCode)
		t.Log("L1 reorg triggered successfully")

		// Poll for 1 minute asserting the verified L2 block never moves backwards,
		// and that the espresso-tagged block never exceeds the OP geth full nodes latest block.
		t.Log("Monitoring proxy block number for backwards movement during and after reorg")
		previous := blockBeforeReorg
		deadline = time.Now().Add(1 * time.Minute)
		for {
			current := getStoredBlock(t, espressoStore)
			require.GreaterOrEqual(t, current, previous,
				"proxy block moved backwards: was %d, now %d", previous, current)
			if current > previous {
				t.Logf("Proxy advanced to L2 block %d", current)
				previous = current
			}

			// The espresso-tagged block must not be ahead of the OP geth full nodes latest block
			latestFullNodeBlock := getBlockByTag(t, opGethFullNode, "latest")

			require.LessOrEqual(t, current, latestFullNodeBlock,
				"proxy espresso block %d is ahead of OP geth full nodes latest block %d", current, latestFullNodeBlock)

			if time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Second)
		}

		verifiedBlock := getStoredBlock(t, espressoStore)
		require.GreaterOrEqual(t, verifiedBlock, blockBeforeReorg,
			"proxy did not advance past block %d after reorg resolved", blockBeforeReorg)
		t.Logf("Proxy at L2 block %d after reorg, block never moved backwards", verifiedBlock)

		proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", verifiedBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult))
		t.Log("Proxy espresso tag response matches direct OP geth full node response after reorg")
	})

	t.Run("rpc compatibility", func(t *testing.T) {
		t.Log("Starting OP Verifier")
		logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stdout, log.LevelInfo, true))
		log.SetDefault(logger)

		v := startVerifier(ctx, t, logger, espressoStore)
		defer v.Stop()

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
		t.Log("Starting OP Verifier")
		logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stdout, log.LevelInfo, true))
		log.SetDefault(logger)

		v := startVerifier(ctx, t, logger, espressoStore)
		defer v.Stop()
		const initialHotshotHeight = uint64(1)
		const targetBlockNum = uint64(10)

		initialStateFile := t.TempDir() + "/initial-proxy-state.json"
		finalizedL2Block := getBlockByTag(t, opGethFullNode, "finalized")
		initialStore, err := espressostore.NewEspressoStore(initialStateFile, initialHotshotHeight)
		require.NoError(t, err)
		err = initialStore.Update(finalizedL2Block, initialHotshotHeight)
		require.NoError(t, err)

		firstCapturer := &logCapturer{}

		nodeProxy := proxy.NewProxy(opGethFullNode, initialStore, espressoTag)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		proxyURL := "http://" + listener.Addr().String()
		server := &http.Server{Handler: http.HandlerFunc(nodeProxy.Serve)}
		go func() { _ = server.Serve(listener) }()
		t.Logf("Proxy listening on %s", proxyURL)

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

		proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", preRestartBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult), "espresso tag should resolve to preRestartBlock before verifier starts")

		require.GreaterOrEqual(t, preRestartHotshotHeight, initialHotshotHeight, "store did not advance past initial hotshot height")
		require.Greater(t, preRestartBlock, finalizedL2Block, "store did not advance past finalized block")
		_ = server.Shutdown(ctx)
		t.Log("proxy and verifier stopped")

		// Now that proxy has advanced to a higher block number and hotshot height,
		// we will restart the proxy with with the same state file, and assert it resumes from the persisted state correctly.
		newStore, err := espressostore.NewEspressoStore(initialStateFile, initialHotshotHeight)

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

	t.Run("switchover with espresso tag", func(t *testing.T) {
		stateFile := t.TempDir() + "/switchover-state.json"
		store, err := espressostore.NewEspressoStore(stateFile, 50)
		require.NoError(t, err)

		require.Equal(t, uint64(0), getStoredBlock(t, store), "store should start with L2BlockNumber=0")

		t.Log("Starting proxy with empty store, no verifier running")
		p := proxy.NewProxy(opGethFullNode, store, espressoTag)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		proxyURL := "http://" + listener.Addr().String()
		server := &http.Server{Handler: http.HandlerFunc(p.Serve)}
		go func() { _ = server.Serve(listener) }()
		defer func() { _ = server.Shutdown(ctx) }()
		t.Logf("proxy listening on %s", proxyURL)

		// Espresso tag errors while state is empty and no verifier is running
		resp := jsonRPCCallRaw(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		require.True(t, resp.Error != nil && string(resp.Error) != "null", "should return a JSON-RPC error when store has no verified state, got result: %s", string(resp.Result))

		// Non Espresso requests should still work and return data from the OP geth full node
		resp = jsonRPCCallRaw(t, proxyURL, "eth_blockNumber", nil)
		require.True(t, resp.Error == nil || string(resp.Error) == "null", "should not return a JSON-RPC error for non-espresso tag requests, got error: %s", string(resp.Error))
		require.NotNil(t, resp.Result, "should return a result for eth_blockNumber even when store is empty")

		// Now wait for OP full node to produce blocks
		t.Log("Waiting for OP full node to produce blocks")
		deadline := time.Now().Add(2 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "OP full node did not produce block 10 within timeout")
			result := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{"0xa", false}))
			if string(result) != "null" {
				break
			}
			time.Sleep(time.Second)
		}

		resp = jsonRPCCallRaw(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		require.True(t, resp.Error != nil && string(resp.Error) != "null",
			"espresso tag should still error before verifier starts, got result: %s", string(resp.Result))

		t.Log("Confirmed: espresso tag still errors with blocks produced but no verifier running")
		t.Log("Starting verifier, it will start the streamer and sync blocks from Espresso to update the store")
		logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stdout, log.LevelInfo, true))
		log.SetDefault(logger)
		v := startVerifier(ctx, t, logger, store)
		defer v.Stop()

		t.Log("Waiting for verifier to update store (switchover)")
		deadline = time.Now().Add(3 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "verifier did not update store within timeout")
			if getStoredBlock(t, store) > 0 {
				break
			}
			time.Sleep(time.Second)
		}
		t.Logf("Switchover complete: store at block %d", getStoredBlock(t, store))

		// Now check if Espresso tag resolves after switchover
		resp = jsonRPCCallRaw(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		require.True(t, resp.Error == nil || string(resp.Error) == "null", "should not return a JSON-RPC error for espresso tag after switchover, got error: %s", string(resp.Error))
		require.NotNil(t, resp.Result, "should return a result for espresso tag after switchover")
		t.Log("Confirmed: espresso tag works after switchover")
	})

	t.Run("switchover with finalized tag", func(t *testing.T) {
		espressoTag := "finalized"
		stateFile := t.TempDir() + "/switchover-finalized-state.json"
		store, err := espressostore.NewEspressoStore(stateFile, 70)
		require.NoError(t, err)

		require.Equal(t, uint64(0), getStoredBlock(t, store), "store should start with L2BlockNumber=0")

		t.Log("Starting proxy with finalized tag, empty store, no verifier running")
		p := proxy.NewProxy(opGethFullNode, store, espressoTag)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		proxyURL := "http://" + listener.Addr().String()
		server := &http.Server{Handler: http.HandlerFunc(p.Serve)}
		go func() { _ = server.Serve(listener) }()
		defer func() { _ = server.Shutdown(ctx) }()
		t.Logf("proxy listening on %s", proxyURL)

		// Unlike the espresso tag, "finalized" is a valid Ethereum block tag so the
		// full node handles it even when the store is empty (request passes through unchanged).
		resp := jsonRPCCallRaw(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		require.True(t, resp.Error == nil || string(resp.Error) == "null",
			"finalized tag should not error with empty store, got error: %s", string(resp.Error))
		require.NotNil(t, resp.Result, "should return a result for finalized tag even with empty store")
		t.Log("Confirmed: finalized tag does not error with empty store (forwarded to full node)")

		// Wait for OP full node to produce blocks
		t.Log("Waiting for OP full node to produce blocks")
		deadline := time.Now().Add(2 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "OP full node did not produce block 10 within timeout")
			result := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{"0xa", false}))
			if string(result) != "null" {
				break
			}
			time.Sleep(time.Second)
		}

		// Before switchover: proxy forwards "finalized" to full node unchanged
		// so it should return the Ethereum finalized block (identical to calling full node directly)
		proxyResp := jsonRPCCallRaw(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResp := jsonRPCCallRaw(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{"finalized", false}))
		requireJSONRPCEqual(t, directResp, proxyResp, "eth_getBlockByNumber(finalized)")
		t.Log("Confirmed: before switchover, proxy returns same finalized block as full node (Ethereum finalized)")

		t.Log("Starting verifier, it will start the streamer and sync blocks from Espresso to update the store")
		logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stdout, log.LevelInfo, true))
		log.SetDefault(logger)
		v := startVerifier(ctx, t, logger, store)
		defer v.Stop()

		t.Log("Waiting for verifier to update store (switchover)")
		deadline = time.Now().Add(3 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "verifier did not update store within timeout")
			if getStoredBlock(t, store) > 0 {
				break
			}
			time.Sleep(time.Second)
		}
		t.Logf("Switchover complete: store at block %d", getStoredBlock(t, store))

		// After switchover: proxy replaces "finalized" with the Espresso finalized block
		// number from the store instead of forwarding to the full node unchanged.
		espressoFinalizedBlock := getBlockByTag(t, proxyURL, espressoTag)
		storeBlock := getStoredBlock(t, store)
		// The store may advance between the RPC call and the read, so the proxy-returned
		// block will be at or just below the current store value.
		require.True(t, espressoFinalizedBlock >= storeBlock,
			"proxy should return Espresso finalized block (%d) at (%d)",
			espressoFinalizedBlock, storeBlock)
		t.Logf("Confirmed: after switchover, proxy returns Espresso finalized block %d (store at %d)",
			espressoFinalizedBlock, storeBlock)
	})
}

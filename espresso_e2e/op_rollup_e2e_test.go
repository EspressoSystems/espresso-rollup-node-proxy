package espresso_e2e

import (
	"context"
	"fmt"
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
	waitForRollupServicesReady(t)

	espressoStore := newTestStore(t, "espresso-state", 1)
	err := espressoStore.Update(1, 1)
	require.NoError(t, err)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxyURL, shutdownProxy := startTestProxy(ctx, t, opGethFullNode, espressoStore, espressoTag)
	defer shutdownProxy()

	t.Run("basic proxy advances", func(t *testing.T) {
		t.Log("Starting OP Verifier")
		v := startVerifier(ctx, t, newDefaultLogger(), espressoStore)
		defer v.Stop()
		const targetBlockNum = uint64(10)

		t.Log("Waiting for block 10 to be produced on OP Geth full node")
		pollUntil(t, 2*time.Minute, "block 10 not produced within timeout", func() bool {
			result := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{"0xa", false}))
			return string(result) != "null"
		})

		t.Log("Waiting for OP verifer to update espresso store past block 10")
		pollUntil(t, 1*time.Minute, "OP verifier did not reach block 10 within timeout", func() bool {
			return getStoredBlock(t, espressoStore) >= targetBlockNum
		})

		verifiedBlock := getStoredBlock(t, espressoStore)
		t.Logf("Espresso store at block %d", verifiedBlock)

		proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", verifiedBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult))
		t.Log("Proxy espresso tag response matches direct OP geth full node response")
	})

	t.Run("rpc compatibility", func(t *testing.T) {
		t.Log("Starting OP Verifier")
		v := startVerifier(ctx, t, newDefaultLogger(), espressoStore)
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
				directParams := replaceTag(proxyParams, espressoTag, blockHex)
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
					directBatch[i].params = replaceTag(e.params, espressoTag, blockHex)
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
		v := startVerifier(ctx, t, newDefaultLogger(), espressoStore)
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

		proxyURL, shutdownProxy := startTestProxy(ctx, t, opGethFullNode, initialStore, espressoTag)

		// Now we start the verifier and check if it starts with finalizedL2Block and initialHotshotHeight, and that it advances the store past block 10.
		verifier := startVerifier(ctx, t, log.NewLogger(firstCapturer), initialStore)
		requireLogAttrs(t, firstCapturer, "Starting OP Verifier", map[string]uint64{
			"start block number":               finalizedL2Block,
			"starting fallback_hotshot_height": initialHotshotHeight,
		})

		t.Log("Waiting for OP verifer to update espresso store past block 10")
		pollUntil(t, 3*time.Minute, "OP verifier did not reach block 10 within timeout", func() bool {
			return getStoredBlock(t, initialStore) >= finalizedL2Block+targetBlockNum
		})

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
		shutdownProxy()
		t.Log("proxy and verifier stopped")

		// Now that proxy has advanced to a higher block number and hotshot height,
		// we will restart the proxy with with the same state file, and assert it resumes from the persisted state correctly.
		newStore, err := espressostore.NewEspressoStore(initialStateFile, initialHotshotHeight)
		require.NoError(t, err)

		proxyURL, shutdownProxy = startTestProxy(ctx, t, opGethFullNode, newStore, espressoTag)
		defer shutdownProxy()

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
		hotshotHeight := uint64(0)

		// Switch to fallback batcher so there is no espresso state.
		t.Log("Stopping espresso batcher and activating fallback batcher")
		dockerComposeStop(t, rollupWorkingDir, "op-batcher")
		switchBatcher(t)
		dockerComposeStart(t, rollupWorkingDir, []string{"fallback"}, "op-batcher-fallback")

		// Cleanup: if the test fails while in fallback mode, restore espresso batcher.
		fallbackMode := true
		defer func() {
			if fallbackMode {
				dockerComposeStop(t, rollupWorkingDir, "op-batcher-fallback")
				switchBatcher(t)
				dockerComposeStart(t, rollupWorkingDir, nil, "op-batcher")
			}
		}()

		// Create an empty store and proxy.
		store := newTestStore(t, "switchover-state", hotshotHeight)
		require.Equal(t, uint64(0), getStoredBlock(t, store), "store should start with L2BlockNumber=0")

		t.Log("Starting proxy with empty store, fallback batcher active")
		proxyURL, shutdownProxy := startTestProxy(ctx, t, opGethFullNode, store, espressoTag)
		defer shutdownProxy()

		// Espresso tag should error because the store has no verified state.
		resp := jsonRPCCallRaw(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		require.True(t, resp.Error != nil && string(resp.Error) != "null",
			"should return a JSON-RPC error when store has no verified state, got result: %s", string(resp.Result))

		// Non-espresso requests should still work (forwarded to the OP geth full node).
		resp = jsonRPCCallRaw(t, proxyURL, "eth_blockNumber", nil)
		require.True(t, resp.Error == nil || string(resp.Error) == "null",
			"should not return a JSON-RPC error for non-espresso tag requests, got error: %s", string(resp.Error))
		require.NotNil(t, resp.Result, "should return a result for eth_blockNumber even when store is empty")
		t.Log("Confirmed: espresso tag errors and non-espresso requests work with fallback batcher")

		//  Switch to espresso batcher.
		t.Log("Stopping fallback batcher and activating espresso batcher")
		dockerComposeStop(t, rollupWorkingDir, "op-batcher-fallback")
		switchBatcher(t)
		dockerComposeStart(t, rollupWorkingDir, nil, "op-batcher")
		fallbackMode = false

		// Start the verifier — it reads FallbackHotshotHeight from the store
		// (captured before the switch) and immediately picks up new espresso batches.
		t.Log("Starting verifier to sync from Espresso")
		v := startVerifier(ctx, t, newDefaultLogger(), store)
		defer v.Stop()

		t.Log("Waiting for espresso finalized block to exceed ethereum finalized block")
		var ethFinalizedBlock uint64
		pollUntil(t, 3*time.Minute, "verifier did not advance past ethereum finalized block within timeout", func() bool {
			ethFinalizedBlock = getBlockByTag(t, opGethFullNode, "finalized")
			return ethFinalizedBlock > 0 && getStoredBlock(t, store) > ethFinalizedBlock
		})
		t.Logf("Switchover complete: store at block %d (ethereum finalized at %d)", getStoredBlock(t, store), ethFinalizedBlock)

		// Post-switchover: espresso tag should resolve.
		resp = jsonRPCCallRaw(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		require.True(t, resp.Error == nil || string(resp.Error) == "null",
			"should not return a JSON-RPC error for espresso tag after switchover, got error: %s", string(resp.Error))
		require.NotNil(t, resp.Result, "should return a result for espresso tag after switchover")
		t.Log("Confirmed: espresso tag works after switchover")
	})

	t.Run("switchover with finalized tag", func(t *testing.T) {
		espressoTag := "finalized"

		hotshotHeight := uint64(0)

		t.Log("Stopping espresso batcher and activating fallback batcher")
		dockerComposeStop(t, rollupWorkingDir, "op-batcher")
		switchBatcher(t)
		dockerComposeStart(t, rollupWorkingDir, []string{"fallback"}, "op-batcher-fallback")

		fallbackMode := true
		defer func() {
			if fallbackMode {
				dockerComposeStop(t, rollupWorkingDir, "op-batcher-fallback")
				switchBatcher(t)
				dockerComposeStart(t, rollupWorkingDir, nil, "op-batcher")
			}
		}()

		store := newTestStore(t, "switchover-finalized-state", hotshotHeight)
		require.Equal(t, uint64(0), getStoredBlock(t, store), "store should start with L2BlockNumber=0")

		t.Log("Starting proxy with finalized tag, empty store, fallback batcher active")
		proxyURL, shutdownProxy := startTestProxy(ctx, t, opGethFullNode, store, espressoTag)
		defer shutdownProxy()

		// "finalized" is a valid Ethereum tag so the full node handles it even
		// when the store is empty (request passes through unchanged).
		resp := jsonRPCCallRaw(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		require.True(t, resp.Error == nil || string(resp.Error) == "null",
			"finalized tag should not error with empty store, got error: %s", string(resp.Error))
		require.NotNil(t, resp.Result, "should return a result for finalized tag even with empty store")
		t.Log("Confirmed: finalized tag does not error with empty store (forwarded to full node)")

		// Before switchover: proxy forwards "finalized" to full node unchanged
		// so it should return the same result as calling full node directly.
		proxyResp := jsonRPCCallRaw(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResp := jsonRPCCallRaw(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{"finalized", false}))
		requireJSONRPCEqual(t, directResp, proxyResp, "eth_getBlockByNumber(finalized)")
		t.Log("Confirmed: before switchover, proxy returns same finalized block as full node")

		//  Switch to espresso batcher.
		t.Log("Stopping fallback batcher and activating espresso batcher")
		dockerComposeStop(t, rollupWorkingDir, "op-batcher-fallback")
		switchBatcher(t)
		dockerComposeStart(t, rollupWorkingDir, nil, "op-batcher")
		fallbackMode = false

		t.Log("Starting verifier to sync from Espresso")
		v := startVerifier(ctx, t, newDefaultLogger(), store)
		defer v.Stop()

		t.Log("Waiting for espresso finalized block to exceed ethereum finalized block")
		var ethFinalizedBlock uint64
		pollUntil(t, 3*time.Minute, "verifier did not advance past ethereum finalized block within timeout", func() bool {
			ethFinalizedBlock = getBlockByTag(t, opGethFullNode, "finalized")
			return ethFinalizedBlock > 0 && getStoredBlock(t, store) > ethFinalizedBlock
		})
		t.Logf("Switchover complete: store at block %d (ethereum finalized at %d)", getStoredBlock(t, store), ethFinalizedBlock)

		// After switchover: proxy replaces "finalized" with the Espresso finalized block
		// number from the store instead of forwarding to the full node unchanged.
		espressoFinalizedBlock := getBlockByTag(t, proxyURL, espressoTag)
		storeBlock := getStoredBlock(t, store)
		require.True(t, espressoFinalizedBlock >= storeBlock,
			"proxy should return Espresso finalized block (%d) at (%d)",
			espressoFinalizedBlock, storeBlock)
		t.Logf("Confirmed: after switchover, proxy returns Espresso finalized block %d (store at %d)",
			espressoFinalizedBlock, storeBlock)
	})

	t.Run("fallback to ethereum finality when espresso stops", func(t *testing.T) {
		store := newTestStore(t, "fallback-state", 1)
		err := store.Update(1, 1)
		require.NoError(t, err)

		proxyURL, shutdownProxy := startTestProxy(ctx, t, opGethFullNode, store, "finalized")
		defer shutdownProxy()

		capturer := &logCapturer{}
		v := startVerifier(ctx, t, log.NewLogger(capturer), store)
		defer v.Stop()

		t.Log("Waiting for espresso verifier to advance past block 5")
		pollUntil(t, 3*time.Minute, "espresso verifier did not advance past block 5 within timeout", func() bool {
			return getStoredBlock(t, store) >= 5
		})
		preStopBlock := getStoredBlock(t, store)
		t.Logf("Espresso advanced to block %d, stopping espresso batcher", preStopBlock)

		dockerComposeStop(t, rollupWorkingDir, "op-batcher")

		t.Log("Switching BatchAuthenticator to activate fallback batcher")
		switchBatcher(t)
		defer func() {
			switchBatcher(t)
			dockerComposeStart(t, rollupWorkingDir, nil, "op-batcher")
		}()

		t.Log("Starting fallback batcher (espresso disabled)")
		dockerComposeStart(t, rollupWorkingDir, []string{"fallback"}, "op-batcher-fallback")
		defer dockerComposeStop(t, rollupWorkingDir, "op-batcher-fallback")

		t.Log("Waiting for L2 full node to finalize blocks beyond the pre-stop espresso block")
		var ethFinalized uint64
		pollUntil(t, 3*time.Minute, "L2 full node did not finalize past pre-stop block within timeout", func() bool {
			ethFinalized = getBlockByTag(t, opGethFullNode, "finalized")
			if ethFinalized > preStopBlock {
				t.Logf("L2 finalized block %d is past pre-stop espresso block %d", ethFinalized, preStopBlock)
				return true
			}
			return false
		})

		t.Log("Waiting for verifier to advance store using ethereum finality")
		pollUntil(t, 3*time.Minute, "verifier did not advance store past pre-stop block via ethereum finality within timeout", func() bool {
			return getStoredBlock(t, store) > ethFinalized
		})
		t.Logf("Store advanced to block %d (was %d before espresso batcher stopped)", getStoredBlock(t, store), preStopBlock)

		requireLogStringAttrs(t, capturer, "ethereum finalized block is ahead of espresso finalized block", map[string]string{})
		t.Log("Confirmed: verifier logged that ethereum finalized block is ahead of espresso finalized block")

		resp := jsonRPCCallRaw(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{"finalized", false}))
		require.True(t, resp.Error == nil || string(resp.Error) == "null",
			"proxy should still return valid blocks for finalized tag, got error: %s", string(resp.Error))
		require.NotNil(t, resp.Result, "proxy should return a result for finalized tag")
		t.Log("Confirmed: proxy still works with finalized tag after espresso batcher stopped")
	})

}

package espresso_e2e

import (
	"context"
	"fmt"
	espressostore "proxy/store"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNitroE2ERollupEspressoProxy(t *testing.T) {
	t.Log("Starting Nitro rollup nodes")
	shutdown := runDockerCompose(nitroWorkingDir)
	defer shutdown()

	t.Log("Waiting for Nitro services to be ready")
	waitForNitroServicesReady(t)

	espressoStore := newTestStore(t, "nitro-espresso-state", 0)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxyURL, shutdownProxy := startTestProxy(ctx, t, nitroFullNodeURL, espressoStore, espressoTag)
	defer shutdownProxy()

	t.Run("basic proxy advances", func(t *testing.T) {
		t.Log("Starting Nitro verifier")
		v := startNitroVerifier(ctx, t, espressoStore)
		defer v.Stop()

		const targetBlock = 20

		t.Log("Waiting for block 10 on Nitro full node")
		pollUntil(t, 3*time.Minute, "block 10 not produced on Nitro full node within timeout", func() bool {
			result := jsonRPCCall(t, nitroFullNodeURL, "eth_getBlockByNumber", jsonMarshal(t, []any{"0xa", false}))
			return string(result) != "null"
		})

		t.Log("Waiting for Nitro verifier to advance store past block 10")
		pollUntil(t, 2*time.Minute, "Nitro verifier did not reach block 10 within timeout", func() bool {
			return getStoredBlock(t, espressoStore) >= targetBlock
		})

		verifiedBlock := getStoredBlock(t, espressoStore)
		t.Logf("Espresso store at block %d", verifiedBlock)

		proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult := jsonRPCCall(t, nitroFullNodeURL, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", verifiedBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult))
		t.Log("Proxy espresso tag response matches direct Nitro full node response")
	})

	t.Run("rpc compatibility", func(t *testing.T) {
		t.Log("Starting Nitro Verifier")
		v := startNitroVerifier(ctx, t, espressoStore)
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
				directResp := jsonRPCCallRaw(t, nitroFullNodeURL, tc.method, jsonMarshal(t, tc.params))
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
				directResp := jsonRPCCallRaw(t, nitroFullNodeURL, tc.method, directParams)
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
			directResps := jsonRPCBatchCallRaw(t, nitroFullNodeURL, directBatch)

			for i, entry := range proxyBatch {
				requireJSONRPCEqual(t, directResps[i], proxyResps[i], entry.method)
			}
		})
	})

	t.Run("proxy restart resumes from persisted state", func(t *testing.T) {
		const initialHotshotHeight = uint64(1)
		const targetBlockNum = uint64(10)

		// Wait for a finalized block to use as the initial store anchor.
		pollUntil(t, 3*time.Minute, "Nitro full node did not produce a finalized block", func() bool {
			_, ok := tryGetBlockByTag(t, nitroFullNodeURL, "finalized")
			return ok
		})
		finalizedL2Block, _ := tryGetBlockByTag(t, nitroFullNodeURL, "finalized")

		initialStateFile := t.TempDir() + "/initial-proxy-state.json"
		initialStore, err := espressostore.NewEspressoStore(initialStateFile, initialHotshotHeight)
		require.NoError(t, err)
		_, err = initialStore.UpdateIfGreater(finalizedL2Block, initialHotshotHeight)
		require.NoError(t, err)

		proxyURL2, shutdownProxy2 := startTestProxy(ctx, t, nitroFullNodeURL, initialStore, espressoTag)

		firstLogger, firstCapturer := newCapturingLogger()
		nitroVerifier1 := startNitroVerifierWithLogger(ctx, t, firstLogger, initialStore, nitroFullNodeFeedURL)
		requireLogAttrs(t, firstCapturer, "Starting Nitro Verifier", map[string]uint64{
			"start_block_number":   finalizedL2Block,
			"start_hotshot_height": initialHotshotHeight,
		})

		t.Log("Waiting for Nitro verifier to advance store past finalized+10")
		pollUntil(t, 3*time.Minute, "Nitro verifier did not advance 10 blocks within timeout", func() bool {
			return getStoredBlock(t, initialStore) >= finalizedL2Block+targetBlockNum
		})
		nitroVerifier1.Stop()

		preRestartBlock := getStoredBlock(t, initialStore)
		preRestartHotshotHeight := getStoredHotshotHeight(t, initialStore)
		t.Logf("Espresso store at block %d, hotshot height %d before restart", preRestartBlock, preRestartHotshotHeight)

		proxyResult := jsonRPCCall(t, proxyURL2, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult := jsonRPCCall(t, nitroFullNodeURL, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", preRestartBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult), "espresso tag should resolve to preRestartBlock before restart")

		require.GreaterOrEqual(t, preRestartHotshotHeight, initialHotshotHeight, "store did not advance past initial hotshot height")
		require.Greater(t, preRestartBlock, finalizedL2Block, "store did not advance past finalized block")
		shutdownProxy2()
		t.Log("proxy and verifier stopped")

		// Restart proxy with the same state file and verify it resumes from persisted state.
		newStore, err := espressostore.NewEspressoStore(initialStateFile, initialHotshotHeight)
		require.NoError(t, err)

		proxyURL2, shutdownProxy2 = startTestProxy(ctx, t, nitroFullNodeURL, newStore, espressoTag)
		defer shutdownProxy2()

		resumedBlock := getStoredBlock(t, newStore)
		require.Equal(t, preRestartBlock, resumedBlock, "new store should resume from persisted block")

		proxyResult = jsonRPCCall(t, proxyURL2, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult = jsonRPCCall(t, nitroFullNodeURL, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", preRestartBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult), "espresso tag should resolve to preRestartBlock after restart")

		secondLogger, secondCapturer := newCapturingLogger()
		nitroVerifier2 := startNitroVerifierWithLogger(ctx, t, secondLogger, newStore, nitroFullNodeFeedURL)
		defer nitroVerifier2.Stop()
		requireLogAttrs(t, secondCapturer, "Starting Nitro Verifier", map[string]uint64{
			"start_block_number":   preRestartBlock,
			"start_hotshot_height": preRestartHotshotHeight,
		})
		t.Log("Verified verifier and proxy resumed with correct block number and hotshot height after restart")
	})
}

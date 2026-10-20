package espresso_e2e

import (
	"context"
	"fmt"
	espressostore "github.com/EspressoSystems/espresso-rollup-node-proxy/store"
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

	espressoStore := newTestStore(t, "nitro-espresso-state", 1)

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
		runProxyRPCCompatibility(t, proxyURL, nitroFullNodeURL, espressoStore, v.Stop)
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

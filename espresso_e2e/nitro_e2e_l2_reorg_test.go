package espresso_e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNitroE2EL2Reorg(t *testing.T) {
	t.Log("Starting Nitro rollup nodes with reorg-capable L1")
	shutdown := runDockerComposeFile(nitroWorkingDir, "docker-compose.reorg.yml", nil)
	defer shutdown()

	t.Log("Waiting for Nitro services to be ready")
	waitForNitroServicesReady(t)

	espressoStore := newTestStore(t, "nitro-espresso-state-seq-reorg", 1, nitroNamespace)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxyURL, shutdownProxy := startTestProxy(ctx, t, nitroFullNodeURL, espressoStore, espressoTag)
	defer shutdownProxy()

	t.Log("Starting Nitro verifier")
	v := startNitroVerifier(ctx, t, espressoStore)
	defer v.Stop()

	t.Run("proxy does not go backwards on sequencer restart with wiped state", func(t *testing.T) {
		// Wait until batch poster has finalized some data
		const reorgBlocks = uint64(3)
		pollUntil(t, 5*time.Minute, "sequencer did not build enough finalized + unfinalized headroom", func() bool {
			latest := getBlockByTag(t, nitroSeqURL, "latest")
			finalized, ok := tryGetBlockByTag(t, nitroSeqURL, "finalized")
			return ok && finalized > 20 && latest >= finalized+reorgBlocks
		})

		currentBlock := getBlockByTag(t, nitroSeqURL, "latest")
		finalized, _ := tryGetBlockByTag(t, nitroSeqURL, "finalized")
		blockBeforeReorg := getStoredBlock(t, espressoStore)
		t.Logf("Sequencer at block %d (finalized %d), proxy verified at block %d", currentBlock, finalized, blockBeforeReorg)

		// Only capture hashes above the finalized boundary — blocks at or below it are
		// re-derived deterministically from L1 on restart and will be identical.
		preReorgHashes := captureBlockHashes(t, "before nitro reorg", finalized+1, currentBlock, nitroSeqURL)

		// Stop the sequencer and wipe its local chain DB.
		t.Log("Stopping and removing sequencer container")
		dockerComposeFileRm(t, nitroWorkingDir, "docker-compose.reorg.yml", "sequencer")
		t.Log("Removing sequencer chain volume")
		removeDockerVolume(t, "nitro_nitro-sequencer")

		t.Log("Restarting sequencer")
		dockerComposeFileUp(t, nitroWorkingDir, "docker-compose.reorg.yml", "sequencer")
		waitForHTTPReady(t, nitroSeqURL, 5*time.Minute)

		// Check the hash at finalized+1 immediately after restart
		// Since volume was cleared, new blocks after finalized will differ
		reorgCheckBlock := finalized + 1
		pollUntil(t, 1*time.Minute, fmt.Sprintf("sequencer did not rebuild block %d after restart", reorgCheckBlock), func() bool {
			latest := getBlockByTag(t, nitroSeqURL, "latest")
			return latest >= reorgCheckBlock
		})
		reorgBlockJSON := jsonRPCCall(t, nitroSeqURL, "eth_getBlockByNumber",
			jsonMarshal(t, []any{fmt.Sprintf("0x%x", reorgCheckBlock), false}))
		var reorgBlock struct {
			Hash string `json:"hash"`
		}
		require.NoError(t, json.Unmarshal(reorgBlockJSON, &reorgBlock))
		require.NotEqual(t, preReorgHashes[reorgCheckBlock], reorgBlock.Hash,
			"expected hash change at block %d (finalized+1) immediately after sequencer restart", reorgCheckBlock)
		t.Logf("Sequencer reorg confirmed: block %d hash changed from %s to %s",
			reorgCheckBlock, preReorgHashes[reorgCheckBlock], reorgBlock.Hash)

		// Wait for the sequencer to rebuild past the original tip.
		t.Logf("Waiting for sequencer to reach block %d again", currentBlock)
		pollUntil(t, 3*time.Minute, fmt.Sprintf("sequencer did not recover to block %d", currentBlock), func() bool {
			return getBlockByTag(t, nitroSeqURL, "latest") >= currentBlock
		})

		// Monitor the proxy for the full recovery window — it must never go backwards.
		previous := monitorStoredBlockProgress(t, espressoStore, blockBeforeReorg, 4*time.Minute, nitroFullNodeURL, func(current uint64) bool {
			return current > currentBlock+2
		})

		require.Greater(t, previous, currentBlock+2,
			"proxy did not advance after reorg past %d within timeout (stuck at %d)", currentBlock, previous)
		t.Logf("Proxy at L2 block %d after sequencer reorg, never moved backwards", previous)

		// Verify proxy and sequencer store the same data after reorg
		currentBlockHex := fmt.Sprintf("0x%x", currentBlock)
		seqBlockJSON := jsonRPCCall(t, nitroSeqURL, "eth_getBlockByNumber", jsonMarshal(t, []any{currentBlockHex, false}))
		proxyBlockJSON := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{currentBlockHex, false}))
		require.JSONEq(t, string(seqBlockJSON), string(proxyBlockJSON),
			"proxy and sequencer should serve the same block at %d after reorg", currentBlock)

		// Make sure what was finalized on espresso and l1 was the data sent before the reorg.
		// With espresso we finalize the first valid message seen
		t.Log("Verifying Espresso enforced canonical chain, pre reorg hashes must be what is finalized on l1")
		for blockNum, expectedHash := range preReorgHashes {
			result := jsonRPCCall(t, nitroSeqURL, "eth_getBlockByNumber",
				jsonMarshal(t, []any{fmt.Sprintf("0x%x", blockNum), false}))
			var b struct {
				Hash string `json:"hash"`
			}
			require.NoError(t, json.Unmarshal(result, &b))
			require.Equal(t, expectedHash, b.Hash,
				"block %d: expected pre-restart hash %s after Espresso correction, got %s",
				blockNum, expectedHash, b.Hash)
		}
		t.Logf("Sequencer hash pre-reorg hashes and confirmed for %d blocks above finalized boundary", len(preReorgHashes))
	})
}

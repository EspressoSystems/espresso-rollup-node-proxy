package espresso_e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNitroE2ESequencerReorg tests that the proxy never serves a block that goes
// backwards when the Nitro sequencer is restarted with a wiped local chain database.
//
// Nitro's APIBackend.SetHead is panic("not implemented"), so we cannot use
// debug_setHead to trigger a reorg. Instead we stop the sequencer, remove its
// chain-data volume (nitro_nitro-sequencer), and restart it. The sequencer
// re-initialises from the last L1 batch and rebuilds subsequent blocks with fresh
// timestamps, producing different hashes — a real sequencer reorg. The proxy must
// never decrease its verified block number throughout this process.
func TestNitroE2ESequencerReorg(t *testing.T) {
	t.Log("Starting Nitro rollup nodes with reorg-capable L1")
	shutdown := runDockerComposeFile(nitroWorkingDir, "docker-compose.reorg.yml", nil)
	defer shutdown()

	t.Log("Waiting for Nitro services to be ready")
	waitForNitroServicesReady(t)

	espressoStore := newTestStore(t, "nitro-espresso-state-seq-reorg", 0)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxyURL, shutdownProxy := startTestProxy(ctx, t, nitroFullNodeURL, espressoStore, espressoTag)
	defer shutdownProxy()

	t.Log("Starting Nitro verifier")
	v := startNitroVerifier(ctx, t, espressoStore)
	defer v.Stop()

	t.Run("proxy does not go backwards on sequencer restart with wiped state", func(t *testing.T) {
		// Wait until the sequencer has a safe block above 20 and enough unsafe headroom
		// above safe so the reorg lands in the unsafe range. tryGetBlockByTag is used
		// because Nitro returns an error for "safe" until enough L1 blocks have elapsed.
		const reorgBlocks = uint64(8)
		pollUntil(t, 5*time.Minute, "sequencer did not build enough safe + unsafe headroom", func() bool {
			latest := getBlockByTag(t, nitroSeqURL, "latest")
			safe, ok := tryGetBlockByTag(t, nitroSeqURL, "safe")
			return ok && safe > 20 && latest >= safe+reorgBlocks
		})

		currentBlock := getBlockByTag(t, nitroSeqURL, "latest")
		blockBeforeReorg := getStoredBlock(t, espressoStore)
		t.Logf("Sequencer at block %d, proxy verified at block %d", currentBlock, blockBeforeReorg)

		preReorgHashes := captureNitroSeqBlockHashes(t, blockBeforeReorg, currentBlock)

		// Stop the sequencer and wipe its local chain DB. The config volume (rollup
		// contract addresses, deployed_chain_info.json) is intentionally left intact
		// so the sequencer can re-initialise from L1 state on restart.
		// Docker Compose project name = dirname of working dir = "nitro",
		// so the volume is "nitro_nitro-sequencer".
		// rm -f -s stops and removes the container so the volume is no longer in use.
		t.Log("Stopping and removing sequencer container")
		dockerComposeFileRm(t, nitroWorkingDir, "docker-compose.reorg.yml", "sequencer")
		t.Log("Wiping sequencer chain volume")
		removeDockerVolume(t, "nitro_nitro-sequencer")

		// up -d re-creates the volume and starts the container.
		t.Log("Restarting sequencer")
		dockerComposeFileUp(t, nitroWorkingDir, "docker-compose.reorg.yml", "sequencer")
		waitForHTTPReady(t, nitroSeqURL, 5*time.Minute)

		// Wait for the sequencer to rebuild past the original tip.
		t.Logf("Waiting for sequencer to reach block %d again", currentBlock)
		pollUntil(t, 3*time.Minute, fmt.Sprintf("sequencer did not recover to block %d", currentBlock), func() bool {
			return getBlockByTag(t, nitroSeqURL, "latest") >= currentBlock
		})

		// Confirm the reorg produced different hashes (fresh timestamps guarantee this
		// for any block above the last L1-finalized batch boundary).
		newBlockJSON := jsonRPCCall(t, nitroSeqURL, "eth_getBlockByNumber",
			jsonMarshal(t, []any{fmt.Sprintf("0x%x", currentBlock), false}))
		var newBlock struct {
			Hash string `json:"hash"`
		}
		require.NoError(t, json.Unmarshal(newBlockJSON, &newBlock))
		require.NotEqual(t, preReorgHashes[currentBlock], newBlock.Hash,
			"expected hash change at block %d after sequencer restart", currentBlock)
		t.Logf("Sequencer reorg confirmed at block %d", currentBlock)

		// Monitor the proxy for the full recovery window — it must never go backwards.
		previous := monitorNitroStoredBlockProgress(t, espressoStore, blockBeforeReorg, 3*time.Minute, func(current uint64) bool {
			return current > currentBlock+5
		})

		require.Greater(t, previous, currentBlock+5,
			"proxy did not advance 5 blocks past reorg block %d within timeout (stuck at %d)", currentBlock, previous)
		t.Logf("Proxy at L2 block %d after sequencer reorg, never moved backwards", previous)

		// After the full node catches up, the proxy and sequencer should serve
		// identical data at the reorg boundary.
		currentBlockHex := fmt.Sprintf("0x%x", currentBlock)
		seqBlockJSON := jsonRPCCall(t, nitroSeqURL, "eth_getBlockByNumber", jsonMarshal(t, []any{currentBlockHex, false}))
		proxyBlockJSON := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{currentBlockHex, false}))
		require.JSONEq(t, string(seqBlockJSON), string(proxyBlockJSON),
			"proxy and sequencer should serve the same block at %d after reorg", currentBlock)
	})
}

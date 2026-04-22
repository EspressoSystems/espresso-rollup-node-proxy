package espresso_e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOPE2EL2Reorg(t *testing.T) {
	t.Log("Starting rollup nodes")
	shutdown := runDockerComposeFile(rollupWorkingDir, "docker-compose.yml")
	defer shutdown()

	// Wait for services to come up
	t.Log("waiting for services to be ready")
	waitForRollupServicesReady(t)

	espressoStore := newTestStore(t, "espresso-state", 1)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxyURL, shutdownProxy := startTestProxy(ctx, t, opGethFullNode, espressoStore, espressoTag)
	defer shutdownProxy()

	t.Log("Starting OP Verifier")
	v := startVerifier(ctx, t, newDefaultLogger(), espressoStore)
	defer v.Stop()

	const reorgBlocks = uint64(8)

	// Wait for the sequencer to have a safe block so there's headroom for the reorg
	pollUntil(t, 3*time.Minute, "sequencer safe block did not become > 15", func() bool {
		return getBlockByTag(t, opGethSeqURL, "safe") > 15
	})

	currentSeqBlock := getBlockByTag(t, opGethSeqURL, "latest")
	blockBeforeReorg := getStoredBlock(t, espressoStore)
	reorgTarget := currentSeqBlock - reorgBlocks
	t.Logf("Sequencer at block %d, proxy verified at block %d", currentSeqBlock, blockBeforeReorg)

	preReorgHashes := captureBlockHashes(t, "before reorg", reorgTarget, currentSeqBlock)

	// Stop sequencer node
	t.Log("Stopping op-node-sequencer")
	dockerComposeStop(t, rollupWorkingDir, "op-node-sequencer")

	// rewind sequencer geth while op sequencer is offline
	t.Logf("Rewinding op-geth-sequencer from block %d to %d", currentSeqBlock, reorgTarget)
	_ = jsonRPCCall(t, opGethSeqURL, "debug_setHead", jsonMarshal(t, []any{fmt.Sprintf("0x%x", reorgTarget)}))

	// start load gen for some non deterministic block building the rewound blocks
	t.Log("Starting load generator to ensure non-empty blocks")
	stopLoad := startLoadGen(ctx, t, opGethSeqURL)
	defer stopLoad()

	time.Sleep(5 * time.Second)

	// bring back up
	t.Log("Restarting op-node-sequencer")
	dockerComposeStart(t, rollupWorkingDir, nil, true, "op-node-sequencer")

	// Wait for sequencer to rebuild past the original block
	t.Logf("Waiting for sequencer to reach block %d again", currentSeqBlock)
	pollUntil(t, 2*time.Minute, fmt.Sprintf("sequencer did not recover to block %d", currentSeqBlock), func() bool {
		return getBlockByTag(t, opGethSeqURL, "latest") >= currentSeqBlock
	})
	stopLoad()

	captureBlockHashes(t, "after reorg", reorgTarget, currentSeqBlock)

	// Confirm the reorg produced a different hash at the tip
	newSeqBlockJSON := jsonRPCCall(t, opGethSeqURL, "eth_getBlockByNumber",
		jsonMarshal(t, []any{fmt.Sprintf("0x%x", currentSeqBlock), false}))
	var newSeqBlock struct {
		Hash string `json:"hash"`
	}
	require.NoError(t, json.Unmarshal(newSeqBlockJSON, &newSeqBlock))
	require.NotEqual(t, preReorgHashes[currentSeqBlock], newSeqBlock.Hash,
		"expected hash change at block %d after sequencer reorg", currentSeqBlock)
	t.Logf("Sequencer reorg confirmed at block %d", currentSeqBlock)

	previous := monitorStoredBlockProgress(t, espressoStore, blockBeforeReorg, 2*time.Minute, func(current uint64) bool {
		return current > currentSeqBlock
	})

	t.Logf("Proxy at L2 block %d after sequencer reorg, never moved backwards", previous)

	// Espresso enforces the canonical chain, so both the sequencer and proxy should
	// end up serving the original pre-reorg block at currentSeqBlock.
	currentSeqBlockHex := fmt.Sprintf("0x%x", currentSeqBlock)
	seqBlockJSON := jsonRPCCall(t, opGethSeqURL, "eth_getBlockByNumber", jsonMarshal(t, []any{currentSeqBlockHex, false}))
	proxyBlockJSON := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{currentSeqBlockHex, false}))
	var seqBlock struct {
		Hash string `json:"hash"`
	}
	require.NoError(t, json.Unmarshal(seqBlockJSON, &seqBlock))
	require.Equal(t, preReorgHashes[currentSeqBlock], seqBlock.Hash,
		"sequencer block %d should match pre-reorg hash", currentSeqBlock)
	require.Greater(t, previous, currentSeqBlock,
		"proxy did not advance past reorged block %d (stuck at %d)", currentSeqBlock, previous)
	require.JSONEq(t, string(seqBlockJSON), string(proxyBlockJSON),
		"proxy and sequencer should serve the same block at %d", currentSeqBlock)
}

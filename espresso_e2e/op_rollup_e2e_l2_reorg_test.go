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
	shutdown := runDockerComposeFile(opWorkingDir, "docker-compose.yml", []string{"verifier"})
	defer shutdown()

	// Wait for services to come up
	t.Log("waiting for services to be ready")
	waitForRollupServicesReady(t)
	waitForHTTPReady(t, opRethVerifierUrl, 1*time.Minute)

	espressoStore := newTestStore(t, "espresso-state", 1, opNamespace)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxyURL, shutdownProxy := startTestProxy(ctx, t, opRethFullNode, espressoStore, espressoTag)
	defer shutdownProxy()

	t.Log("Starting OP Verifier")
	v := startOpVerifier(ctx, t, newDefaultLogger(), espressoStore)
	defer v.Stop()

	const reorgBlocks = uint64(8)

	// Wait until latest - safe >= reorgBlocks so the reorg target stays above the safe block.
	pollUntil(t, 3*time.Minute, fmt.Sprintf("sequencer did not build enough unsafe headroom for a %d-block reorg", reorgBlocks), func() bool {
		latest := getBlockByTag(t, opRethSeqURL, "latest")
		safe := getBlockByTag(t, opRethSeqURL, "safe")
		return safe > 20 && latest >= safe+reorgBlocks
	})

	currentSeqBlock := getBlockByTag(t, opRethSeqURL, "latest")
	safeBlock := getBlockByTag(t, opRethSeqURL, "safe")
	blockBeforeReorg := getStoredBlock(t, espressoStore)
	reorgTarget := currentSeqBlock - reorgBlocks
	t.Logf("Sequencer at block %d (safe %d), proxy verified at block %d, rewinding to %d", currentSeqBlock, safeBlock, blockBeforeReorg, reorgTarget)

	preReorgHashes := captureBlockHashes(t, "before reorg", reorgTarget, currentSeqBlock, opRethSeqURL)

	// Stop sequencer node
	t.Log("Stopping op-node-sequencer")
	dockerComposeStop(t, opWorkingDir, "op-node-sequencer")

	t.Logf("Rewinding op-reth-sequencer from block %d to %d", currentSeqBlock, reorgTarget)
	rewindSequencer(t, opWorkingDir, reorgTarget)

	// bring the sequencer driver back up; it rebuilds from the rewound head.
	t.Log("Restarting op-node-sequencer")
	dockerComposeStart(t, opWorkingDir, nil, "op-node-sequencer")

	// Wait for sequencer to rebuild past the original block
	t.Logf("Waiting for sequencer to reach block %d again", currentSeqBlock)
	pollUntil(t, 2*time.Minute, fmt.Sprintf("sequencer did not recover to block %d", currentSeqBlock), func() bool {
		return getBlockByTag(t, opRethSeqURL, "latest") >= currentSeqBlock
	})

	captureBlockHashes(t, "after reorg", reorgTarget, currentSeqBlock, opRethSeqURL)

	// Confirm the reorg produced a different hash at the tip
	newSeqBlockJSON := jsonRPCCall(t, opRethSeqURL, "eth_getBlockByNumber",
		jsonMarshal(t, []any{fmt.Sprintf("0x%x", currentSeqBlock), false}))
	var newSeqBlock struct {
		Hash string `json:"hash"`
	}
	require.NoError(t, json.Unmarshal(newSeqBlockJSON, &newSeqBlock))
	require.NotEqual(t, preReorgHashes[currentSeqBlock], newSeqBlock.Hash,
		"expected hash change at block %d after sequencer reorg", currentSeqBlock)
	t.Logf("Sequencer reorg confirmed at block %d", currentSeqBlock)

	previous := monitorStoredBlockProgress(t, espressoStore, blockBeforeReorg, 3*time.Minute, opRethFullNode, func(current uint64) bool {
		return current > currentSeqBlock+5
	})

	require.Greater(t, previous, currentSeqBlock+5,
		"proxy did not advance 5 blocks past reorg block %d within timeout (stuck at %d)", currentSeqBlock, previous)
	t.Logf("Proxy at L2 block %d after sequencer reorg, never moved backwards", previous)

	// Espresso enforces the canonical chain, so both the sequencer and proxy should
	// end up serving the original pre-reorg block at currentSeqBlock.
	currentSeqBlockHex := fmt.Sprintf("0x%x", currentSeqBlock)
	seqBlockJSON := jsonRPCCall(t, opRethSeqURL, "eth_getBlockByNumber", jsonMarshal(t, []any{currentSeqBlockHex, false}))
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

	// Verify the op-geth-verifier node (which derives from L1 without Espresso) also converges
	// to the canonical chain.
	pollUntil(t, 1*time.Minute, fmt.Sprintf("verifier did not reach block %d", currentSeqBlock), func() bool {
		return getBlockByTag(t, opRethVerifierUrl, "latest") >= currentSeqBlock
	})
	verifierBlockJSON := jsonRPCCall(t, opRethVerifierUrl, "eth_getBlockByNumber", jsonMarshal(t, []any{currentSeqBlockHex, false}))
	require.JSONEq(t, string(seqBlockJSON), string(verifierBlockJSON),
		"verifier and sequencer should serve the same block at %d", currentSeqBlock)
}

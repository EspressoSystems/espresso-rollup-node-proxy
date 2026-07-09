package espresso_e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// batcherKeyB is a dev private key used as the rotated Espresso batch signing
// key (anvil account #9).
const batcherKeyB = "2a871d0798f97d79848a013d4936a73bf4cc922c825d33c1cf7073dff6d409c6"

// droppedBatcherLogMsg is the streamer log emitted when a batch signed by a
// batcher that is not the authorized espresso batcher is dropped.
const droppedBatcherLogMsg = "Dropping batch with invalid espresso batcher"

func TestOPE2EBatcherKeyRotation(t *testing.T) {
	t.Log("Starting rollup nodes")
	shutdown := runDockerCompose(opWorkingDir)
	defer shutdown()

	t.Log("waiting for services to be ready")
	waitForRollupServicesReady(t)

	store := newTestStore(t, "batcher-rotation-state", 1)
	_, err := store.UpdateIfGreater(1, 1)
	require.NoError(t, err)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxyURL, shutdownProxy := startTestProxy(ctx, t, opRethFullNode, store, espressoTag)
	defer shutdownProxy()

	logger, capturer := newCapturingLogger()
	v := startOpVerifier(ctx, t, logger, store)
	defer v.Stop()

	// Confirm the chain is advancing normally under the original batcher.
	t.Log("waiting for the L2 chain to finalize under the original batcher")
	pollUntil(t, 3*time.Minute, "chain did not finalize under the original batcher", func() bool {
		return getBlockByTag(t, opRethFullNode, "finalized") >= 10
	})
	t.Logf("chain finalizing normally at block %d", getBlockByTag(t, opRethFullNode, "finalized"))

	// Rotate the Espresso batcher key to B: authorize B on-chain, then restart the
	// batcher so it signs with B before that authorization has finalized on L1.
	keyB, err := crypto.HexToECDSA(batcherKeyB)
	require.NoError(t, err)
	addrB := crypto.PubkeyToAddress(keyB.PublicKey)

	t.Logf("authorizing new espresso batcher %s on-chain", addrB)
	rotationBlock := setEspressoBatcher(t, addrB)

	// Wait for the L1 block carrying the rotation to finalize so the new
	// batcher's authorization is final before it starts signing.
	t.Logf("waiting for L1 block %d containing the rotation to finalize", rotationBlock)
	pollUntil(t, 5*time.Minute, "L1 block containing the batcher rotation did not finalize", func() bool {
		b := getBlockByTag(t, l1GethURL, "finalized")
		t.Logf("finalized %d, rotation %d", b, rotationBlock)
		return getBlockByTag(t, l1GethURL, "finalized") >= rotationBlock
	})

	// The old batcher is still signing with the now-unauthorized key, so wait
	// until the streamer drops one of its batches — proof the rotation has taken
	// effect on the streamer side.
	pollUntil(t, 3*time.Minute, "streamer never dropped a batch from the old batcher after the rotation finalized", func() bool {
		return matchLogStringAttrs(capturer, droppedBatcherLogMsg, map[string]string{"signer": opBatcherAddress})
	})

	t.Log("restarting op-batcher to sign with the new key")
	restartBatcherWithEspressoKey(t, batcherKeyB)
	verified := getStoredBlock(t, store)

	// Once the rotation finalizes the held batches are accepted and the batcher
	// keeps posting, so the chain finalizes past the rotation point. If we did the key rotation
	// incorrectly (eg shutting off first one to early) the batch poster would stall
	pollUntil(t, 3*time.Minute, "verification did not progress after rotation", func() bool {
		return getStoredBlock(t, store) > verified+20
	})

	t.Logf("proxy continued progressing past the rotation point %d, verified %d", verified, getStoredBlock(t, store))
	pollUntil(t, 5*time.Minute, "chain did not finalize past the rotation — batcher key rotation stalled the batcher", func() bool {
		return getBlockByTag(t, opRethFullNode, "finalized") > verified+20
	})
	t.Logf("chain finalized to block %d, past the rotation point %d", getBlockByTag(t, opRethFullNode, "finalized"), verified)

	// Cross-check that all three independently-derived views of the chain agree
	// after the rotation
	verifiedBlock := getStoredBlock(t, store)
	verifiedBlockHex := fmt.Sprintf("0x%x", verifiedBlock)
	t.Logf("cross-checking state consistency at verified block %d", verifiedBlock)

	// The proxy's espresso-tagged view must match the full node at that block.
	requireProxyTagMatchesDirectBlock(t, proxyURL, opRethFullNode, espressoTag)

	// The independent L1-derived verifier must converge to the same block hash.
	pollUntil(t, 2*time.Minute, fmt.Sprintf("verifier did not reach verified block %d", verifiedBlock), func() bool {
		return getBlockByTag(t, opRethVerifierUrl, "latest") >= verifiedBlock
	})
	fullNodeBlockJSON := jsonRPCCall(t, opRethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{verifiedBlockHex, false}))
	verifierBlockJSON := jsonRPCCall(t, opRethVerifierUrl, "eth_getBlockByNumber", jsonMarshal(t, []any{verifiedBlockHex, false}))
	require.JSONEq(t, string(fullNodeBlockJSON), string(verifierBlockJSON),
		"verifier and full node should serve the same block %d after the rotation", verifiedBlock)
	t.Logf("full node, verifier, and proxy all agree on block %d", verifiedBlock)
}

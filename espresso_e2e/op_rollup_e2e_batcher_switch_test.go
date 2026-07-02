package espresso_e2e

import (
	"context"
	"testing"
	"time"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/log/logutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// batcherKeyB is a dev private key used as the rotated Espresso batch signing
// key (anvil account #9).
const batcherKeyB = "2a871d0798f97d79848a013d4936a73bf4cc922c825d33c1cf7073dff6d409c6"

// pendingBatcherLogMsg is the streamer log emitted while a batch is signed by a
// batcher whose on-chain authorization is not yet finalized.
// const pendingBatcherLogMsg = "Batch signed by pending (unfinalized) espresso batcher, awaiting L1 finality"

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
	_, shutdownProxy := startTestProxy(ctx, t, opRethFullNode, store, espressoTag)
	defer shutdownProxy()

	capturer := logutil.NewCaptureLogger(nil)
	v := startOpVerifier(ctx, t, log.NewLogger(capturer), store)
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
	setEspressoBatcher(t, addrB)

	t.Log("restarting op-batcher to sign with the new key")
	restartBatcherWithEspressoKey(t, batcherKeyB)
	verified := getStoredBlock(t, store)

	// The new batcher signs blocks while its authorization is still unfinalized, so
	// the streamer should hold those batches (BatchUndecided) rather than dropping
	// them. Wait for that log instead of a fixed sleep.
	// pollUntil(t, 5*time.Minute, "streamer never reported a pending (unfinalized) batcher after the rotation", func() bool {
	// 	return matchLogStringAttrs(capturer, pendingBatcherLogMsg, map[string]string{})
	// })
	// t.Log("streamer is holding batches from the pending batcher; waiting for the rotation to finalize and verification to resume")

	// Once the rotation finalizes the held batches are accepted and the batcher
	// keeps posting, so the chain finalizes past the rotation point. Without the
	// fix the batcher would stall and finalization would stop advancing.
	pollUntil(t, 3*time.Minute, "verification did not progress after rotation", func() bool {
		return getStoredBlock(t, store) > verified+20
	})

	pollUntil(t, 5*time.Minute, "chain did not finalize past the rotation — batcher key rotation stalled the batcher", func() bool {
		return getBlockByTag(t, opRethFullNode, "finalized") > verified+20
	})
	t.Logf("chain finalized to block %d, past the rotation point %d", getBlockByTag(t, opRethFullNode, "finalized"), verified)
}

package espresso_e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCASNitroE2ERollupEspressoProxy(t *testing.T) {
	ctx := context.Background()

	t.Log("Starting CAS-backed Nitro stack (l1-anvil, espresso-dev-node, sequencer, full-node, tx-generator)")
	shutdown := runDockerCompose(casWorkingDir, "l1-anvil", "espresso-dev-node", "sequencer", "full-node", "tx-generator")
	defer shutdown()

	t.Log("Waiting for base services to be ready")
	waitForHTTPReady(t, casL1URL, 2*time.Minute)
	waitForHTTPReady(t, casEspressoURL+"/v0/status/block-height", 2*time.Minute)
	waitForHTTPReady(t, casSeqURL, 5*time.Minute)
	waitForHTTPReady(t, casFullNodeURL, 5*time.Minute)

	t.Log("Starting CAS container (published image)")
	dockerComposeStart(t, casWorkingDir, nil, "cas")
	waitForCasReady(t, 2*time.Minute)

	t.Log("Starting Nitro poster (DA = CAS calldata)")
	dockerComposeStart(t, casWorkingDir, nil, "poster")

	espressoStore := newTestStore(t, "cas-nitro-espresso-state", 0)

	t.Log("Starting in-process proxy in front of the full node")
	proxyURL, shutdownProxy := startTestProxy(ctx, t, casFullNodeURL, espressoStore, espressoTag)
	defer shutdownProxy()

	t.Log("Starting CAS Nitro verifier")
	v := startCasNitroVerifier(ctx, t, espressoStore, casBridgeAddress)
	defer v.Stop()

	const targetBlock = 40

	t.Log("Waiting for block 10 on the full node")
	pollUntil(t, 3*time.Minute, "block 10 not produced on full node within timeout", func() bool {
		result := jsonRPCCall(t, casFullNodeURL, "eth_getBlockByNumber", jsonMarshal(t, []any{"0xa", false}))
		return string(result) != "null"
	})

	t.Log("Waiting for CAS Nitro verifier to advance store past block 20")
	pollUntil(t, 5*time.Minute, "CAS Nitro verifier did not reach target block within timeout", func() bool {
		return getStoredBlock(t, espressoStore) >= targetBlock
	})

	verifiedBlock := getStoredBlock(t, espressoStore)
	t.Logf("Espresso store at block %d", verifiedBlock)

	proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
	directResult := jsonRPCCall(t, casFullNodeURL, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", verifiedBlock), false}))
	require.JSONEq(t, string(directResult), string(proxyResult))
	t.Log("Proxy espresso tag response matches direct full node response")

	// Verify the proxy is RPC-compatible with the full node (passthrough and
	// espresso-tagged methods). This stops the verifier to freeze the store.
	t.Run("rpc compatibility", func(t *testing.T) {
		runProxyRPCCompatibility(t, proxyURL, casFullNodeURL, espressoStore, v.Stop)
	})
}

package espresso_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNitroE2ERollupEspressoProxyReorg(t *testing.T) {
	t.Log("Starting Nitro rollup nodes with reorg-capable L1")
	shutdown := runDockerComposeFile(nitroWorkingDir, "docker-compose.reorg.yml", nil)
	defer shutdown()

	t.Log("Waiting for Nitro services to be ready")
	waitForNitroServicesReady(t)

	espressoStore := newTestStore(t, "nitro-espresso-state-reorg", 0)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxyURL, shutdownProxy := startTestProxy(ctx, t, nitroFullNodeURL, espressoStore, espressoTag)
	defer shutdownProxy()

	t.Log("Starting Nitro verifier")
	v := startNitroVerifier(ctx, t, espressoStore)
	defer v.Stop()

	t.Run("proxy does not go backwards on L1 reorg", func(t *testing.T) {
		const targetBlock = uint64(10)

		t.Log("Waiting for block 10 on Nitro full node")
		pollUntil(t, 5*time.Minute, "block 10 not produced on Nitro full node within timeout", func() bool {
			result := jsonRPCCall(t, nitroFullNodeURL, "eth_getBlockByNumber", jsonMarshal(t, []any{"0xa", false}))
			return string(result) != "null"
		})

		t.Log("Waiting for Nitro verifier to advance store past block 10")
		pollUntil(t, 2*time.Minute, "Nitro verifier did not reach block 10 within timeout", func() bool {
			return getStoredBlock(t, espressoStore) >= targetBlock
		})

		latestL1Block := getBlockByTag(t, nitroL1URL, "latest")
		t.Logf("L1 latest block before reorg: %d", latestL1Block)

		blockBeforeReorg := getStoredBlock(t, espressoStore)
		t.Logf("Proxy at L2 block %d before triggering reorg", blockBeforeReorg)

		const reorgBlocks = 5
		forkBlock := latestL1Block - reorgBlocks
		t.Logf("Triggering L1 reorg: forking at block %d (depth %d)", forkBlock, reorgBlocks)

		forkBody, err := json.Marshal(map[string]uint64{"blockNum": forkBlock})
		require.NoError(t, err)
		resp, err := http.Post(mockBeaconURL+"/fork", "application/json", bytes.NewReader(forkBody))
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "mock beacon fork request failed with status %d", resp.StatusCode)
		t.Log("L1 reorg triggered successfully")

		t.Log("Monitoring proxy for backwards movement during and after reorg")
		previous := monitorStoredBlockProgress(t, espressoStore, blockBeforeReorg, 90*time.Second, nitroFullNodeURL, func(uint64) bool {
			return false
		})

		require.Greater(t, previous, blockBeforeReorg,
			"proxy did not advance past block %d during monitoring", blockBeforeReorg)
		t.Logf("Proxy at L2 block %d after reorg, block never moved backwards", previous)

		requireProxyTagMatchesDirectBlock(t, proxyURL, nitroFullNodeURL, espressoTag)
		t.Log("Proxy espresso tag matches direct Nitro full node response after L1 reorg")
	})
}

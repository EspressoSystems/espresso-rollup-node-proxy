package espresso_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

func TestOPE2ERollupEspressoProxyReorg(t *testing.T) {
	t.Log("Starting rollup nodes")
	shutdown := runDockerComposeFile(rollupWorkingDir, "docker-compose.reorg.yml")
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
	newDefaultLogger()

	defaultCapturer := &logCapturer{}
	v := startVerifier(ctx, t, log.NewLogger(defaultCapturer), espressoStore)
	defer v.Stop()

	t.Run("proxy does not go backwords in case of l1 reorg", func(t *testing.T) {
		const targetBlockNum = uint64(10)
		t.Log("Waiting for block 10 to be produced on OP Geth full node")
		pollUntil(t, 2*time.Minute, "block 10 not produced within timeout", func() bool {
			result := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{"0xa", false}))
			return string(result) != "null"
		})

		t.Log("Waiting for OP verifer to update espresso store past block 10")
		pollUntil(t, 1*time.Minute, "OP verifier did not reach block 10 within timeout", func() bool {
			return getStoredBlock(t, espressoStore) >= targetBlockNum
		})

		// Get the current L1 block number to use as the reorg point
		latestL1BlockNum := getBlockByTag(t, l1GethURL, "latest")
		t.Logf("L1 latest block before reorg: %d", latestL1BlockNum)

		blockBeforeReorg := getStoredBlock(t, espressoStore)
		t.Logf("Proxy at L2 block %d before triggering reorg", blockBeforeReorg)

		// Trigger the L1 reorg via the mock beacon
		const reorgBlocks = 5
		t.Logf("Triggering L1 reorg at block %d, current l1 block %d", latestL1BlockNum-reorgBlocks, latestL1BlockNum)
		forkBody, err := json.Marshal(map[string]uint64{"blockNum": latestL1BlockNum - reorgBlocks})
		require.NoError(t, err)
		resp, err := http.Post(mockBeaconURL+"/fork", "application/json", bytes.NewReader(forkBody))
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "mock beacon fork request failed with status %d", resp.StatusCode)
		t.Log("L1 reorg triggered successfully")

		t.Log("Monitoring proxy block number for backwards movement during and after reorg")
		previous := monitorStoredBlockProgress(t, espressoStore, blockBeforeReorg, 1*time.Minute, func(uint64) bool {
			return false
		})

		require.Greater(t, previous, blockBeforeReorg,
			"proxy did not advance past block %d during monitoring", blockBeforeReorg)
		t.Logf("Proxy at L2 block %d after reorg, block never moved backwards", previous)

		requireProxyTagMatchesDirectBlock(t, proxyURL, opGethFullNode, espressoTag)
		t.Log("Proxy espresso tag response matches direct OP geth full node response after reorg")
	})

	t.Run("proxy does not advance if full node has incorrect state", func(t *testing.T) {
		const forkFullNodeOffset = uint64(5)
		currentL2 := getBlockByTag(t, opGethFullNode, "latest")
		maliciousBlockNum := currentL2 + forkFullNodeOffset

		// First send malicious block number to engine
		reorgBody, err := json.Marshal(map[string]uint64{"blockNumber": maliciousBlockNum})
		require.NoError(t, err)
		resp, err := http.Post(p2pAttackUrl+"/create-fork-at-block", "application/json", bytes.NewReader(reorgBody))
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "p2p service request failed with status %d", resp.StatusCode)
		t.Logf("Full node fork ready at block %d", maliciousBlockNum)

		// Wait for L2 to reach malicious block
		t.Logf("Waiting for stored block to reach full node malicious block: %d", maliciousBlockNum)
		deadline := time.Now().Add(3 * time.Minute)
		var blockBeforeFork uint64
		for {
			blockBeforeFork = getStoredBlock(t, espressoStore)
			require.True(t, time.Now().Before(deadline), "L2 did not reach block %d within timeout", maliciousBlockNum)
			require.LessOrEqual(t, blockBeforeFork, maliciousBlockNum-1,
				"proxy passed malicious block %d without stopping", maliciousBlockNum)
			t.Logf("Waiting for L2 block %d, currently at %d", maliciousBlockNum-1, blockBeforeFork)
			if blockBeforeFork == maliciousBlockNum-1 {
				break
			}
			time.Sleep(time.Second)
		}
		t.Logf("Proxy at L2 block %d before triggering fork on full node", blockBeforeFork)

		// Wait for both full node and sequencer to produce the malicious block
		maliciousBlockHex := fmt.Sprintf("0x%x", maliciousBlockNum)
		for {
			require.True(t, time.Now().Before(deadline), "full node did not produce block %d within timeout", maliciousBlockNum)
			if getBlockByTag(t, opGethFullNode, "latest") >= maliciousBlockNum &&
				getBlockByTag(t, opGethSeqURL, "latest") >= maliciousBlockNum {
				break
			}
			time.Sleep(time.Second)
		}

		// Ensure full node block hash and sequencer block hash mismatch
		fullNodeBlock := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{maliciousBlockHex, false}))
		seqBlock := jsonRPCCall(t, opGethSeqURL, "eth_getBlockByNumber", jsonMarshal(t, []any{maliciousBlockHex, false}))
		var fullNodeHash, seqHash struct {
			Hash string `json:"hash"`
		}
		require.NoError(t, json.Unmarshal(fullNodeBlock, &fullNodeHash))
		require.NoError(t, json.Unmarshal(seqBlock, &seqHash))
		require.NotEqual(t, fullNodeHash.Hash, seqHash.Hash,
			"expected different hashes at block %d: full node=%s sequencer=%s", maliciousBlockNum, fullNodeHash.Hash, seqHash.Hash)
		t.Logf("Block %d hash differs as expected: full node=%s sequencer=%s", maliciousBlockNum, fullNodeHash.Hash, seqHash.Hash)

		// Make sure we never go backwards
		t.Log("Monitoring proxy block number for backwards movement during and after reorg")
		monitorStoredBlockProgress(t, espressoStore, blockBeforeFork, 3*time.Minute, func(current uint64) bool {
			return current >= blockBeforeFork+5
		})

		// Verify we advanced after full node reorg
		verifiedBlock := getStoredBlock(t, espressoStore)
		require.Greater(t, verifiedBlock, blockBeforeFork,
			"proxy did not advance past block %d after full node reorg resolved", blockBeforeFork)
		t.Logf("Proxy at L2 block %d after full node fork, before was at %d, block never moved backwards", verifiedBlock, blockBeforeFork)

		requireProxyTagMatchesDirectBlock(t, proxyURL, opGethFullNode, espressoTag)
		t.Log("Proxy espresso tag response matches direct OP geth full node response after full node reorg")

		requireLogStringAttrs(t, defaultCapturer, "batch verification failed", map[string]string{
			"error": fmt.Sprintf("batch verification failed for batch number %d: espresso batch does not match full node batch", maliciousBlockNum),
		})
		t.Logf("Succesfully discarded verification of bad block hash")
		// Make sure hashes are now correct at the malicious block as well
		proxyMaliciousBlock := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{maliciousBlockHex, false}))
		seqMaliciousBlock := jsonRPCCall(t, opGethSeqURL, "eth_getBlockByNumber", jsonMarshal(t, []any{maliciousBlockHex, false}))
		require.JSONEq(t, string(seqMaliciousBlock), string(proxyMaliciousBlock),
			"proxy block at %d should match sequencer after full node reorg resolved", maliciousBlockNum)
		t.Logf("Proxy block %d matches sequencer after full node reorg resolved", maliciousBlockNum)
	})
}

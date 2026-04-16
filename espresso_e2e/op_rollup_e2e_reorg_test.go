package espresso_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"proxy/proxy"
	espressostore "proxy/store"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

func waitForStoredBlockAtLeast(t *testing.T, store *espressostore.EspressoStore, target uint64, timeout time.Duration, msg string) uint64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		current := getStoredBlock(t, store)
		if current >= target {
			return current
		}
		require.True(t, time.Now().Before(deadline), "%s: current=%d target=%d", msg, current, target)
		time.Sleep(time.Second)
	}
}

func waitForL1BlockAtLeast(t *testing.T, url string, target uint64, timeout time.Duration, msg string) uint64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		current := getBlockByTag(t, url, "latest")
		if current >= target {
			return current
		}
		require.True(t, time.Now().Before(deadline), "%s: current=%d target=%d", msg, current, target)
		time.Sleep(time.Second)
	}
}

func waitForL2BlockAtLeast(t *testing.T, url string, target uint64, timeout time.Duration, msg string) uint64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	targetHex := fmt.Sprintf("0x%x", target)
	for {
		result := jsonRPCCall(t, url, "eth_getBlockByNumber", jsonMarshal(t, []any{targetHex, false}))
		if string(result) != "null" {
			return target
		}
		require.True(t, time.Now().Before(deadline), "%s: target=%d", msg, target)
		time.Sleep(time.Second)
	}
}

func getCurrentL1BySyncStatus(t *testing.T, url string) uint64 {
	t.Helper()
	result := jsonRPCCall(t, url, "optimism_syncStatus", jsonMarshal(t, []any{}))
	var status struct {
		CurrentL1 struct {
			Number any `json:"number"`
		} `json:"current_l1"`
	}
	require.NoError(t, json.Unmarshal(result, &status))
	switch v := status.CurrentL1.Number.(type) {
	case string:
		base := 10
		value := v
		if strings.HasPrefix(v, "0x") || strings.HasPrefix(v, "0X") {
			base = 16
			value = strings.TrimPrefix(strings.TrimPrefix(v, "0x"), "0X")
		}
		num, err := strconv.ParseUint(value, base, 64)
		require.NoError(t, err)
		return num
	case float64:
		return uint64(v)
	default:
		require.Failf(t, "unexpected current_l1.number type", "type=%T value=%v", status.CurrentL1.Number, status.CurrentL1.Number)
		return 0
	}
}

func waitForCurrentL1AtLeast(t *testing.T, url string, target uint64, timeout time.Duration, msg string) uint64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		current := getCurrentL1BySyncStatus(t, url)
		if current >= target {
			return current
		}
		require.True(t, time.Now().Before(deadline), "%s: current=%d target=%d", msg, current, target)
		time.Sleep(time.Second)
	}
}

func waitForCurrentL1Below(t *testing.T, url string, target uint64, timeout time.Duration, msg string) uint64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		current := getCurrentL1BySyncStatus(t, url)
		if current < target {
			return current
		}
		require.True(t, time.Now().Before(deadline), "%s: current=%d target=%d", msg, current, target)
		time.Sleep(time.Second)
	}
}

func triggerBeaconFork(t *testing.T, blockNum uint64) {
	t.Helper()
	forkBody, err := json.Marshal(map[string]uint64{"blockNum": blockNum})
	require.NoError(t, err)
	resp, err := http.Post(mockBeaconURL+"/fork", "application/json", bytes.NewReader(forkBody))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "mock beacon fork request failed with status %d", resp.StatusCode)
}

func dockerComposeLogs(t *testing.T, service string) string {
	t.Helper()
	cmd := exec.Command("docker", "compose", "-f", "docker-compose.reorg.yml", "logs", "--no-color", service)
	cmd.Dir = rollupWorkingDir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "docker compose logs failed for %s: %s", service, string(out))
	return string(out)
}

func dockerComposeLogsBestEffort(service string) (string, error) {
	cmd := exec.Command("docker", "compose", "-f", "docker-compose.reorg.yml", "logs", "--no-color", service)
	cmd.Dir = rollupWorkingDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func dockerComposeRestartService(t *testing.T, service string) {
	t.Helper()
	cmd := exec.Command("docker", "compose", "-f", "docker-compose.reorg.yml", "restart", service)
	cmd.Dir = rollupWorkingDir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "docker compose restart failed for %s: %s", service, string(out))
}

func requireServiceLogsContain(t *testing.T, service string, timeout time.Duration, substrings ...string) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		logs := dockerComposeLogs(t, service)
		allFound := true
		for _, substring := range substrings {
			if !strings.Contains(logs, substring) {
				allFound = false
				break
			}
		}
		if allFound {
			return logs
		}
		require.True(t, time.Now().Before(deadline), "service %s logs did not contain all substrings %v", service, substrings)
		time.Sleep(time.Second)
	}
}

func dumpComposeLogsOnFailure(t *testing.T, services ...string) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, service := range services {
			logs, err := dockerComposeLogsBestEffort(service)
			if err != nil {
				t.Logf("\n===== BEGIN %s LOGS (failed to fetch cleanly) =====\n%s\nerror: %v\n===== END %s LOGS =====", service, logs, err, service)
				continue
			}
			t.Logf("\n===== BEGIN %s LOGS =====\n%s\n===== END %s LOGS =====", service, logs, service)
		}
	})
}

func requireStoredBlockStalls(t *testing.T, store *espressostore.EspressoStore, stallWindow time.Duration) (start uint64, end uint64) {
	t.Helper()
	start = getStoredBlock(t, store)
	deadline := time.Now().Add(stallWindow)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		end = getStoredBlock(t, store)
		require.LessOrEqual(t, end, start+1,
			"expected verifier store to stall for %s, but it advanced from %d to %d", stallWindow, start, end)
	}
	return start, end
}

func TestOPE2ERollupEspressoProxyReorg(t *testing.T) {
	t.Log("Starting rollup nodes")
	shutdown := runDockerComposeFile(rollupWorkingDir, "docker-compose.reorg.yml")
	defer shutdown()

	// Wait for services to come up
	t.Log("waiting for services to be ready")
	waitForHTTPReady(t, l1GethURL, 1*time.Minute)
	waitForHTTPReady(t, espressoURL+"/v0/status/block-height", 1*time.Minute)
	waitForHTTPReady(t, opGethSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opNodeSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opGethFullNode, 1*time.Minute)
	waitForHTTPReady(t, opNodeFullNode, 1*time.Minute)

	stateFile := t.TempDir() + "/espresso-state.json"
	espressoStore, err := espressostore.NewEspressoStore(stateFile, 1)
	require.NoError(t, err)

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	p := proxy.NewProxy(opGethFullNode, espressoStore, espressoTag)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyURL := "http://" + listener.Addr().String()
	server := &http.Server{Handler: http.HandlerFunc(p.Serve)}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Shutdown(ctx) }()
	t.Logf("proxy listening on %s", proxyURL)

	t.Log("Starting OP Verifier")
	logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stdout, log.LevelInfo, true))
	log.SetDefault(logger)

	defaultCapturer := &logCapturer{}
	v := startVerifier(ctx, t, log.NewLogger(defaultCapturer), espressoStore)
	defer v.Stop()

	t.Run("proxy does not go backwords in case of l1 reorg", func(t *testing.T) {
		const targetBlockNum = uint64(10)
		t.Log("Waiting for block 10 to be produced on OP Geth full node")
		deadline := time.Now().Add(2 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "block 10 not produced within timeout")
			result := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{"0xa", false}))
			if string(result) != "null" {
				break
			}
			time.Sleep(time.Second)
		}

		t.Log("Waiting for OP verifer to update espresso store past block 10")
		deadline = time.Now().Add(1 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "OP verifier did not reach block 10 within timeout")
			if getStoredBlock(t, espressoStore) >= targetBlockNum {
				break
			}
			time.Sleep(time.Second)
		}

		// Get the current L1 block number to use as the reorg point
		latestL1BlockNum := getBlockByTag(t, l1GethURL, "latest")
		t.Logf("L1 latest block before reorg: %d", latestL1BlockNum)

		blockBeforeReorg := getStoredBlock(t, espressoStore)
		t.Logf("Proxy at L2 block %d before triggering reorg", blockBeforeReorg)

		// Trigger the L1 reorg via the mock beacon
		const reorgBlocks = 5
		t.Logf("Triggering L1 reorg at block %d, current l1 block %d", latestL1BlockNum-reorgBlocks, latestL1BlockNum)
		triggerBeaconFork(t, latestL1BlockNum-reorgBlocks)
		t.Log("L1 reorg triggered successfully")

		// Poll for 1 minute asserting the verified L2 block never moves backwards,
		// and that the espresso-tagged block never exceeds the OP geth full nodes latest block.
		t.Log("Monitoring proxy block number for backwards movement during and after reorg")
		previous := blockBeforeReorg
		deadline = time.Now().Add(1 * time.Minute)
		for {
			current := getStoredBlock(t, espressoStore)
			require.GreaterOrEqual(t, current, previous,
				"proxy block moved backwards: was %d, now %d", previous, current)
			if current > previous {
				t.Logf("Proxy advanced to L2 block %d", current)
				previous = current
			}

			// The espresso-tagged block must not be ahead of the OP geth full nodes latest block
			latestFullNodeBlock := getBlockByTag(t, opGethFullNode, "latest")
			require.LessOrEqual(t, current, latestFullNodeBlock,
				"proxy espresso block %d is ahead of OP geth full nodes latest block %d", current, latestFullNodeBlock)

			if time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Second)
		}

		require.Greater(t, previous, blockBeforeReorg,
			"proxy did not advance past block %d during monitoring", blockBeforeReorg)
		t.Logf("Proxy at L2 block %d after reorg, block never moved backwards", previous)

		proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		var proxyBlock struct {
			Number string `json:"number"`
		}
		require.NoError(t, json.Unmarshal(proxyResult, &proxyBlock))
		directResult := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{proxyBlock.Number, false}))
		require.JSONEq(t, string(directResult), string(proxyResult))
		t.Log("Proxy espresso tag response matches direct OP geth full node response after reorg")
	})

	t.Run("large l1 reorg reproduces batcher stall and validator dropping stale batches", func(t *testing.T) {
		const warmupTargetBlock = uint64(25)
		const reorgDepth = uint64(63)
		const stallWindow = 30 * time.Second
		const currentL1Headroom = uint64(170)

		dumpComposeLogsOnFailure(t,
			"op-batcher",
			"op-node-sequencer",
			"op-node-fullnode",
			"op-geth-sequencer",
			"op-geth-fullnode",
			"l1-geth",
			"mock-l1-beacon",
			"espresso-dev-node",
		)

		t.Logf("Waiting for OP full node to produce block %d before verifier warmup", warmupTargetBlock)
		waitForL2BlockAtLeast(t, opGethFullNode, warmupTargetBlock, 3*time.Minute,
			"OP full node did not produce enough blocks before large reorg")

		waitForStoredBlockAtLeast(t, espressoStore, warmupTargetBlock, 3*time.Minute,
			"verifier did not warm up before large reorg")
		beforeCurrentL1 := waitForCurrentL1AtLeast(t, opNodeSeqURL, currentL1Headroom, 5*time.Minute,
			"need enough sequencer CurrentL1 before triggering large reorg")
		latestL1BlockNum := waitForL1BlockAtLeast(t, l1GethURL, beforeCurrentL1+5, 5*time.Minute,
			"need latest L1 to stay ahead of sequencer CurrentL1 before triggering large reorg")
		require.Greater(t, beforeCurrentL1, reorgDepth, "CurrentL1 must be above reorg depth")

		storedBeforeReorg := getStoredBlock(t, espressoStore)
		hotshotBeforeReorg := getStoredHotshotHeight(t, espressoStore)
		forkBlockNum := beforeCurrentL1 - reorgDepth

		l1Finalized := getBlockByTag(t, l1GethURL, "finalized")
		require.Greater(t, forkBlockNum, l1Finalized,
			"fork block %d must be above L1 finalized %d to avoid finality violation", forkBlockNum, l1Finalized)

		t.Logf("Triggering large L1 reorg depth=%d at fork block=%d while latest l1=%d sequencer currentL1=%d l1Finalized=%d verifier l2=%d hotshot=%d",
			reorgDepth, forkBlockNum, latestL1BlockNum, beforeCurrentL1, l1Finalized, storedBeforeReorg, hotshotBeforeReorg)
		triggerBeaconFork(t, forkBlockNum)
		t.Log("Restarting op-node-sequencer to force CurrentL1 to be recomputed against the reorged L1 chain")
		dockerComposeRestartService(t, "op-node-sequencer")
		waitForHTTPReady(t, opNodeSeqURL, 2*time.Minute)
		afterCurrentL1 := waitForCurrentL1Below(t, opNodeSeqURL, beforeCurrentL1, 2*time.Minute,
			"sequencer CurrentL1 did not move backwards after deep L1 reorg")
		t.Logf("Observed sequencer CurrentL1 reversal: before=%d after=%d", beforeCurrentL1, afterCurrentL1)

		requireServiceLogsContain(t, "op-batcher", 2*time.Minute,
			"sequencer currentL1 reversed",
			"Sequencer is out of sync, retrying next tick.")
		requireServiceLogsContain(t, "op-node-fullnode", 2*time.Minute,
			"Dropping past singular batch",
			"dropping past batch with old timestamp")

		stalledFrom, stalledTo := requireStoredBlockStalls(t, espressoStore, stallWindow)
		currentHotshot := getStoredHotshotHeight(t, espressoStore)
		t.Logf("Observed stalled verifier state after large reorg: l2 %d -> %d over %s, hotshot=%d",
			stalledFrom, stalledTo, stallWindow, currentHotshot)

		batcherLogs := dockerComposeLogs(t, "op-batcher")
		t.Logf("Captured op-batcher stall logs (%d bytes)", len(batcherLogs))
		fullnodeLogs := dockerComposeLogs(t, "op-node-fullnode")
		t.Logf("Captured op-node-fullnode drop logs (%d bytes)", len(fullnodeLogs))
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
		previous := blockBeforeFork
		deadline = time.Now().Add(3 * time.Minute)
		for {
			current := getStoredBlock(t, espressoStore)
			require.GreaterOrEqual(t, current, previous,
				"proxy block moved backwards: was %d, now %d", previous, current)
			if current > previous {
				t.Logf("Proxy advanced to L2 block %d", current)
				previous = current
			}

			// The espresso-tagged block must not be ahead of the OP geth full nodes latest block
			latestFullNodeBlock := getBlockByTag(t, opGethFullNode, "latest")
			require.LessOrEqual(t, current, latestFullNodeBlock,
				"proxy espresso block %d is ahead of OP geth full nodes latest block %d", current, latestFullNodeBlock)

			if current >= blockBeforeFork+5 || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Second)
		}

		// Verify we advanced after full node reorg
		verifiedBlock := getStoredBlock(t, espressoStore)
		require.Greater(t, verifiedBlock, blockBeforeFork,
			"proxy did not advance past block %d after full node reorg resolved", blockBeforeFork)
		t.Logf("Proxy at L2 block %d after full node fork, before was at %d, block never moved backwards", verifiedBlock, blockBeforeFork)

		proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		var proxyBlock struct {
			Number string `json:"number"`
		}
		require.NoError(t, json.Unmarshal(proxyResult, &proxyBlock))
		directResult := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{proxyBlock.Number, false}))
		require.JSONEq(t, string(directResult), string(proxyResult))
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

package espresso_e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"proxy/proxy"
	espressostore "proxy/store"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

func TestOPE2EL2Reorg(t *testing.T) {
	t.Log("Starting rollup nodes")
	shutdown := runDockerCompose(rollupWorkingDir)
	defer shutdown()

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
	v := startVerifier(ctx, t, log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stdout, log.LevelInfo, true)), espressoStore)
	defer v.Stop()

	const reorgBlocks = uint64(8)

	// Wait for the sequencer to have a safe block so there's headroom for the reorg
	deadline := time.Now().Add(3 * time.Minute)
	for {
		require.True(t, time.Now().Before(deadline), "sequencer safe block did not become > 0")
		if getBlockByTag(t, opGethSeqURL, "safe") > 15 {
			break
		}
		time.Sleep(time.Second)
	}

	currentSeqBlock := getBlockByTag(t, opGethSeqURL, "latest")
	blockBeforeReorg := getStoredBlock(t, espressoStore)
	reorgTarget := currentSeqBlock - reorgBlocks
	t.Logf("Sequencer at block %d, proxy verified at block %d", currentSeqBlock, blockBeforeReorg)

	captureBlockHashes := func(label string) map[uint64]string {
		hashes := make(map[uint64]string)
		t.Logf("Block hashes %s (blocks %d..%d):", label, reorgTarget, currentSeqBlock)
		for i := reorgTarget; i <= currentSeqBlock; i++ {
			result := jsonRPCCall(t, opGethSeqURL, "eth_getBlockByNumber",
				jsonMarshal(t, []any{fmt.Sprintf("0x%x", i), false}))
			var block struct {
				Hash string `json:"hash"`
			}
			if err := json.Unmarshal(result, &block); err != nil || block.Hash == "" {
				t.Logf("  block %d: <not found>", i)
			} else {
				t.Logf("  block %d: %s", i, block.Hash)
				hashes[i] = block.Hash
			}
		}
		return hashes
	}

	preReorgHashes := captureBlockHashes("before reorg")

	// Stop sequencer node, rewind geth, restart
	t.Log("Stopping op-node-sequencer")
	dockerComposeExec(t, rollupWorkingDir, "docker-compose.yml", "stop", "op-node-sequencer")

	t.Logf("Rewinding op-geth-sequencer from block %d to %d", currentSeqBlock, reorgTarget)
	_ = jsonRPCCall(t, opGethSeqURL, "debug_setHead", jsonMarshal(t, []any{fmt.Sprintf("0x%x", reorgTarget)}))

	t.Log("Starting load generator to ensure non-empty blocks")
	stopLoad := startLoadGen(ctx, t, opGethSeqURL)
	defer stopLoad()

	time.Sleep(5 * time.Second)

	t.Log("Restarting op-node-sequencer")
	dockerComposeExec(t, rollupWorkingDir, "docker-compose.yml", "start", "op-node-sequencer")

	// Wait for sequencer to rebuild past the original block
	t.Logf("Waiting for sequencer to reach block %d again", currentSeqBlock)
	deadline = time.Now().Add(2 * time.Minute)
	for {
		require.True(t, time.Now().Before(deadline), "sequencer did not recover to block %d", currentSeqBlock)
		if getBlockByTag(t, opGethSeqURL, "latest") >= currentSeqBlock {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	stopLoad()

	captureBlockHashes("after reorg")

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

	previous := blockBeforeReorg
	deadline = time.Now().Add(2 * time.Minute)
	for {
		current := getStoredBlock(t, espressoStore)
		require.GreaterOrEqual(t, current, previous,
			"proxy block moved backwards: was %d, now %d", previous, current)
		if current > previous {
			t.Logf("Proxy advanced to L2 block %d", current)
			previous = current
		}
		if previous > currentSeqBlock || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}

	t.Logf("Proxy at L2 block %d after sequencer reorg, never moved backwards", previous)

	// Espresso enforces the canonical chain, so both the sequencer and proxy should
	// end up serving the original pre-reorg block at currentSeqBlock.
	hex := fmt.Sprintf("0x%x", currentSeqBlock)
	seqBlockJSON := jsonRPCCall(t, opGethSeqURL, "eth_getBlockByNumber", jsonMarshal(t, []any{hex, false}))
	proxyBlockJSON := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{hex, false}))
	var seqBlockHash, proxyBlockHash struct {
		Hash string `json:"hash"`
	}
	require.NoError(t, json.Unmarshal(seqBlockJSON, &seqBlockHash))
	require.NoError(t, json.Unmarshal(proxyBlockJSON, &proxyBlockHash))
	require.Equal(t, preReorgHashes[currentSeqBlock], seqBlockHash.Hash,
		"sequencer block %d should match pre-reorg hash", currentSeqBlock)
	require.Equal(t, preReorgHashes[currentSeqBlock], proxyBlockHash.Hash,
		"proxy block %d should match pre-reorg hash", currentSeqBlock)
}

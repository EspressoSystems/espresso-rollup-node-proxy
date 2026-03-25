//go:build e2e

package espresso_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"proxy/proxy"
	espressostore "proxy/store"
	opStreamer "proxy/streamer/op"
	verifier "proxy/verifier/op"
	"testing"
	"time"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

type mockLightClient struct {
	client *espressoClient.Client
	last   uint64
}

func (m *mockLightClient) FinalizedState(_ *bind.CallOpts) (opStreamer.FinalizedState, error) {
	current, err := m.client.FetchLatestBlockHeight(context.Background())
	result := m.last
	if err == nil {
		// Make sure finalized state is back enough blocks
		m.last = max(current-finalizedBlocks, 0)
	}
	return opStreamer.FinalizedState{
		BlockHeight:   result,
		ViewNum:       0,
		BlockCommRoot: big.NewInt(0),
	}, nil
}

const (
	rollupWorkingDir = "./op"
	l1GethURL        = "http://127.0.0.1:8545"
	espressoURL      = "http://127.0.0.1:24000"
	opGethSeqURL     = "http://127.0.0.1:8546"
	opGethFullNode   = "http://127.0.0.1:8555"
	opNodeSeqURL     = "http://127.0.0.1:9545"
	opNodeFullNode   = "http://127.0.0.1:9548"
	mockBeaconURL    = "http://127.0.0.1:5052"
	L2_CHAIN_ID      = 22266222
	espressoTag      = "espresso"
	finalizedBlocks  = 30
)

func startVerifier(ctx context.Context, t *testing.T, logger log.Logger, store *espressostore.EspressoStore) *verifier.OPEspressoBatchVerifier {
	t.Helper()
	l1Client, err := ethclient.DialContext(ctx, l1GethURL)
	if err != nil {
		logger.Crit("failed to create L1 client", "error", err)
	}
	v := verifier.NewOPEspressoBatchVerifier(ctx, logger, store,
		l1Client,
		&mockLightClient{client: espressoClient.NewClient(espressoURL)},
		&verifier.OPEspressoBatchVerifierConfig{
			FullNodeExecutionRPC: opGethFullNode,
			FullNodeConsensusRPC: opNodeFullNode,
			VerificationInterval: time.Second,
			QueryServiceURL:      espressoURL,
			BatcherAddress:       "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		},
	)
	v.Start(ctx)
	return v
}

func TestOPE2ERollupEspressoProxy(t *testing.T) {
	t.Log("Starting rollup nodes")
	shutdown := runDockerCompose(rollupWorkingDir)
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
	espressoStore, err := espressostore.NewEspressoStore(stateFile, 1, 0)
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

	v := startVerifier(ctx, t, logger, espressoStore)
	defer v.Stop()

	t.Run("basic proxy advances", func(t *testing.T) {
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
			if storeBlock(t, espressoStore) >= targetBlockNum {
				break
			}
			time.Sleep(time.Second)
		}

		verifiedBlock := storeBlock(t, espressoStore)
		t.Logf("Espresso store at block %d", verifiedBlock)

		proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", verifiedBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult))
		t.Log("Proxy espresso tag response matches direct OP geth full node response")
	})

	t.Run("proxy does not go backwords in case of l1 reorg", func(t *testing.T) {
		// Wait for L1 to advance 10 l1 blocks
		latestL1BlockNum := getBlockNum(t, l1GethURL)
		const reorgTriggerL1Block = uint64(10)
		t.Logf("Waiting for L1 to reach block %d, currently at %d", latestL1BlockNum+reorgTriggerL1Block, latestL1BlockNum)
		deadline := time.Now().Add(3 * time.Minute)
		for {
			require.True(t, time.Now().Before(deadline), "L1 did not reach block %d within timeout", reorgTriggerL1Block)
			l1Block := getBlockNum(t, l1GethURL)
			if l1Block >= latestL1BlockNum+reorgTriggerL1Block {
				break
			}
			time.Sleep(time.Second)
		}

		// Get the current L1 block number to use as the reorg point
		latestL1BlockNum = getBlockNum(t, l1GethURL)
		t.Logf("L1 latest block before reorg: %d", latestL1BlockNum)

		blockBeforeReorg := storeBlock(t, espressoStore)
		t.Logf("Proxy at L2 block %d before triggering reorg", blockBeforeReorg)

		// Trigger the L1 reorg via the mock beacon
		const reorgBlocks = 3
		t.Logf("Triggering L1 reorg at block %d", latestL1BlockNum-reorgBlocks)
		forkBody, err := json.Marshal(map[string]uint64{"blockNum": latestL1BlockNum - reorgBlocks})
		require.NoError(t, err)
		resp, err := http.Post(mockBeaconURL+"/fork", "application/json", bytes.NewReader(forkBody))
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "mock beacon fork request failed with status %d", resp.StatusCode)
		t.Log("L1 reorg triggered successfully")

		// Poll for 1 minute asserting the verified L2 block never moves backwards,
		// and that the espresso-tagged block never exceeds the OP geth full nodes latest block.
		t.Log("Monitoring proxy block number for backwards movement during and after reorg")
		previous := blockBeforeReorg
		deadline = time.Now().Add(1 * time.Minute)
		for {
			current := storeBlock(t, espressoStore)
			require.GreaterOrEqual(t, current, previous,
				"proxy block moved backwards: was %d, now %d", previous, current)
			if current > previous {
				t.Logf("Proxy advanced to L2 block %d", current)
				previous = current
			}

			// The espresso-tagged block must not be ahead of the OP geth full nodes latest block
			latestFullNodeBlock := getBlockNum(t, opGethFullNode)
			require.LessOrEqual(t, current, latestFullNodeBlock,
				"proxy espresso block %d is ahead of OP geth full nodes latest block %d", current, latestFullNodeBlock)

			if time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Second)
		}

		verifiedBlock := storeBlock(t, espressoStore)
		require.Greater(t, verifiedBlock, blockBeforeReorg,
			"proxy did not advance past block %d after reorg resolved", blockBeforeReorg)
		t.Logf("Proxy at L2 block %d after reorg, block never moved backwards", verifiedBlock)

		proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
		directResult := jsonRPCCall(t, opGethFullNode, "eth_getBlockByNumber", jsonMarshal(t, []any{fmt.Sprintf("0x%x", verifiedBlock), false}))
		require.JSONEq(t, string(directResult), string(proxyResult))
		t.Log("Proxy espresso tag response matches direct OP geth full node response after reorg")
	})
}

package espresso_e2e

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	espressostore "proxy/espresso_store"
	"proxy/proxy"
	streamer "proxy/streamer/op"
	verifier "proxy/verifier/op"
	"testing"
	"time"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

const (
	rollupWorkingDir  = "./rollup"
	l1GethURL         = "http://127.0.0.1:8545"
	espressoURL       = "http://127.0.0.1:24000"
	opGethSeqURL      = "http://127.0.0.1:8546"
	opGethVerifierURL = "http://127.0.0.1:8547"
	opNodeSeqURL      = "http://127.0.0.1:9545"
	opNodeVerifierURL = "http://127.0.0.1:9546"
	L2_CHAIN_ID       = 22266222
)

type ethL1ClientWrapper struct {
	client *ethclient.Client
}

func (c *ethL1ClientWrapper) HeaderHashByNumber(ctx context.Context, number *big.Int) (common.Hash, error) {
	header, err := c.client.HeaderByNumber(ctx, number)
	if err != nil {
		return common.Hash{}, err
	}
	return header.Hash(), nil
}

func TestE2ERollupEspressoProxy(t *testing.T) {
	t.Log("Starting docker compose - setting up rollup containers")
	shutdown := runDockerCompose(rollupWorkingDir)
	defer shutdown()

	// Wait for services to come up
	t.Log("waiting for services to be ready")

	waitForHTTPReady(t, l1GethURL, 1*time.Minute)
	waitForHTTPReady(t, espressoURL+"/v0/status/block-height", 1*time.Minute)
	waitForHTTPReady(t, opGethSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opGethVerifierURL, 1*time.Minute)
	waitForHTTPReady(t, opNodeSeqURL, 1*time.Minute)
	waitForHTTPReady(t, opNodeVerifierURL, 1*time.Minute)

	// Now lets create the proxy
	stateFile := t.TempDir() + "/espresso-state.json"
	espressoStore, err := espressostore.NewEspressoStore(stateFile)
	if err != nil {
		t.Fatalf("failed to create espresso store: %v", err)
	}

	ctx := context.Background()
	t.Log("Starting in-process proxy")
	proxy := proxy.NewProxy(opGethVerifierURL, espressoStore)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyURL := "http://" + listener.Addr().String()
	server := &http.Server{Handler: http.HandlerFunc(proxy.Serve)}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Shutdown(ctx) }()
	t.Logf("proxy listening on %s", proxyURL)
	// Start OP Verifier
	t.Log("Starting OP Verifier")
	logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true))
	log.SetDefault(logger)
	ethL1Client, err := ethclient.DialContext(ctx, l1GethURL)
	require.NoError(t, err)
	l1Client := &ethL1ClientWrapper{client: ethL1Client}
	espClient := espressoClient.NewClient(espressoURL)
	batchPosterAddr := common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")
	s := streamer.NewEspressoStreamer[streamer.EspressoBatch](L2_CHAIN_ID, l1Client, l1Client, espClient, nil, logger, streamer.CreateEspressoBatchUnmarshaler(batchPosterAddr), time.Millisecond, 1, 1)
	// any batch whose L1 origin is below block ~1 billion passes passes finality check
	s.FinalizedL1 = eth.L1BlockRef{Number: 999_999_999}

	streamerCtx, streamerCancel := context.WithCancel(ctx)
	defer streamerCancel()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-streamerCtx.Done():
				return
			case <-ticker.C:
				if err := s.Update(streamerCtx); err != nil {
					logger.Error("streamer update failed", "error", err)
				}
			}
		}
	}()

	v := verifier.NewVerifier(ctx, s, logger, espressoStore, opGethVerifierURL, opNodeVerifierURL, time.Millisecond)
	go func() {
		v.Start(ctx)
	}()

	const targetBlockNum = uint64(10)

	t.Log("Waiting for block 10 to be produced on OP Geth verifier")
	deadline := time.Now().Add(3 * time.Minute)
	for {
		require.True(t, time.Now().Before(deadline), "block 10 not produced within timeout")
		result := jsonRPCCall(t, opGethVerifierURL, "eth_getBlockByNumber", []interface{}{"0xa", false})
		if string(result) != "null" {
			break
		}
		time.Sleep(time.Second)
	}

	t.Log("Waiting for verifier to update espresso store past block 10")
	deadline = time.Now().Add(3 * time.Minute)
	for {
		require.True(t, time.Now().Before(deadline), "verifier did not reach block 10 within timeout")
		if espressoStore.GetBlockNumber() >= targetBlockNum {
			break
		}
		time.Sleep(time.Second)
	}

	verifiedBlock := espressoStore.GetBlockNumber()
	verifiedBlockHex := fmt.Sprintf("0x%x", verifiedBlock)
	t.Logf("Espresso store at block %d (%s)", verifiedBlock, verifiedBlockHex)

	proxyResult := jsonRPCCall(t, proxyURL, "eth_getBlockByNumber", []interface{}{"espresso", false})
	directResult := jsonRPCCall(t, opGethVerifierURL, "eth_getBlockByNumber", []interface{}{verifiedBlockHex, false})
	require.JSONEq(t, string(directResult), string(proxyResult))
	t.Log("Proxy espresso tag response matches direct verifier response")
}

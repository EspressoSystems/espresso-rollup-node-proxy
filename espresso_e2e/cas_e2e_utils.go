package espresso_e2e

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	espressostore "github.com/EspressoSystems/espresso-rollup-node-proxy/store"
	nitroVerifier "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/nitro"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

const (
	casWorkingDir      = "./CAS"
	casEspressoURL     = "http://127.0.0.1:41000"
	casL1URL           = "http://127.0.0.1:8545"
	casSeqURL          = "http://127.0.0.1:8547"
	casFullNodeURL     = "http://127.0.0.1:8549"
	casFullNodeFeedURL = "ws://127.0.0.1:9643"
	casDARPCURL        = "http://127.0.0.1:8000/cas/arb/calldata"

	casNamespace     = uint64(412346)
	casBatchPoster   = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
	casBridgeAddress = "0x8dAF17A20c9DBA35f005b6324F493785D239719d"

	casWorkingDirV3_10    = "./CAS-v3_10"
	casBridgeAddressV3_10 = "0x7CB923362F408dEAD3d274EdC9645eef3753d7B2"
)

func startCasNitroVerifier(ctx context.Context, t *testing.T, store *espressostore.EspressoStore, bridgeAddress string) *nitroVerifier.NitroEspressoBatchVerifier {
	t.Helper()
	v := nitroVerifier.NewNitroEspressoBatchVerifier(ctx, newDefaultLogger(), store,
		&nitroVerifier.NitroEspressoBatchVerifierConfig{
			FeedURL:              casFullNodeFeedURL,
			FullNodeExecutionRPC: casFullNodeURL,
			EthRpc:               casL1URL,
			VerificationInterval: 250 * time.Millisecond,
			QueryServiceURL:      casEspressoURL,
			Namespace:            casNamespace,
			BridgeAddress:        common.HexToAddress(bridgeAddress),
			ValidBatcherAddresses: []nitroVerifier.BatcherAddressConfig{
				{Address: common.HexToAddress(casBatchPoster), From: 0, To: math.MaxUint64},
			},
		})
	require.NotNil(t, v, "failed to create CAS Nitro verifier")
	v.Start(ctx)
	return v
}

func waitForCasReady(t *testing.T, timeout time.Duration) {
	t.Helper()
	const body = `{"jsonrpc":"2.0","method":"daprovider_getSupportedHeaderBytes","params":[],"id":1}`
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Post(casDARPCURL, "application/json", strings.NewReader(body))
		if err == nil {
			_ = resp.Body.Close()
			t.Logf("CAS DA RPC ready at %s (status %d)", casDARPCURL, resp.StatusCode)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("CAS DA RPC at %s did not become ready within %s", casDARPCURL, timeout)
}

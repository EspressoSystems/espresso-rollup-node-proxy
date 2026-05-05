package espresso_e2e

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	proxydURL         = "http://127.0.0.1:8556"
	proxydComposeFile = "docker-compose.proxyd.yml"
)

func getEspressoBlockFromProxyd(t *testing.T, url string) (uint64, bool) {
	t.Helper()
	resp := jsonRPCCallRaw(t, url, "eth_getBlockByNumber", jsonMarshal(t, []any{espressoTag, false}))
	if resp.Error != nil && string(resp.Error) != "null" {
		return 0, false
	}
	if resp.Result == nil || string(resp.Result) == "null" {
		return 0, false
	}
	var block struct {
		Number string `json:"number"`
	}
	if err := json.Unmarshal(resp.Result, &block); err != nil || block.Number == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(block.Number, "0x"), 16, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func TestOPE2EProxyd(t *testing.T) {
	t.Log("Starting OP stack + proxyd")
	shutdown := runDockerComposeFile(rollupWorkingDir, proxydComposeFile, nil)
	defer shutdown()

	t.Log("Waiting for OP stack services to be ready")
	waitForRollupServicesReady(t)

	t.Log("Waiting for proxyd to be ready")
	waitForHTTPReady(t, proxydURL, 2*time.Minute)

	t.Log("Waiting for proxyd espresso tag to start resolving")
	pollUntil(t, 5*time.Minute, "proxyd espresso tag did not start resolving within timeout", func() bool {
		_, ok := getEspressoBlockFromProxyd(t, proxydURL)
		return ok
	})

	t.Run("espresso proxy restart and proxyd only goes forward", func(t *testing.T) {
		initialBlock, _ := getEspressoBlockFromProxyd(t, proxydURL)
		require.Greater(t, initialBlock, uint64(0), "expected initial block from proxyd to be greater than 0")
		t.Logf("Proxyd espresso tag at block %d, shutting down fullnode-proxy-1", initialBlock)

		dockerComposeFileStop(t, rollupWorkingDir, proxydComposeFile, "fullnode-proxy-1")
		t.Log("fullnode-proxy-1 stopped")

		previous := initialBlock
		pollUntil(t, 2*time.Minute, fmt.Sprintf("proxyd espresso tag did not advance 15 blocks past %d with one proxy down", initialBlock), func() bool {
			current, ok := getEspressoBlockFromProxyd(t, proxydURL)
			if ok {
				require.GreaterOrEqual(t, current, previous,
					"espresso tag moved backwards: was %d, now %d", previous, current)
				if current > previous {
					t.Logf("Proxyd espresso tag advanced to block %d", current)
					previous = current
				}
			}
			return previous >= initialBlock+20
		})
		t.Logf("Proxyd espresso tag reached block %d after fullnode-proxy-1 shutdown, tag never moved backwards", previous)

		blockBeforeRestart := previous
		t.Logf("Restarting fullnode-proxy-1, espresso tag was at block %d", blockBeforeRestart)
		dockerComposeFileStart(t, rollupWorkingDir, proxydComposeFile, "fullnode-proxy-1")
		t.Log("fullnode-proxy-1 restarted")

		pollUntil(t, 2*time.Minute, fmt.Sprintf("proxyd espresso tag did not advance 15 blocks past %d after proxy-1 restart", blockBeforeRestart), func() bool {
			current, ok := getEspressoBlockFromProxyd(t, proxydURL)
			if ok {
				require.GreaterOrEqual(t, current, previous,
					"espresso tag moved backwards after proxy-1 restart: was %d, now %d", previous, current)
				if current > previous {
					t.Logf("Proxyd espresso tag advanced to block %d after proxy-1 restart", current)
					previous = current
				}
			}
			return previous >= blockBeforeRestart+20
		})
		t.Logf("Proxyd espresso tag reached block %d after proxy-1 restart, tag never moved backwards", previous)
	})

	t.Run("RPCMethods", func(t *testing.T) {
		userAddr := "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
		hash := "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

		// just verify proxyd returns a valid result and avoid any race conditions for eth_blockNumber
		t.Run("eth_blockNumber", func(t *testing.T) {
			resp := jsonRPCCallRaw(t, proxydURL, "eth_blockNumber", nil)
			require.True(t, resp.Error == nil || string(resp.Error) == "null", "eth_blockNumber returned error: %s", string(resp.Error))
			require.False(t, resp.Result == nil || string(resp.Result) == "null", "eth_blockNumber returned null result")
		})

		methodsWithNoEspressoTag := []struct {
			method string
			params any
		}{
			{"eth_chainId", nil},
			{"eth_syncing", nil},
			{"eth_gasPrice", nil},
			{"eth_maxPriorityFeePerGas", nil},
			{"eth_accounts", nil},
			{"eth_getBlockByHash", []any{hash, false}},
			{"eth_getBlockTransactionCountByHash", []any{hash}},
			{"eth_getUncleCountByBlockHash", []any{hash}},
			{"eth_getTransactionByHash", []any{hash}},
			{"eth_getTransactionReceipt", []any{hash}},
			{"eth_getTransactionByBlockHashAndIndex", []any{hash, "0x0"}},
			{"eth_getUncleByBlockHashAndIndex", []any{hash, "0x0"}},
			{"eth_getHeaderByHash", []any{hash}},
			{"net_version", nil},
			{"net_listening", nil},
			{"net_peerCount", nil},
			{"web3_clientVersion", nil},
			{"web3_sha3", []any{"0x68656c6c6f"}},
		}

		for _, tc := range methodsWithNoEspressoTag {
			t.Run(tc.method, func(t *testing.T) {
				proxyResp := jsonRPCCallRaw(t, proxydURL, tc.method, jsonMarshal(t, tc.params))
				directResp := jsonRPCCallRaw(t, opGethFullNode, tc.method, jsonMarshal(t, tc.params))
				requireJSONRPCEqual(t, directResp, proxyResp, tc.method)
			})
		}

		espressoTagMethods := []struct {
			method string
			params []any
		}{
			{"eth_getBalance", []any{userAddr, espressoTag}},
			{"eth_getCode", []any{userAddr, espressoTag}},
			{"eth_getStorageAt", []any{userAddr, "0x0", espressoTag}},
			{"eth_getTransactionCount", []any{userAddr, espressoTag}},
			{"eth_call", []any{map[string]any{"to": userAddr, "data": "0x"}, espressoTag}},
			{"eth_getBlockByNumber", []any{espressoTag, false}},
			{"eth_getBlockTransactionCountByNumber", []any{espressoTag}},
			{"eth_getUncleCountByBlockNumber", []any{espressoTag}},
			{"eth_getTransactionByBlockNumberAndIndex", []any{espressoTag, "0x0"}},
			{"eth_getUncleByBlockNumberAndIndex", []any{espressoTag, "0x0"}},
			{"eth_getLogs", []any{map[string]any{"fromBlock": espressoTag, "toBlock": espressoTag}}},
			{"eth_feeHistory", []any{"0x4", espressoTag, []any{25, 75}}},
			{"eth_getHeaderByNumber", []any{espressoTag}},
		}

		espressoBlock, ok := getEspressoBlockFromProxyd(t, proxydURL)
		require.True(t, ok, "expected espresso tag to resolve")
		blockHex := fmt.Sprintf("0x%x", espressoBlock)

		for _, tc := range espressoTagMethods {
			t.Run(tc.method, func(t *testing.T) {
				params := replaceTag(jsonMarshal(t, tc.params), espressoTag, blockHex)
				proxyResp := jsonRPCCallRaw(t, proxydURL, tc.method, params)
				directResp := jsonRPCCallRaw(t, opGethFullNode, tc.method, params)
				requireJSONRPCEqual(t, directResp, proxyResp, tc.method)
			})
		}
	})
}

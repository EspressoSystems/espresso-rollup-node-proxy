package espresso_e2e

import (
	"testing"
	"time"
)

const (
	rollupWorkingDir  = "./op"
	l1GethURL         = "http://127.0.0.1:8545"
	espressoURL       = "http://127.0.0.1:24000"
	opGethSeqURL      = "http://127.0.0.1:8546"
	opGethFullNode    = "http://127.0.0.1:8548"
	opGethVerifierURL = "http://127.0.0.1:8547"
	opNodeSeqURL      = "http://127.0.0.1:9545"
	opNodeFullNode    = "http://127.0.0.1:9547"
	opNodeVerifierURL = "http://127.0.0.1:9546"
	L2_CHAIN_ID       = 22266222
)

func TestOPE2ERollupEspressoProxy(t *testing.T) {
	t.Log("Starting rollup nodes")
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
	waitForHTTPReady(t, opGethFullNode, 1*time.Minute)
	waitForHTTPReady(t, opNodeFullNode, 1*time.Minute)

	// wait for 1  min
	time.Sleep(1 * time.Minute)
}

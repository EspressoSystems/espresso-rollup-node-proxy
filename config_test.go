package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	valid := &Config{
		FullNodeExecutionRPC: "http://localhost:8545",
		L1RPC:                "ws://localhost:8546",
		ListenAddr:           ":8080",
		EspressoTag:          "espresso",
		StoreFilePath:        "espresso_store.json",
		LogLevel:             "info",
		OPConfig: OPConfig{
			Enable:                    true,
			FullNodeConsensusRPC:      "http://localhost:9545",
			VerificationInterval:      1 * time.Millisecond,
			QueryServiceURL:           "https://query.espresso.network",
			LightClientAddress:        "0x1234567890abcdef1234567890abcdef12345678",
			BatcherAddress:            "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
			BatchAuthenticatorAddress: "0x1111111111111111111111111111111111111111",
		},
	}
	require.NoError(t, valid.validate())

	opEnabled := &Config{OPConfig: OPConfig{Enable: true}}
	err := opEnabled.validate()
	require.Error(t, err)
	for _, field := range []string{
		"full-node-execution-rpc", "l1-rpc",
		"op.full-node-consensus-rpc", "op.query-service-url",
		"op.light-client-address", "op.batcher-address", "op.batch-authenticator-address",
		"listen-addr", "espresso-tag", "store-file-path",
	} {
		require.Contains(t, err.Error(), field)
	}

	opDisabled := &Config{}
	err = opDisabled.validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "full-node-execution-rpc")
	require.NotContains(t, err.Error(), "op.full-node-consensus-rpc")
	require.NotContains(t, err.Error(), "op.light-client-address")

	malformed := *valid
	malformed.FullNodeExecutionRPC = "notaurl"
	malformed.OPConfig.LightClientAddress = "0xTOOSHORT"
	err = malformed.validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing scheme")
	require.Contains(t, err.Error(), "invalid Ethereum address")
}

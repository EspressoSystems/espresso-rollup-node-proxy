package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	nitroVerifier "proxy/verifier/nitro"
)

func TestDurationPflag(t *testing.T) {
	var d Duration
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.DurationVar((*time.Duration)(&d), "interval", 0, "test interval")

	require.NoError(t, fs.Parse([]string{"--interval=250ms"}))
	require.Equal(t, 250*time.Millisecond, time.Duration(d))

	require.NoError(t, fs.Parse([]string{"--interval", "1m"}))
	require.Equal(t, time.Minute, time.Duration(d))
}

func TestDurationUnmarshalJSON(t *testing.T) {
	t.Run("string duration", func(t *testing.T) {
		var d Duration
		require.NoError(t, json.Unmarshal([]byte(`"250ms"`), &d))
		require.Equal(t, 250*time.Millisecond, time.Duration(d))
	})

	t.Run("integer nanoseconds", func(t *testing.T) {
		var d Duration
		require.NoError(t, json.Unmarshal([]byte(`250000000`), &d))
		require.Equal(t, 250*time.Millisecond, time.Duration(d))
	})

	t.Run("invalid string", func(t *testing.T) {
		var d Duration
		require.Error(t, json.Unmarshal([]byte(`"notaduration"`), &d))
	})

	t.Run("full config json", func(t *testing.T) {
		raw := []byte(`{"verification_interval": "250ms", "finality_poll_interval": "500ms"}`)
		var cfg Config
		require.NoError(t, json.Unmarshal(raw, &cfg))
		require.Equal(t, 250*time.Millisecond, time.Duration(cfg.VerificationInterval))
		require.Equal(t, 500*time.Millisecond, time.Duration(cfg.FinalityPollInterval))
	})

	t.Run("finality poll interval defaults to 1s when unset", func(t *testing.T) {
		raw := []byte(`{"verification_interval": "250ms"}`)
		var cfg Config
		require.NoError(t, json.Unmarshal(raw, &cfg))
		require.Equal(t, time.Duration(0), time.Duration(cfg.FinalityPollInterval))

		// zero flows into NewFinalityPoller which substitutes the default
		opCfg := cfg.toOPVerifierConfig()
		require.Equal(t, time.Duration(0), opCfg.FinalityPollInterval)
		nitroCfg := cfg.toNitroVerifierConfig()
		require.Equal(t, time.Duration(0), nitroCfg.FinalityPollInterval)
	})
}

func TestConfigValidate(t *testing.T) {
	valid := Config{
		FullNodeExecutionRPC: "http://localhost:8545",
		L1RPC:                "ws://localhost:8546",
		Mode:                 ModeOP,
		ListenAddr:           ":8080",
		EspressoTag:          "espresso",
		StoreFilePath:        "espresso_store.json",
		LogLevel:             "info",
		QueryServiceURL:      "https://query.espresso.network",
		VerificationInterval: Duration(1 * time.Millisecond),
		OPConfig: OPConfig{
			FullNodeConsensusRPC:      "http://localhost:9545",
			LightClientAddress:        common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678"),
			BatcherAddress:            common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"),
			BatchAuthenticatorAddress: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		},
	}
	require.NoError(t, valid.validate())

	opEmpty := Config{Mode: ModeOP}
	err := opEmpty.validate()
	require.Error(t, err)
	for _, field := range []string{
		"full-node-execution-rpc", "l1-rpc",
		"op.full-node-consensus-rpc", "query-service-url",
		"op.light-client-address", "op.batcher-address", "op.batch-authenticator-address",
		"listen-addr", "espresso-tag", "store-file-path",
	} {
		require.Contains(t, err.Error(), field)
	}

	noMode := Config{}
	err = noMode.validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "mode")
	require.NotContains(t, err.Error(), "op.full-node-consensus-rpc")
	require.NotContains(t, err.Error(), "op.light-client-address")

	malformed := valid
	malformed.FullNodeExecutionRPC = "notaurl"
	malformed.OPConfig.LightClientAddress = common.Address{}
	err = malformed.validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing scheme")
	require.Contains(t, err.Error(), "op.light-client-address: must not be empty")

	t.Run("malformed address rejected at JSON unmarshal", func(t *testing.T) {
		raw := []byte(`{"op":{"light_client_address":"0xTOOSHORT"}}`)
		var cfg Config
		require.Error(t, json.Unmarshal(raw, &cfg))
	})

	badLogFormat := valid
	badLogFormat.LogFormat = "yaml"
	err = badLogFormat.validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "log-format")

	for _, goodFormat := range []string{"", "text", "json"} {
		goodLogFormat := valid
		goodLogFormat.LogFormat = goodFormat
		require.NoError(t, goodLogFormat.validate(), "log format %q should be valid", goodFormat)
	}

	validNitro := Config{
		FullNodeExecutionRPC: "http://localhost:8547",
		L1RPC:                "ws://localhost:8546",
		Mode:                 ModeNitro,
		ListenAddr:           ":8080",
		EspressoTag:          "espresso",
		StoreFilePath:        "espresso_store.json",
		LogLevel:             "info",
		QueryServiceURL:      "https://query.espresso.network",
		VerificationInterval: Duration(1 * time.Millisecond),
		NitroConfig: NitroConfig{
			FeedURL:       "ws://localhost:9642",
			BridgeAddress: common.HexToAddress("0x3f1Eae7D46d88F08fc2F8ed27FCb2AB183EB2d0E"),
			Namespace:     412346,
			ValidBatcherAddresses: []nitroVerifier.BatcherAddressConfig{
				{Address: "0x3f1Eae7D46d88F08fc2F8ed27FCb2AB183EB2d0E"},
			},
		},
	}
	require.NoError(t, validNitro.validate())

	nitroEmpty := Config{Mode: ModeNitro}
	err = nitroEmpty.validate()
	require.Error(t, err)
	for _, field := range []string{
		"full-node-execution-rpc", "l1-rpc", "nitro.feed-url",
		"nitro.bridge-address", "nitro.namespace", "nitro.valid-batcher-addresses",
		"query-service-url", "listen-addr", "espresso-tag", "store-file-path",
	} {
		require.Contains(t, err.Error(), field)
	}

	nitroBadAddr := validNitro
	nitroBadAddr.NitroConfig.ValidBatcherAddresses = []nitroVerifier.BatcherAddressConfig{
		{Address: "0xNOTANADDR"},
	}
	err = nitroBadAddr.validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "nitro.valid-batcher-addresses[0].address")
}

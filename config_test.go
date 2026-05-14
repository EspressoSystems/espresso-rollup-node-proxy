package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	nitroVerifier "proxy/verifier/nitro"
)

func TestDurationPflag(t *testing.T) {
	var d Duration
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.DurationVar(&d.Duration, "interval", 0, "test interval")

	require.NoError(t, fs.Parse([]string{"--interval=250ms"}))
	require.Equal(t, 250*time.Millisecond, d.Duration)

	require.NoError(t, fs.Parse([]string{"--interval", "1m"}))
	require.Equal(t, time.Minute, d.Duration)
}

func TestDurationUnmarshalJSON(t *testing.T) {
	t.Run("string duration", func(t *testing.T) {
		var d Duration
		require.NoError(t, json.Unmarshal([]byte(`"250ms"`), &d))
		require.Equal(t, 250*time.Millisecond, d.Duration)
	})

	t.Run("integer nanoseconds", func(t *testing.T) {
		var d Duration
		require.NoError(t, json.Unmarshal([]byte(`250000000`), &d))
		require.Equal(t, 250*time.Millisecond, d.Duration)
	})

	t.Run("invalid string", func(t *testing.T) {
		var d Duration
		require.Error(t, json.Unmarshal([]byte(`"notaduration"`), &d))
	})

	t.Run("full config json", func(t *testing.T) {
		raw := []byte(`{"verification_interval": "250ms"}`)
		var cfg Config
		require.NoError(t, json.Unmarshal(raw, &cfg))
		require.Equal(t, 250*time.Millisecond, cfg.VerificationInterval.Duration)
	})
}

func TestConfigValidate(t *testing.T) {
	valid := Config{
		FullNodeExecutionRPC: "http://localhost:8545",
		Mode:                 ModeOP,
		ListenAddr:           ":8080",
		EspressoTag:          "espresso",
		StoreFilePath:        "espresso_store.json",
		LogLevel:             "info",
		QueryServiceURL:      "https://query.espresso.network",
		VerificationInterval: Duration{1 * time.Millisecond},
		OPConfig: OPConfig{
			L1RPC:                     "ws://localhost:8546",
			FullNodeConsensusRPC:      "http://localhost:9545",
			LightClientAddress:        "0x1234567890abcdef1234567890abcdef12345678",
			BatcherAddress:            "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
			BatchAuthenticatorAddress: "0x1111111111111111111111111111111111111111",
		},
	}
	require.NoError(t, valid.validate())

	opEmpty := Config{Mode: ModeOP}
	err := opEmpty.validate()
	require.Error(t, err)
	for _, field := range []string{
		"full-node-execution-rpc", "op.l1-rpc",
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
	malformed.OPConfig.LightClientAddress = "0xTOOSHORT"
	err = malformed.validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing scheme")
	require.Contains(t, err.Error(), "invalid Ethereum address")

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
		Mode:                 ModeNitro,
		ListenAddr:           ":8080",
		EspressoTag:          "espresso",
		StoreFilePath:        "espresso_store.json",
		LogLevel:             "info",
		QueryServiceURL:      "https://query.espresso.network",
		VerificationInterval: Duration{1 * time.Millisecond},
		NitroConfig: NitroConfig{
			FeedURL:   "ws://localhost:9642",
			Namespace: 412346,
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
		"full-node-execution-rpc", "nitro.feed-url",
		"nitro.namespace", "nitro.valid-batcher-addresses",
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

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
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
		var op OPConfig
		require.NoError(t, json.Unmarshal(raw, &op))
		require.Equal(t, 250*time.Millisecond, op.VerificationInterval.Duration)
	})
}

func TestConfigValidate(t *testing.T) {
	valid := Config{
		FullNodeExecutionRPC: "http://localhost:8545",
		L1RPC:                "ws://localhost:8546",
		ListenAddr:           ":8080",
		EspressoTag:          "espresso",
		StoreFilePath:        "espresso_store.json",
		LogLevel:             "info",
		OPConfig: OPConfig{
			Enable:                    true,
			FullNodeConsensusRPC:      "http://localhost:9545",
			VerificationInterval:      Duration{1 * time.Millisecond},
			QueryServiceURL:           "https://query.espresso.network",
			LightClientAddress:        "0x1234567890abcdef1234567890abcdef12345678",
			BatcherAddress:            "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
			BatchAuthenticatorAddress: "0x1111111111111111111111111111111111111111",
		},
	}
	require.NoError(t, valid.validate())

	opEnabled := Config{OPConfig: OPConfig{Enable: true}}
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

	opDisabled := Config{}
	err = opDisabled.validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "full-node-execution-rpc")
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
}

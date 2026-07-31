package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"time"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/proxy"
	nitroVerifier "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/nitro"
	delayedmessagefetcher "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/nitro/delayed_message_fetcher"
	opVerifier "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/op"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"

	"github.com/spf13/pflag"
)

const (
	ModeOP    = "op"
	ModeNitro = "nitro"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		*d = Duration(parsed)
		return nil
	}
	return json.Unmarshal(b, (*time.Duration)(d))
}

type OPConfig struct {
	LightClientAddress        common.Address `json:"light_client_address"`
	BatcherAddress            common.Address `json:"batcher_address"`
	BatchAuthenticatorAddress common.Address `json:"batch_authenticator_address"`
}

type NitroConfig struct {
	FeedURL                  string                                  `json:"feed_url"`
	BridgeAddress            common.Address                          `json:"bridge_address"`
	ValidSigningKeyAddresses []nitroVerifier.SigningKeyAddressConfig `json:"valid_signing_key_addresses"`
	WaitForEthFinality       bool                                    `json:"wait_for_eth_finality"`
	EthLogScanBlockRange     uint64                                  `json:"eth_log_scan_block_range"`
}

type Config struct {
	FullNodeExecutionRPC   string      `json:"full_node_execution_rpc"`
	WsFullNodeExecutionRPC string      `json:"ws_full_node_execution_rpc"`
	EthRPC                 string      `json:"eth_rpc"`
	Mode                   string      `json:"mode"`
	Namespace              uint64      `json:"namespace"`
	ListenAddr             string      `json:"listen_addr"`
	WsListenAddr           string      `json:"ws_listen_addr"`
	EspressoTag            string      `json:"espresso_tag"`
	StoreFilePath          string      `json:"store_file_path"`
	QueryServiceURL        string      `json:"query_service_url"`
	VerificationInterval   Duration    `json:"verification_interval"`
	FinalityPollInterval   Duration    `json:"finality_poll_interval"`
	InitialHotshotHeight   uint64      `json:"initial_hotshot_height"`
	MaxBatchSize           int         `json:"max_batch_size"`
	MaxRequestBodySize     int         `json:"max_request_body_size"`
	OPConfig               OPConfig    `json:"op"`
	NitroConfig            NitroConfig `json:"nitro"`
	LogLevel               string      `json:"log_level"`
	LogFormat              string      `json:"log_format"`
}

func defaultConfig() *Config {
	return &Config{
		ListenAddr:           ":8080",
		EspressoTag:          "espresso",
		StoreFilePath:        "espresso_store.json",
		MaxBatchSize:         proxy.DefaultMaxBatchSize,
		MaxRequestBodySize:   proxy.DefaultMaxRequestBodySize,
		LogLevel:             "info",
		LogFormat:            "json",
		VerificationInterval: Duration(10 * time.Millisecond),
		NitroConfig: NitroConfig{
			EthLogScanBlockRange: delayedmessagefetcher.DefaultMaxBlocksPerScan,
		},
	}
}

func parseConfig() *Config {
	cfg := defaultConfig()

	configFlags := pflag.NewFlagSet("config", pflag.ContinueOnError)
	configFlags.ParseErrorsAllowlist.UnknownFlags = true
	configFile := configFlags.String("config", "", "path to JSON config file")
	_ = configFlags.Parse(os.Args[1:])

	if *configFile != "" {
		data, err := os.ReadFile(*configFile)
		if err != nil {
			log.Crit("failed to read config file", "file", *configFile, "error", err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			log.Crit("failed to parse config file", "file", *configFile, "error", err)
		}
	}

	pflag.String("config", "", "path to JSON config file")
	pflag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "logging level (e.g., debug, info, warn, error)")
	pflag.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "log output format (text or json)")
	pflag.StringVar(&cfg.ListenAddr, "listen-addr", cfg.ListenAddr, "proxy listen address")
	pflag.StringVar(&cfg.WsListenAddr, "ws.listen-addr", cfg.WsListenAddr, "proxy WebSocket listen address")
	pflag.StringVar(&cfg.FullNodeExecutionRPC, "full-node-execution-rpc", cfg.FullNodeExecutionRPC, "full node execution RPC URL")
	pflag.StringVar(&cfg.EthRPC, "eth-rpc", cfg.EthRPC, "Ethereum RPC URL")
	pflag.TextVar(&cfg.OPConfig.LightClientAddress, "op.light-client-address", cfg.OPConfig.LightClientAddress, "Espresso light client contract address")
	pflag.TextVar(&cfg.OPConfig.BatcherAddress, "op.batcher-address", cfg.OPConfig.BatcherAddress, "OP batcher address")
	pflag.TextVar(&cfg.OPConfig.BatchAuthenticatorAddress, "op.batch-authenticator-address", cfg.OPConfig.BatchAuthenticatorAddress, "Espresso batch authenticator contract address")
	pflag.StringVar(&cfg.WsFullNodeExecutionRPC, "ws.full-node-execution-rpc", cfg.WsFullNodeExecutionRPC, "full node execution RPC URL (websocket)")
	pflag.StringVar(&cfg.EspressoTag, "espresso-tag", cfg.EspressoTag, "espresso tag")
	pflag.StringVar(&cfg.StoreFilePath, "store-file-path", cfg.StoreFilePath, "path to state persistence file")
	pflag.Uint64Var(&cfg.InitialHotshotHeight, "initial-hotshot-height", cfg.InitialHotshotHeight, "initial hotshot height")
	pflag.IntVar(&cfg.MaxBatchSize, "max-batch-size", cfg.MaxBatchSize, "maximum number of requests in a JSON-RPC batch (0 = no limit)")
	pflag.IntVar(&cfg.MaxRequestBodySize, "max-request-body-size", cfg.MaxRequestBodySize, "maximum request body size in bytes (0 = no limit)")

	pflag.StringVar(&cfg.QueryServiceURL, "query-service-url", cfg.QueryServiceURL, "Espresso query service URL")
	pflag.DurationVar((*time.Duration)(&cfg.VerificationInterval), "verification-interval", time.Duration(cfg.VerificationInterval), "verification interval")
	pflag.DurationVar((*time.Duration)(&cfg.FinalityPollInterval), "finality-poll-interval", time.Duration(cfg.FinalityPollInterval), "finality poll interval (default 1s)")

	pflag.StringVar(&cfg.Mode, "mode", cfg.Mode, "verifier mode: op or nitro")
	pflag.Uint64Var(&cfg.Namespace, "namespace", cfg.Namespace, "Espresso namespace (Always should be l2 chain id)")

	pflag.StringVar(&cfg.NitroConfig.FeedURL, "nitro.feed-url", cfg.NitroConfig.FeedURL, "Nitro sequencer feed WebSocket URL")
	pflag.TextVar(&cfg.NitroConfig.BridgeAddress, "nitro.bridge-address", cfg.NitroConfig.BridgeAddress, "Nitro Bridge contract address on Ethereum")
	pflag.BoolVar(&cfg.NitroConfig.WaitForEthFinality, "nitro.wait-for-eth-finality", cfg.NitroConfig.WaitForEthFinality, "wait for Ethereum block finalization before fetching delayed messages")
	pflag.Uint64Var(&cfg.NitroConfig.EthLogScanBlockRange, "nitro.eth-log-scan-block-range", cfg.NitroConfig.EthLogScanBlockRange, "max Ethereum blocks scanned per eth_getLogs query when fetching delayed messages")
	var signingKeyAddressFlags []string
	pflag.StringArrayVar(&signingKeyAddressFlags, "nitro.valid-signing-key-addresses", nil, "valid signing key addresses for Nitro verifier (full range; use config file for from/to)")

	pflag.Parse()

	for _, addr := range signingKeyAddressFlags {
		cfg.NitroConfig.ValidSigningKeyAddresses = append(cfg.NitroConfig.ValidSigningKeyAddresses, nitroVerifier.SigningKeyAddressConfig{
			Address: common.HexToAddress(addr),
			From:    0,
			To:      math.MaxUint64,
		})
	}

	if err := cfg.validate(); err != nil {
		var joined interface{ Unwrap() []error }
		if errors.As(err, &joined) {
			for _, e := range joined.Unwrap() {
				log.Error("invalid configuration", "error", e)
			}
			log.Crit("invalid configuration")
			os.Exit(1)
		}
		log.Crit("invalid configuration", "error", err)
	}

	return cfg
}

func validateWebSocketURL(field, s string) error {
	if err := validateURL(field, s); err != nil {
		return err
	}
	u, _ := url.Parse(s)
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("%s: URL scheme must be ws or wss, got %q", field, u.Scheme)
	}
	return nil
}

func validateURL(field, s string) error {
	if s == "" {
		return fmt.Errorf("%s: must not be empty", field)
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("%s: invalid URL %q: %w", field, s, err)
	}
	if u.Scheme == "" {
		return fmt.Errorf("%s: missing scheme in URL %q", field, s)
	}
	if u.Host == "" {
		return fmt.Errorf("%s: missing host in URL %q", field, s)
	}
	return nil
}

func validateAddress(field string, a common.Address) error {
	if a == (common.Address{}) {
		return fmt.Errorf("%s: must not be empty", field)
	}
	return nil
}

func (c *Config) validate() error {
	var errs []error

	errs = append(errs, validateURL("full-node-execution-rpc", c.FullNodeExecutionRPC))
	errs = append(errs, validateURL("eth-rpc", c.EthRPC))

	switch c.Mode {
	case ModeOP, ModeNitro:
	default:
		errs = append(errs, fmt.Errorf("mode: must be %q or %q, got %q", ModeOP, ModeNitro, c.Mode))
	}

	errs = append(errs, validateURL("query-service-url", c.QueryServiceURL))
	if time.Duration(c.VerificationInterval) <= 0 {
		errs = append(errs, fmt.Errorf("verification-interval: must not be zero"))
	}

	if c.Mode == ModeOP {
		errs = append(errs, validateAddress("op.light-client-address", c.OPConfig.LightClientAddress))
		errs = append(errs, validateAddress("op.batcher-address", c.OPConfig.BatcherAddress))
		errs = append(errs, validateAddress("op.batch-authenticator-address", c.OPConfig.BatchAuthenticatorAddress))
	}

	if c.Mode == ModeNitro {
		errs = append(errs, validateURL("nitro.feed-url", c.NitroConfig.FeedURL))
		errs = append(errs, validateAddress("nitro.bridge-address", c.NitroConfig.BridgeAddress))
		if len(c.NitroConfig.ValidSigningKeyAddresses) == 0 {
			errs = append(errs, fmt.Errorf("nitro.valid-signing-key-addresses: at least one address required"))
		}
		for i, a := range c.NitroConfig.ValidSigningKeyAddresses {
			errs = append(errs, validateAddress(fmt.Sprintf("nitro.valid-signing-key-addresses[%d].address", i), a.Address))
		}
	}

	if c.WsListenAddr != "" {
		errs = append(errs, validateWebSocketURL("ws.full-node-execution-rpc", c.WsFullNodeExecutionRPC))
	}

	if c.ListenAddr == "" {
		errs = append(errs, fmt.Errorf("listen-addr: must not be empty"))
	}
	if c.EspressoTag == "" {
		errs = append(errs, fmt.Errorf("espresso-tag: must not be empty"))
	}
	if c.StoreFilePath == "" {
		errs = append(errs, fmt.Errorf("store-file-path: must not be empty"))
	}
	if c.Namespace == 0 {
		errs = append(errs, fmt.Errorf("namespace: must not be zero"))
	}
	if c.LogFormat != "" && c.LogFormat != "text" && c.LogFormat != "json" {
		errs = append(errs, fmt.Errorf("log-format: must be \"text\" or \"json\", got %q", c.LogFormat))
	}

	return errors.Join(errs...)
}

func (c *Config) toOPVerifierConfig() *opVerifier.OPEspressoBatchVerifierConfig {
	return &opVerifier.OPEspressoBatchVerifierConfig{
		FullNodeExecutionRPC:      c.FullNodeExecutionRPC,
		EthRPC:                    c.EthRPC,
		Namespace:                 c.Namespace,
		VerificationInterval:      time.Duration(c.VerificationInterval),
		QueryServiceURL:           c.QueryServiceURL,
		BatcherAddress:            c.OPConfig.BatcherAddress,
		BatchAuthenticatorAddress: c.OPConfig.BatchAuthenticatorAddress,
		FinalityPollInterval:      time.Duration(c.FinalityPollInterval),
	}
}

func (c *Config) toNitroVerifierConfig() *nitroVerifier.NitroEspressoBatchVerifierConfig {
	return &nitroVerifier.NitroEspressoBatchVerifierConfig{
		FeedURL:                  c.NitroConfig.FeedURL,
		FullNodeExecutionRPC:     c.FullNodeExecutionRPC,
		EthRpc:                   c.EthRPC,
		BridgeAddress:            c.NitroConfig.BridgeAddress,
		VerificationInterval:     time.Duration(c.VerificationInterval),
		FinalityPollInterval:     time.Duration(c.FinalityPollInterval),
		QueryServiceURL:          c.QueryServiceURL,
		Namespace:                c.Namespace,
		ValidSigningKeyAddresses: c.NitroConfig.ValidSigningKeyAddresses,
		WaitForEthFinalization:   c.NitroConfig.WaitForEthFinality,
		EthLogScanBlockRange:     c.NitroConfig.EthLogScanBlockRange,
	}
}

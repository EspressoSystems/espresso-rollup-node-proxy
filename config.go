package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"time"

	"proxy/proxy"
	nitroVerifier "proxy/verifier/nitro"
	opVerifier "proxy/verifier/op"

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
	FullNodeConsensusRPC      string         `json:"full_node_consensus_rpc"`
	LightClientAddress        common.Address `json:"light_client_address"`
	BatcherAddress            common.Address `json:"batcher_address"`
	BatchAuthenticatorAddress common.Address `json:"batch_authenticator_address"`
}

type NitroConfig struct {
	FeedURL               string                               `json:"feed_url"`
	BridgeAddress         common.Address                       `json:"bridge_address"`
	Namespace             uint64                               `json:"namespace"`
	InitialHotshotBlock   uint64                               `json:"initial_hotshot_block"`
	ValidBatcherAddresses []nitroVerifier.BatcherAddressConfig `json:"valid_batcher_addresses"`
	WaitForL1Finalization bool                                 `json:"wait_for_l1_finalization"`
}

type Config struct {
	FullNodeExecutionRPC string      `json:"full_node_execution_rpc"`
	L1RPC                string      `json:"l1_rpc"`
	Mode                 string      `json:"mode"`
	ListenAddr           string      `json:"listen_addr"`
	WsListenAddr         string      `json:"ws_listen_addr"`
	EspressoTag          string      `json:"espresso_tag"`
	StoreFilePath        string      `json:"store_file_path"`
	QueryServiceURL      string      `json:"query_service_url"`
	VerificationInterval Duration    `json:"verification_interval"`
	FinalityPollInterval Duration    `json:"finality_poll_interval"`
	InitialHotshotHeight uint64      `json:"initial_hotshot_height"`
	MaxBatchSize         int         `json:"max_batch_size"`
	MaxRequestBodySize   int         `json:"max_request_body_size"`
	OPConfig             OPConfig    `json:"op"`
	NitroConfig          NitroConfig `json:"nitro"`
	LogLevel             string      `json:"log_level"`
	LogFormat            string      `json:"log_format"`
	TrackBatchLatency    bool        `json:"track_batch_latency"`
}

func defaultConfig() *Config {
	return &Config{
		ListenAddr:           ":8080",
		WsListenAddr:         ":8081",
		EspressoTag:          "espresso",
		StoreFilePath:        "espresso_store.json",
		MaxBatchSize:         proxy.DefaultMaxBatchSize,
		MaxRequestBodySize:   proxy.DefaultMaxRequestBodySize,
		LogLevel:             "info",
		LogFormat:            "json",
		VerificationInterval: Duration(10 * time.Millisecond),
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
	pflag.StringVar(&cfg.WsListenAddr, "ws-listen-addr", cfg.WsListenAddr, "proxy WebSocket listen address")
	pflag.StringVar(&cfg.FullNodeExecutionRPC, "full-node-execution-rpc", cfg.FullNodeExecutionRPC, "full node execution RPC URL")
	pflag.StringVar(&cfg.L1RPC, "l1-rpc", cfg.L1RPC, "L1 RPC URL")
	pflag.TextVar(&cfg.OPConfig.LightClientAddress, "op.light-client-address", cfg.OPConfig.LightClientAddress, "Espresso light client contract address")
	pflag.TextVar(&cfg.OPConfig.BatcherAddress, "op.batcher-address", cfg.OPConfig.BatcherAddress, "OP batcher address")
	pflag.TextVar(&cfg.OPConfig.BatchAuthenticatorAddress, "op.batch-authenticator-address", cfg.OPConfig.BatchAuthenticatorAddress, "Espresso batch authenticator contract address")
	pflag.StringVar(&cfg.EspressoTag, "espresso-tag", cfg.EspressoTag, "espresso tag")
	pflag.StringVar(&cfg.StoreFilePath, "store-file-path", cfg.StoreFilePath, "path to state persistence file")
	pflag.Uint64Var(&cfg.InitialHotshotHeight, "initial-hotshot-height", cfg.InitialHotshotHeight, "initial hotshot height")
	pflag.BoolVar(&cfg.TrackBatchLatency, "track-batch-latency", cfg.TrackBatchLatency, "whether to track batch latency")
	pflag.IntVar(&cfg.MaxBatchSize, "max-batch-size", cfg.MaxBatchSize, "maximum number of requests in a JSON-RPC batch (0 = no limit)")
	pflag.IntVar(&cfg.MaxRequestBodySize, "max-request-body-size", cfg.MaxRequestBodySize, "maximum request body size in bytes (0 = no limit)")

	pflag.StringVar(&cfg.QueryServiceURL, "query-service-url", cfg.QueryServiceURL, "Espresso query service URL")
	pflag.DurationVar((*time.Duration)(&cfg.VerificationInterval), "verification-interval", time.Duration(cfg.VerificationInterval), "verification interval")
	pflag.DurationVar((*time.Duration)(&cfg.FinalityPollInterval), "finality-poll-interval", time.Duration(cfg.FinalityPollInterval), "finality poll interval (default 1s)")

	pflag.StringVar(&cfg.Mode, "mode", cfg.Mode, "verifier mode: op or nitro")
	pflag.StringVar(&cfg.OPConfig.FullNodeConsensusRPC, "op.full-node-consensus-rpc", cfg.OPConfig.FullNodeConsensusRPC, "OP full node consensus RPC URL")

	pflag.StringVar(&cfg.NitroConfig.FeedURL, "nitro.feed-url", cfg.NitroConfig.FeedURL, "Nitro sequencer feed WebSocket URL")
	pflag.TextVar(&cfg.NitroConfig.BridgeAddress, "nitro.bridge-address", cfg.NitroConfig.BridgeAddress, "Nitro Bridge contract address on L1")
	pflag.BoolVar(&cfg.NitroConfig.WaitForL1Finalization, "nitro.wait-for-l1-finalization", cfg.NitroConfig.WaitForL1Finalization, "wait for L1 block finalization before fetching delayed messages")
	pflag.Uint64Var(&cfg.NitroConfig.Namespace, "nitro.namespace", cfg.NitroConfig.Namespace, "Nitro namespace")
	pflag.Uint64Var(&cfg.NitroConfig.InitialHotshotBlock, "nitro.initial-hotshot-block", cfg.NitroConfig.InitialHotshotBlock, "initial HotShot block for Nitro streamer")
	var batcherAddressFlags []string
	pflag.StringArrayVar(&batcherAddressFlags, "nitro.valid-batcher-addresses", nil, "valid batcher addresses for Nitro verifier (full range; use config file for from/to)")

	pflag.Parse()

	for _, addr := range batcherAddressFlags {
		cfg.NitroConfig.ValidBatcherAddresses = append(cfg.NitroConfig.ValidBatcherAddresses, nitroVerifier.BatcherAddressConfig{
			Address: addr,
			From:    0,
			To:      math.MaxUint64,
		})
	}

	if err := cfg.validate(); err != nil {
		log.Crit("invalid configuration", "error", err)
	}

	return cfg
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

func validateAddressString(field, s string) error {
	if s == "" {
		return fmt.Errorf("%s: must not be empty", field)
	}
	if !common.IsHexAddress(s) {
		return fmt.Errorf("%s: invalid Ethereum address %q", field, s)
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
	errs = append(errs, validateURL("l1-rpc", c.L1RPC))

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
		errs = append(errs, validateURL("op.full-node-consensus-rpc", c.OPConfig.FullNodeConsensusRPC))
		errs = append(errs, validateAddress("op.light-client-address", c.OPConfig.LightClientAddress))
		errs = append(errs, validateAddress("op.batcher-address", c.OPConfig.BatcherAddress))
		errs = append(errs, validateAddress("op.batch-authenticator-address", c.OPConfig.BatchAuthenticatorAddress))
	}

	if c.Mode == ModeNitro {
		errs = append(errs, validateURL("nitro.feed-url", c.NitroConfig.FeedURL))
		errs = append(errs, validateAddress("nitro.bridge-address", c.NitroConfig.BridgeAddress))
		if c.NitroConfig.Namespace == 0 {
			errs = append(errs, fmt.Errorf("nitro.namespace: must not be zero"))
		}
		if len(c.NitroConfig.ValidBatcherAddresses) == 0 {
			errs = append(errs, fmt.Errorf("nitro.valid-batcher-addresses: at least one address required"))
		}
		for i, a := range c.NitroConfig.ValidBatcherAddresses {
			errs = append(errs, validateAddressString(fmt.Sprintf("nitro.valid-batcher-addresses[%d].address", i), a.Address))
		}
	}

	if c.ListenAddr == "" {
		errs = append(errs, fmt.Errorf("listen-addr: must not be empty"))
	}
	if c.WsListenAddr == "" {
		errs = append(errs, fmt.Errorf("ws-listen-addr: must not be empty"))
	}
	if c.EspressoTag == "" {
		errs = append(errs, fmt.Errorf("espresso-tag: must not be empty"))
	}
	if c.StoreFilePath == "" {
		errs = append(errs, fmt.Errorf("store-file-path: must not be empty"))
	}
	if c.LogFormat != "" && c.LogFormat != "text" && c.LogFormat != "json" {
		errs = append(errs, fmt.Errorf("log-format: must be \"text\" or \"json\", got %q", c.LogFormat))
	}

	return errors.Join(errs...)
}

func (c *Config) toProxyConfig() *proxy.ProxyConfig {
	return &proxy.ProxyConfig{
		FullNodeExecutionRPC: c.FullNodeExecutionRPC,
		EspressoTag:          c.EspressoTag,
		MaxBatchSize:         c.MaxBatchSize,
		MaxRequestBodySize:   c.MaxRequestBodySize,
	}
}

func (c *Config) toOPVerifierConfig() *opVerifier.OPEspressoBatchVerifierConfig {
	return &opVerifier.OPEspressoBatchVerifierConfig{
		FullNodeExecutionRPC:      c.FullNodeExecutionRPC,
		L1RPC:                     c.L1RPC,
		FullNodeConsensusRPC:      c.OPConfig.FullNodeConsensusRPC,
		VerificationInterval:      time.Duration(c.VerificationInterval),
		QueryServiceURL:           c.QueryServiceURL,
		BatcherAddress:            c.OPConfig.BatcherAddress,
		BatchAuthenticatorAddress: c.OPConfig.BatchAuthenticatorAddress,
		TrackBatchLatency:         c.TrackBatchLatency,
		FinalityPollInterval:      time.Duration(c.FinalityPollInterval),
	}
}

func (c *Config) toNitroVerifierConfig() *nitroVerifier.NitroEspressoBatchVerifierConfig {
	return &nitroVerifier.NitroEspressoBatchVerifierConfig{
		FeedURL:               c.NitroConfig.FeedURL,
		FullNodeExecutionRPC:  c.FullNodeExecutionRPC,
		L1RPC:                 c.L1RPC,
		BridgeAddress:         c.NitroConfig.BridgeAddress,
		VerificationInterval:  time.Duration(c.VerificationInterval),
		FinalityPollInterval:  time.Duration(c.FinalityPollInterval),
		QueryServiceURL:       c.QueryServiceURL,
		Namespace:             c.NitroConfig.Namespace,
		InitialHotshotBlock:   c.NitroConfig.InitialHotshotBlock,
		ValidBatcherAddresses: c.NitroConfig.ValidBatcherAddresses,
		WaitForL1Finalization: c.NitroConfig.WaitForL1Finalization,
	}
}

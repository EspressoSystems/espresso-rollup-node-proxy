package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"proxy/proxy"
	nitroVerifier "proxy/verifier/nitro"
	opVerifier "proxy/verifier/op"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/spf13/pflag"
)

type OPConfig struct {
	Enable                    bool          `json:"enable"`
	FullNodeConsensusRPC      string        `json:"full_node_consensus_rpc"`
	VerificationInterval      time.Duration `json:"verification_interval"`
	QueryServiceURL           string        `json:"query_service_url"`
	LightClientAddress        string        `json:"light_client_address"`
	BatcherAddress            string        `json:"batcher_address"`
	BatchAuthenticatorAddress string        `json:"batch_authenticator_address"`
}

type NitroConfig struct {
	Enable                bool          `json:"enable"`
	FeedURL               string        `json:"feed_url"`
	VerificationInterval  time.Duration `json:"verification_interval"`
	QueryServiceURL       string        `json:"query_service_url"`
	Namespace             uint64        `json:"namespace"`
	InitialHotshotBlock   uint64        `json:"initial_hotshot_block"`
	ValidBatcherAddresses []string      `json:"valid_batcher_addresses"`
}

type Config struct {
	FullNodeExecutionRPC string      `json:"full_node_execution_rpc"`
	L1RPC                string      `json:"l1_rpc"`
	ListenAddr           string      `json:"listen_addr"`
	EspressoTag          string      `json:"espresso_tag"`
	StoreFilePath        string      `json:"store_file_path"`
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
		ListenAddr:         ":8080",
		EspressoTag:        "espresso",
		StoreFilePath:      "espresso_store.json",
		MaxBatchSize:       proxy.DefaultMaxBatchSize,
		MaxRequestBodySize: proxy.DefaultMaxRequestBodySize,
		LogLevel:           "info",
		LogFormat:          "json",
		OPConfig: OPConfig{
			VerificationInterval: 10 * time.Millisecond,
		},
		NitroConfig: NitroConfig{
			VerificationInterval: 10 * time.Millisecond,
		},
	}
}

func parseConfig() *Config {
	cfg := defaultConfig()

	configFlags := pflag.NewFlagSet("config", pflag.ContinueOnError)
	configFlags.ParseErrorsWhitelist.UnknownFlags = true
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
	pflag.StringVar(&cfg.FullNodeExecutionRPC, "full-node-execution-rpc", cfg.FullNodeExecutionRPC, "full node execution RPC URL")
	pflag.StringVar(&cfg.L1RPC, "l1-rpc", cfg.L1RPC, "L1 RPC URL")
	pflag.StringVar(&cfg.EspressoTag, "espresso-tag", cfg.EspressoTag, "espresso tag")
	pflag.StringVar(&cfg.StoreFilePath, "store-file-path", cfg.StoreFilePath, "path to state persistence file")
	pflag.Uint64Var(&cfg.InitialHotshotHeight, "initial-hotshot-height", cfg.InitialHotshotHeight, "initial hotshot height")
	pflag.BoolVar(&cfg.TrackBatchLatency, "track-batch-latency", cfg.TrackBatchLatency, "whether to track batch latency")
	pflag.IntVar(&cfg.MaxBatchSize, "max-batch-size", cfg.MaxBatchSize, "maximum number of requests in a JSON-RPC batch (0 = no limit)")
	pflag.IntVar(&cfg.MaxRequestBodySize, "max-request-body-size", cfg.MaxRequestBodySize, "maximum request body size in bytes (0 = no limit)")

	pflag.BoolVar(&cfg.OPConfig.Enable, "op.enable", cfg.OPConfig.Enable, "enable OP mode")
	pflag.StringVar(&cfg.OPConfig.FullNodeConsensusRPC, "op.full-node-consensus-rpc", cfg.OPConfig.FullNodeConsensusRPC, "OP full node consensus RPC URL")
	pflag.DurationVar(&cfg.OPConfig.VerificationInterval, "op.verification-interval", cfg.OPConfig.VerificationInterval, "OP verification interval")
	pflag.StringVar(&cfg.OPConfig.QueryServiceURL, "op.query-service-url", cfg.OPConfig.QueryServiceURL, "Espresso query service URL")
	pflag.StringVar(&cfg.OPConfig.LightClientAddress, "op.light-client-address", cfg.OPConfig.LightClientAddress, "Espresso light client contract address")
	pflag.StringVar(&cfg.OPConfig.BatcherAddress, "op.batcher-address", cfg.OPConfig.BatcherAddress, "OP batcher address")
	pflag.StringVar(&cfg.OPConfig.BatchAuthenticatorAddress, "op.batch-authenticator-address", cfg.OPConfig.BatchAuthenticatorAddress, "Espresso batch authenticator contract address")

	pflag.BoolVar(&cfg.NitroConfig.Enable, "nitro.enable", cfg.NitroConfig.Enable, "enable Nitro mode")
	pflag.StringVar(&cfg.NitroConfig.FeedURL, "nitro.feed-url", cfg.NitroConfig.FeedURL, "Nitro sequencer feed WebSocket URL")
	pflag.DurationVar(&cfg.NitroConfig.VerificationInterval, "nitro.verification-interval", cfg.NitroConfig.VerificationInterval, "Nitro verification interval")
	pflag.StringVar(&cfg.NitroConfig.QueryServiceURL, "nitro.query-service-url", cfg.NitroConfig.QueryServiceURL, "Espresso query service URL for Nitro")
	pflag.Uint64Var(&cfg.NitroConfig.Namespace, "nitro.namespace", cfg.NitroConfig.Namespace, "Nitro namespace")
	pflag.Uint64Var(&cfg.NitroConfig.InitialHotshotBlock, "nitro.initial-hotshot-block", cfg.NitroConfig.InitialHotshotBlock, "initial HotShot block for Nitro streamer")
	pflag.StringArrayVar(&cfg.NitroConfig.ValidBatcherAddresses, "nitro.valid-batcher-addresses", cfg.NitroConfig.ValidBatcherAddresses, "valid batcher addresses for Nitro verifier")

	pflag.Parse()

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

func validateAddress(field, s string) error {
	if s == "" {
		return fmt.Errorf("%s: must not be empty", field)
	}
	if !common.IsHexAddress(s) {
		return fmt.Errorf("%s: invalid Ethereum address %q", field, s)
	}
	return nil
}

func (c *Config) validate() error {
	var errs []error

	errs = append(errs, validateURL("full-node-execution-rpc", c.FullNodeExecutionRPC))

	if c.OPConfig.Enable && c.NitroConfig.Enable {
		errs = append(errs, fmt.Errorf("cannot enable both op and nitro verifiers simultaneously"))
	}

	if c.OPConfig.Enable {
		errs = append(errs, validateURL("l1-rpc", c.L1RPC))
		errs = append(errs, validateURL("op.full-node-consensus-rpc", c.OPConfig.FullNodeConsensusRPC))
		errs = append(errs, validateURL("op.query-service-url", c.OPConfig.QueryServiceURL))
		errs = append(errs, validateAddress("op.light-client-address", c.OPConfig.LightClientAddress))
		errs = append(errs, validateAddress("op.batcher-address", c.OPConfig.BatcherAddress))
		errs = append(errs, validateAddress("op.batch-authenticator-address", c.OPConfig.BatchAuthenticatorAddress))
	}

	if c.NitroConfig.Enable {
		errs = append(errs, validateURL("nitro.feed-url", c.NitroConfig.FeedURL))
		errs = append(errs, validateURL("nitro.query-service-url", c.NitroConfig.QueryServiceURL))
		if c.NitroConfig.Namespace == 0 {
			errs = append(errs, fmt.Errorf("nitro.namespace: must not be zero"))
		}
		if len(c.NitroConfig.ValidBatcherAddresses) == 0 {
			errs = append(errs, fmt.Errorf("nitro.valid-batcher-addresses: at least one address required"))
		}
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
		FullNodeConsensusRPC:      c.OPConfig.FullNodeConsensusRPC,
		VerificationInterval:      c.OPConfig.VerificationInterval,
		QueryServiceURL:           c.OPConfig.QueryServiceURL,
		BatcherAddress:            c.OPConfig.BatcherAddress,
		BatchAuthenticatorAddress: c.OPConfig.BatchAuthenticatorAddress,
		TrackBatchLatency:         c.TrackBatchLatency,
	}
}

func (c *Config) toNitroVerifierConfig() *nitroVerifier.NitroEspressoBatchVerifierConfig {
	return &nitroVerifier.NitroEspressoBatchVerifierConfig{
		FeedURL:               c.NitroConfig.FeedURL,
		FullNodeExecutionRPC:  c.FullNodeExecutionRPC,
		VerificationInterval:  c.NitroConfig.VerificationInterval,
		QueryServiceURL:       c.NitroConfig.QueryServiceURL,
		Namespace:             c.NitroConfig.Namespace,
		InitialHotshotBlock:   c.NitroConfig.InitialHotshotBlock,
		ValidBatcherAddresses: c.NitroConfig.ValidBatcherAddresses,
	}
}

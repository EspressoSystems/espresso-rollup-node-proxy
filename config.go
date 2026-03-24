package main

import (
	"encoding/json"
	"os"
	verifier "proxy/verifier/op"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/spf13/pflag"
)

type OPConfig struct {
	FullNodeConsensusRPC string        `json:"full_node_consensus_rpc"`
	VerificationInterval time.Duration `json:"verification_interval"`
	QueryServiceURL      string        `json:"query_service_url"`
	LightClientAddress   string        `json:"light_client_address"`
	BatcherAddress       string        `json:"batcher_address"`
}

type Config struct {
	FullNodeExecutionRPC string   `json:"full_node_execution_rpc"`
	L1RPC                string   `json:"l1_rpc"`
	ListenAddr           string   `json:"listen_addr"`
	EspressoTag          string   `json:"espresso_tag"`
	StoreFilePath        string   `json:"store_file_path"`
	InitialHotshotHeight uint64   `json:"initial_hotshot_height"`
	OPConfig             OPConfig `json:"op"`
	LogLevel             string   `json:"log_level"`
}

func defaultConfig() *Config {
	return &Config{
		ListenAddr:    ":8080",
		EspressoTag:   "espresso",
		StoreFilePath: "espresso_store.json",
		LogLevel:      "info",
		OPConfig: OPConfig{
			VerificationInterval: 1 * time.Millisecond,
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
	pflag.StringVar(&cfg.ListenAddr, "listen-addr", cfg.ListenAddr, "proxy listen address")
	pflag.StringVar(&cfg.FullNodeExecutionRPC, "full-node-execution-rpc", cfg.FullNodeExecutionRPC, "full node execution RPC URL")
	pflag.StringVar(&cfg.L1RPC, "l1-rpc", cfg.L1RPC, "L1 RPC URL")
	pflag.StringVar(&cfg.EspressoTag, "espresso-tag", cfg.EspressoTag, "espresso tag")
	pflag.StringVar(&cfg.StoreFilePath, "store-file-path", cfg.StoreFilePath, "path to state persistence file")
	pflag.Uint64Var(&cfg.InitialHotshotHeight, "initial-hotshot-height", cfg.InitialHotshotHeight, "initial hotshot height")

	// Optimism specific config
	pflag.StringVar(&cfg.OPConfig.FullNodeConsensusRPC, "op.full-node-consensus-rpc", cfg.OPConfig.FullNodeConsensusRPC, "OP full node consensus RPC URL")
	pflag.DurationVar(&cfg.OPConfig.VerificationInterval, "op.verification-interval", cfg.OPConfig.VerificationInterval, "OP verification interval")
	pflag.StringVar(&cfg.OPConfig.QueryServiceURL, "op.query-service-url", cfg.OPConfig.QueryServiceURL, "Espresso query service URL")
	pflag.StringVar(&cfg.OPConfig.LightClientAddress, "op.light-client-address", cfg.OPConfig.LightClientAddress, "Espresso light client contract address")
	pflag.StringVar(&cfg.OPConfig.BatcherAddress, "op.batcher-address", cfg.OPConfig.BatcherAddress, "OP batcher address")

	pflag.Parse()
	return cfg

}

func (c *Config) toOPVerifierConfig() *verifier.OPEspressoBatchVerifierConfig {
	return &verifier.OPEspressoBatchVerifierConfig{
		FullNodeExecutionRPC: c.FullNodeExecutionRPC,
		FullNodeConsensusRPC: c.OPConfig.FullNodeConsensusRPC,
		VerificationInterval: c.OPConfig.VerificationInterval,
		QueryServiceURL:      c.OPConfig.QueryServiceURL,
		BatcherAddress:       c.OPConfig.BatcherAddress,
	}
}

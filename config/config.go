package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"proxy/streamer/nitro"

	"github.com/ethereum/go-ethereum/common"
	"github.com/spf13/pflag"
)

// Duration wraps time.Duration to support JSON unmarshalling from
// human-readable strings like "1s", "500ms", "2m30s".
type Duration time.Duration

func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		dur, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(dur)
		return nil
	}

	var ns int64
	if err := json.Unmarshal(b, &ns); err != nil {
		return fmt.Errorf("invalid duration value: %s", string(b))
	}
	*d = Duration(time.Duration(ns))
	return nil
}

type NitroConfig struct {
	FeedWSURL        string   `json:"feed-ws-url"`
	HotshotBlock     uint64   `json:"hotshot-block"`
	StartSeqNum      uint64   `json:"start-seq-num"`
	BatcherAddresses []string `json:"batcher-addresses"`
	VerifyInterval   Duration `json:"verify-interval"`
	RetryTime        Duration `json:"retry-time"`
}

type Config struct {
	ListenAddr  string      `json:"listen-addr"`
	FullNodeURL string      `json:"full-node-url"`
	EspressoURL string      `json:"espresso-url"`
	Namespace   uint64      `json:"namespace"`
	StateFile   string      `json:"state-file"`
	Nitro       NitroConfig `json:"nitro"`
}

func DefaultConfig() *Config {
	return &Config{
		ListenAddr: ":8547",
		StateFile:  "./espresso-state.json",
		Nitro: NitroConfig{
			HotshotBlock:   nitro.DefaultEspressoStreamerConfig.HotShotBlock,
			StartSeqNum:    1,
			VerifyInterval: Duration(time.Second),
			RetryTime:      Duration(nitro.DefaultEspressoStreamerConfig.TxnsPollingInterval),
		},
	}
}

func (c *Config) Validate() error {
	if c.FullNodeURL == "" {
		return fmt.Errorf("full-node-url is required")
	}
	if c.EspressoURL == "" {
		return fmt.Errorf("espresso-url is required")
	}
	if c.Nitro.FeedWSURL == "" {
		return fmt.Errorf("nitro.feed-ws-url is required")
	}
	return nil
}

func (c *Config) ParseBatcherAddresses() ([]common.Address, error) {
	var addrs []common.Address
	for _, addr := range c.Nitro.BatcherAddresses {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if !common.IsHexAddress(addr) {
			return nil, fmt.Errorf("invalid batcher address: %s", addr)
		}
		addrs = append(addrs, common.HexToAddress(addr))
	}
	return addrs, nil
}

func RegisterFlags(fs *pflag.FlagSet) *string {
	defaults := DefaultConfig()

	configFile := fs.String("config", "", "path to JSON configuration file")

	fs.String("listen-addr", defaults.ListenAddr, "address to listen on for JSON-RPC requests")
	fs.String("full-node-url", defaults.FullNodeURL, "URL of the full rollup node to proxy requests to")
	fs.String("espresso-url", defaults.EspressoURL, "URL of the Espresso node")
	fs.Uint64("namespace", defaults.Namespace, "Espresso namespace for the rollup")
	fs.String("state-file", defaults.StateFile, "path to persist espresso verification state")

	fs.String("nitro.feed-ws-url", defaults.Nitro.FeedWSURL, "WebSocket URL of the Nitro sequencer feed")
	fs.Uint64("nitro.hotshot-block", defaults.Nitro.HotshotBlock, "starting hotshot block number")
	fs.Uint64("nitro.start-seq-num", defaults.Nitro.StartSeqNum, "starting sequence number for the feed client")
	fs.String("nitro.batcher-addresses", "", "comma-separated list of valid batcher addresses (hex)")
	fs.Duration("nitro.verify-interval", defaults.Nitro.VerifyInterval.Duration(), "interval between verification checks")
	fs.Duration("nitro.retry-time", defaults.Nitro.RetryTime.Duration(), "retry interval for the espresso streamer")

	return configFile
}

// Load creates a Config by merging defaults, an optional JSON file, and CLI flags.
// Precedence: CLI flags > JSON file > defaults.
func Load(fs *pflag.FlagSet, configFile string) (*Config, error) {
	cfg := DefaultConfig()

	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	// Override with explicitly-set CLI flags only.
	// pflag.Visit skips flags that were not set on the command line,
	// so JSON/default values are preserved for unset flags.
	fs.Visit(func(f *pflag.Flag) {
		switch f.Name {
		case "listen-addr":
			cfg.ListenAddr, _ = fs.GetString("listen-addr")
		case "full-node-url":
			cfg.FullNodeURL, _ = fs.GetString("full-node-url")
		case "espresso-url":
			cfg.EspressoURL, _ = fs.GetString("espresso-url")
		case "namespace":
			cfg.Namespace, _ = fs.GetUint64("namespace")
		case "state-file":
			cfg.StateFile, _ = fs.GetString("state-file")
		case "nitro.feed-ws-url":
			cfg.Nitro.FeedWSURL, _ = fs.GetString("nitro.feed-ws-url")
		case "nitro.hotshot-block":
			cfg.Nitro.HotshotBlock, _ = fs.GetUint64("nitro.hotshot-block")
		case "nitro.start-seq-num":
			cfg.Nitro.StartSeqNum, _ = fs.GetUint64("nitro.start-seq-num")
		case "nitro.batcher-addresses":
			raw, _ := fs.GetString("nitro.batcher-addresses")
			if raw != "" {
				cfg.Nitro.BatcherAddresses = strings.Split(raw, ",")
			}
		case "nitro.verify-interval":
			d, _ := fs.GetDuration("nitro.verify-interval")
			cfg.Nitro.VerifyInterval = Duration(d)
		case "nitro.retry-time":
			d, _ := fs.GetDuration("nitro.retry-time")
			cfg.Nitro.RetryTime = Duration(d)
		}
	})

	return cfg, nil
}

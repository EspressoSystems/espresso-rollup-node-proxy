package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/spf13/pflag"

	"proxy/config"
	espressostore "proxy/espresso_store"
	proxyPkg "proxy/proxy"
	"proxy/streamer/nitro"
	opstreamer "proxy/streamer/op"
	nitroVerifier "proxy/verifier/nitro"
	"proxy/verifier/nitro/feedclient"
	opVerifier "proxy/verifier/op"
)

func main() {
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true)))

	fs := pflag.CommandLine
	configFile := config.RegisterFlags(fs)
	pflag.Parse()

	cfg, err := config.Load(fs, *configFile)
	if err != nil {
		log.Crit("failed to load configuration", "error", err)
	}

	if err := cfg.Validate(); err != nil {
		log.Crit("invalid configuration", "error", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	store, err := espressostore.NewEspressoStore(cfg.StateFile)
	if err != nil {
		log.Crit("failed to create espresso store", "error", err)
	}

	p := proxyPkg.NewProxy(cfg.FullNodeURL, store)

	var stopFn func()

	switch cfg.Mode {
	case "op":
		runner, err := newOpRunner(ctx, cfg, store)
		if err != nil {
			log.Crit("failed to create op runner", "error", err)
		}
		runner.Start(ctx)
		stopFn = func() {}

	case "nitro":
		batcherAddresses, err := cfg.ParseBatcherAddresses()
		if err != nil {
			log.Crit("invalid batcher addresses", "error", err)
		}
		client := espressoClient.NewClient(cfg.EspressoURL)
		streamer := nitro.NewEspressoStreamer(
			cfg.Namespace,
			cfg.Nitro.HotshotBlock,
			client,
			batcherAddresses,
			cfg.Nitro.RetryTime.Duration(),
		)
		feed := feedclient.NewFeedClient(cfg.Nitro.FeedWSURL, cfg.Nitro.StartSeqNum, nil, nil)
		v := nitroVerifier.NewVerifier(streamer, feed, store, cfg.Nitro.VerifyInterval.Duration())
		if err := streamer.Start(ctx); err != nil {
			log.Crit("failed to start espresso streamer", "error", err)
		}
		feed.Start(ctx)
		v.Start(ctx)
		stopFn = streamer.StopAndWait

	default:
		log.Crit("unknown mode", "mode", cfg.Mode, "valid", []string{"nitro", "op"})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", p.Serve)

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		log.Info("shutting down...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Error("HTTP server shutdown error", "error", err)
		}
		stopFn()
	}()

	log.Info("starting rollup node proxy", "addr", cfg.ListenAddr, "upstream", cfg.FullNodeURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Crit("HTTP server error", "error", err)
	}
}

func newOpRunner(ctx context.Context, cfg *config.Config, store *espressostore.EspressoStore) (*opVerifier.Runner, error) {
	l1, err := ethclient.DialContext(ctx, cfg.Op.L1URL)
	if err != nil {
		return nil, fmt.Errorf("failed to dial op L1 at %s: %w", cfg.Op.L1URL, err)
	}

	rollupL1, err := ethclient.DialContext(ctx, cfg.Op.RollupL1URL)
	if err != nil {
		return nil, fmt.Errorf("failed to dial op rollup L1 at %s: %w", cfg.Op.RollupL1URL, err)
	}

	batchPosterAddr := common.HexToAddress(cfg.Op.BatchPosterAddress)

	s := opstreamer.NewEspressoStreamer(
		cfg.Namespace,
		opstreamer.NewAdaptL1BlockRefClient(l1),
		opstreamer.NewAdaptL1BlockRefClient(rollupL1),
		espressoClient.NewClient(cfg.EspressoURL),
		nil,
		log.Root(),
		opstreamer.CreateEspressoBatchUnmarshaler(batchPosterAddr),
		cfg.Op.PollInterval.Duration(),
		cfg.Op.OriginHotShotPos,
		cfg.Op.OriginBatchPos,
	)

	interval := cfg.Op.PollInterval.Duration()
	if interval == 0 {
		interval = time.Second
	}

	return opVerifier.NewRunner(s, store, l1, interval), nil
}

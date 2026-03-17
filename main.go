package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	"github.com/ethereum/go-ethereum/log"
	"github.com/spf13/pflag"

	"proxy/config"
	espressostore "proxy/espresso_store"
	proxyPkg "proxy/proxy"
	"proxy/streamer/nitro"
	verifier "proxy/verifier/nitro"
	"proxy/verifier/nitro/feedclient"
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

	batcherAddresses, err := cfg.ParseBatcherAddresses()
	if err != nil {
		log.Crit("invalid batcher addresses", "error", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	store, err := espressostore.NewEspressoStore(cfg.StateFile)
	if err != nil {
		log.Crit("failed to create espresso store", "error", err)
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

	v := verifier.NewVerifier(streamer, feed, store, cfg.Nitro.VerifyInterval.Duration())

	p := proxyPkg.NewProxy(cfg.FullNodeURL, store)

	if err := streamer.Start(ctx); err != nil {
		log.Crit("failed to start espresso streamer", "error", err)
	}
	feed.Start(ctx)
	v.Start(ctx)

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
		streamer.StopAndWait()
	}()

	log.Info("starting rollup node proxy", "addr", cfg.ListenAddr, "upstream", cfg.FullNodeURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Crit("HTTP server error", "error", err)
	}
}

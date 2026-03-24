package main

import (
	"context"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"proxy/proxy"
	"proxy/store"
	verifier "proxy/verifier/op"
	"syscall"
	"time"

	espressoLightClient "github.com/EspressoSystems/espresso-network/sdks/go/light-client"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

func main() {
	cfg := parseConfig()
	if cfg == nil {
		log.Crit("failed to parse configuration")
		return
	}

	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		log.Crit("invalid log level", "level", cfg.LogLevel, "error", err)
	}
	logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, logLevel, true))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get the finalized block number to initialize the store
	client, err := ethclient.DialContext(ctx, cfg.FullNodeExecutionRPC)
	if err != nil {
		log.Crit("failed to dial full node execution RPC", "url", cfg.FullNodeExecutionRPC, "error", err)
	}
	defer client.Close()

	header, err := client.HeaderByNumber(ctx, big.NewInt(int64(rpc.FinalizedBlockNumber)))
	if err != nil {
		log.Crit("failed to fetch finalized block from full node", "error", err)
	}

	logger.Info("fetched finalized block number", "number", header.Number.String())

	espressoStore, err := store.NewEspressoStore(cfg.StoreFilePath, cfg.InitialHotshotHeight, header.Number.Uint64())
	if err != nil {
		log.Crit("failed to create espresso store", "error", err)
	}

	// Create an L1 client
	l1Client, err := ethclient.DialContext(ctx, cfg.L1RPC)
	if err != nil {
		logger.Crit("failed to create L1 client", "error", err)
	}

	// Create light client interface
	lightClientAddr := common.HexToAddress(cfg.OPConfig.LightClientAddress)
	espressoLightClient, err := espressoLightClient.NewLightclientCaller(lightClientAddr, l1Client)
	if err != nil || espressoLightClient == nil {
		logger.Crit("failed to create light client")
	}

	fullNodeVerifier := verifier.NewOPEspressoBatchVerifier(ctx, logger, espressoStore, l1Client, espressoLightClient, cfg.toOPVerifierConfig())
	if fullNodeVerifier == nil {
		log.Crit("failed to create OP verifier")
	}

	go fullNodeVerifier.Start(ctx)
	logger.Info("OP Verifier Started")
	fullNodeProxy := proxy.NewProxy(cfg.FullNodeExecutionRPC, espressoStore, cfg.EspressoTag)
	mux := http.NewServeMux()
	mux.HandleFunc("/", fullNodeProxy.Serve)

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	go func() {
		logger.Info("Proxy server listening", "addr", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Crit("proxy server failed", "error", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	// Listen for termination signals to gracefully shut down the server
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Info("received shutdown signal, shutting down server", "signal", sig)

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown failed", "error", err)
	} else {
		logger.Info("server shutdown gracefully")
	}
	fullNodeVerifier.Stop()
	logger.Info("Shutdown complete")
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"

	espressostore "proxy/espresso_store"
	"proxy/proxy"
	streamer "proxy/streamer/op"
	verifier "proxy/verifier/op"
)

type Config struct {
	ProxyPort                string `json:"proxyPort"`
	FullNodeExecutionRPC     string `json:"fullNodeExecutionRPC"`
	FullNodeConsensusRPC     string `json:"fullNodeConsensusRPC"`
	EspressoURL              string `json:"espressoUrl"`
	L1RPC                    string `json:"l1Rpc"`
	EspressoStoreFilePath    string `json:"espressoStoreFilePath"`
	Namespace                uint64 `json:"namespace"`
	BatcherAddress           string `json:"batcherAddress"`
	OriginHotShotPos         uint64 `json:"originHotShotPos"`
	OriginBatchPos           uint64 `json:"originBatchPos"`
	HotShotPollingIntervalMs int    `json:"hotShotPollingIntervalMs"`
	VerifierIntervalMs       int    `json:"verifierIntervalMs"`
	StreamerUpdateIntervalMs int    `json:"streamerUpdateIntervalMs"`
}

type ethL1ClientWrapper struct {
	client *ethclient.Client
}

func (c *ethL1ClientWrapper) HeaderHashByNumber(ctx context.Context, number *big.Int) (common.Hash, error) {
	header, err := c.client.HeaderByNumber(ctx, number)
	if err != nil {
		return common.Hash{}, err
	}
	return header.Hash(), nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <config.json>\n", os.Args[0])
		os.Exit(1)
	}

	configData, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read config file: %v\n", err)
		os.Exit(1)
	}

	var cfg Config
	if err := json.Unmarshal(configData, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse config: %v\n", err)
		os.Exit(1)
	}

	logger := log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true))
	log.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	espressoStore, err := espressostore.NewEspressoStore(cfg.EspressoStoreFilePath)
	if err != nil {
		logger.Crit("failed to create espresso store", "error", err)
	}

	ethL1Client, err := ethclient.DialContext(ctx, cfg.L1RPC)
	if err != nil {
		logger.Crit("failed to connect to L1 RPC", "error", err)
	}
	l1Client := &ethL1ClientWrapper{client: ethL1Client}

	espClient := espressoClient.NewClient(cfg.EspressoURL)

	batcherAddr := common.HexToAddress(cfg.BatcherAddress)
	unmarshalBatch := streamer.CreateEspressoBatchUnmarshaler(batcherAddr)

	hotShotInterval := time.Duration(cfg.HotShotPollingIntervalMs) * time.Millisecond
	s := streamer.NewEspressoStreamer(
		cfg.Namespace,
		l1Client,
		l1Client,
		espClient,
		nil,
		logger,
		unmarshalBatch,
		hotShotInterval,
		cfg.OriginHotShotPos,
		cfg.OriginBatchPos,
	)
	// Set a high finalized L1 number so batches pass the finality check.
	// In production this should be maintained via streamer.Refresh().
	s.FinalizedL1 = eth.L1BlockRef{Number: 999_999_999}

	streamerInterval := time.Duration(cfg.StreamerUpdateIntervalMs) * time.Millisecond
	go func() {
		ticker := time.NewTicker(streamerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.Update(ctx); err != nil {
					logger.Error("streamer update failed", "error", err)
				}
			}
		}
	}()

	verifierInterval := time.Duration(cfg.VerifierIntervalMs) * time.Millisecond
	v := verifier.NewVerifier(ctx, s, logger, espressoStore,
		cfg.FullNodeExecutionRPC, cfg.FullNodeConsensusRPC, verifierInterval)
	v.Start(ctx)

	p := proxy.NewProxy(cfg.FullNodeExecutionRPC, espressoStore)
	server := &http.Server{
		Addr:    ":" + cfg.ProxyPort,
		Handler: http.HandlerFunc(p.Serve),
	}

	go func() {
		logger.Info("proxy server starting", "port", cfg.ProxyPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Crit("proxy server failed", "error", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Info("received shutdown signal", "signal", sig)

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("proxy server shutdown error", "error", err)
	}

	logger.Info("shutdown complete")
}

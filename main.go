package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"proxy/proxy"
	"proxy/store"
	nitroVerifier "proxy/verifier/nitro"
	opVerifier "proxy/verifier/op"
	"runtime/debug"
	"syscall"
	"time"

	espressoLightClient "github.com/EspressoSystems/espresso-network/sdks/go/light-client"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
)

type batchVerifier interface {
	Start(ctx context.Context)
	Stop()
}

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func requestLoggingMiddleware(next http.Handler, logger log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote_addr", r.RemoteAddr,
			"status", sw.statusCode,
			"latency_ms", time.Since(start).Milliseconds(),
			"content_length", r.ContentLength,
		)
	})
}

func recoveryMiddleware(next http.Handler, logger log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered in HTTP handler", "panic", rec, "stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func newOPVerifier(ctx context.Context, logger log.Logger, cfg *Config, espressoStore *store.EspressoStore) batchVerifier {
	l1Client, err := ethclient.DialContext(ctx, cfg.L1RPC)
	if err != nil {
		logger.Crit("failed to create L1 client", "error", err)
	}
	lc, err := espressoLightClient.NewLightclientCaller(cfg.OPConfig.LightClientAddress, l1Client)
	if err != nil || lc == nil {
		logger.Crit("failed to create light client")
	}
	v := opVerifier.NewOPEspressoBatchVerifier(ctx, logger, espressoStore, l1Client, lc, cfg.toOPVerifierConfig())
	if v == nil {
		logger.Crit("failed to create OP verifier")
	}
	logger.Info("OP verifier enabled")
	return v
}

func newNitroVerifier(ctx context.Context, logger log.Logger, cfg *Config, espressoStore *store.EspressoStore) batchVerifier {
	v := nitroVerifier.NewNitroEspressoBatchVerifier(ctx, logger, espressoStore, cfg.toNitroVerifierConfig())
	if v == nil {
		logger.Crit("failed to create Nitro verifier")
	}
	logger.Info("Nitro verifier enabled")
	return v
}

func main() {
	cfg := parseConfig()

	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		log.Crit("invalid log level", "level", cfg.LogLevel, "error", err)
	}

	var handler slog.Handler
	if cfg.LogFormat == "json" {
		handler = log.JSONHandlerWithLevel(os.Stderr, logLevel)
	} else {
		handler = log.NewTerminalHandlerWithLevel(os.Stderr, logLevel, true)
	}
	logger := log.NewLogger(handler)
	log.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	espressoStore, err := store.NewEspressoStore(cfg.StoreFilePath, cfg.InitialHotshotHeight)
	if err != nil {
		logger.Crit("failed to create espresso store", "error", err)
	}

	var fullNodeVerifier batchVerifier

	switch cfg.Mode {
	case ModeOP:
		fullNodeVerifier = newOPVerifier(ctx, logger, cfg, espressoStore)

	case ModeNitro:
		fullNodeVerifier = newNitroVerifier(ctx, logger, cfg, espressoStore)

	default:
		logger.Crit("no verifier enabled: set --op.enable or --nitro.enable")
	}

	fullNodeVerifier.Start(ctx)
	logger.Info("Verifier started")
	fullNodeProxy := proxy.NewProxy(cfg.toProxyConfig(), espressoStore)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("OK"))
		if err != nil {
			logger.Error("failed to write health response", "error", err)
		}
	})
	mux.HandleFunc("/", fullNodeProxy.Serve)

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           requestLoggingMiddleware(recoveryMiddleware(mux, logger), logger),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 * 1024 * 1024,
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

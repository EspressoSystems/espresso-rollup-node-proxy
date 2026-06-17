package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	espressoLightClient "github.com/EspressoSystems/espresso-network/sdks/go/light-client"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/adapters"
	proxyhttp "github.com/EspressoSystems/espresso-rollup-node-proxy/http"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/proxy"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/store"
	nitroVerifier "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/nitro"
	opVerifier "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/op"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket/websocketutil"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
)

type batchVerifier interface {
	Start(ctx context.Context)
	Stop()
}

// mustNewOPVerifier creates a new instance of the OP batch verifier, and will
// log a critical error and exit if the verifier cannot be created. This
// includes failures to create the necessary L1 client and light client
// instances required dependency for the OP batch verifier.
func mustNewOPVerifier(ctx context.Context, logger log.Logger, cfg *Config, espressoStore *store.EspressoStore) batchVerifier {
	l1Client, err := ethclient.DialContext(ctx, cfg.L1RPC)
	if err != nil {
		logger.Crit("failed to create L1 client", "error", err)
		os.Exit(1)
	}
	lc, err := espressoLightClient.NewLightclientCaller(cfg.OPConfig.LightClientAddress, l1Client)
	if err != nil || lc == nil {
		logger.Crit("failed to create light client", "error", err)
		os.Exit(1)
	}
	v := opVerifier.NewOPEspressoBatchVerifier(ctx, logger, espressoStore, l1Client, lc, cfg.toOPVerifierConfig())
	if v == nil {
		logger.Crit("failed to create OP verifier")
		os.Exit(1)
	}
	logger.Info("OP verifier enabled")
	return v
}

// mustNewNitroVerifier creates a new instance of the Nitro batch verifier, and
// will log a critical error and exit if the verifier cannot be created.
func mustNewNitroVerifier(ctx context.Context, logger log.Logger, cfg *Config, espressoStore *store.EspressoStore) batchVerifier {
	v := nitroVerifier.NewNitroEspressoBatchVerifier(ctx, logger, espressoStore, cfg.toNitroVerifierConfig())
	if v == nil {
		logger.Crit("failed to create Nitro verifier")
		os.Exit(1)
	}
	logger.Info("Nitro verifier enabled")
	return v
}

// configureLogger sets up the global logger based on the provided
// configuration.
func configureLogger(cfg *Config) log.Logger {
	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		log.Crit("invalid log level", "level", cfg.LogLevel, "error", err)
		os.Exit(1)
	}

	var handler slog.Handler
	if cfg.LogFormat == "json" {
		handler = log.JSONHandlerWithLevel(os.Stderr, logLevel)
	} else {
		handler = log.NewTerminalHandlerWithLevel(os.Stderr, logLevel, true)
	}
	logger := log.NewLogger(handler)
	log.SetDefault(logger)
	return logger
}

// mustCreateEspressoStore creates a new instance of the EspressoStore,
// and will log a critical error and exit if the store cannot be created.
func mustCreateEspressoStore(logger log.Logger, cfg *Config) *store.EspressoStore {
	espressoStore, err := store.NewEspressoStore(cfg.StoreFilePath, cfg.InitialHotshotHeight)
	if err != nil {
		logger.Crit("failed to create espresso store", "error", err)
		os.Exit(1)
	}
	return espressoStore
}

// mustCreateVerifier creates a new instance of the batch verifier based on the
// provided configuration, and will log a critical error and exit if the
// verifier is enabled but cannot be created. If no verifier mode is enabled,
// it will log a critical message and exit as well.
func mustCreateVerifier(
	ctx context.Context,
	logger log.Logger,
	cfg *Config,
	espressoStore *store.EspressoStore,
) batchVerifier {
	switch cfg.Mode {
	case ModeOP:
		return mustNewOPVerifier(ctx, logger, cfg, espressoStore)

	case ModeNitro:
		return mustNewNitroVerifier(ctx, logger, cfg, espressoStore)

	default:
		logger.Crit("no verifier enabled: set --op.enable or --nitro.enable")
		os.Exit(1)
	}

	// unreachable, but required to satisfy the compiler
	return nil
}

// healthCheckHandler is a simple HTTP handler that responds with a 200 OK
// status and a message body of "OK".
func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "OK", http.StatusOK)
}

func NewSingleHostWSReverseProxy(target *url.URL, upgrader websocket.Upgrader) *websocketutil.ReverseProxy {
	return websocketutil.NewReverseProxy(
		target,
		websocket.CoderDialer(),
		upgrader,
	)
}

// NewSingleHostReverseProxy creates a ReverseProxy that forwards requests to
// the specified target URL.
//
// It also modifies the Host in the outgoing request to match the new target.
func NewSingleHostReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
	}
}

// createHttpServer creates and configures an HTTP server for handling JSON-RPC
// requests. It sets up the necessary middleware for request logging, body
// size limits, and the JSON-RPC bridge to the full node proxy. It also
// includes a health check endpoint at "/health".
func createHttpServer(logger log.Logger, cfg *Config, interceptor adapters.Interceptor) *http.Server {
	fullNodeExecutionRPCURL, err := url.Parse(cfg.FullNodeExecutionRPC)
	if err != nil {
		logger.Crit("failed to parse full node execution RPC URL", "error", err)
		os.Exit(1)
	}

	// Create the Reverse Proxy
	reverseProxy := NewSingleHostReverseProxy(fullNodeExecutionRPCURL)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthCheckHandler)
	mux.Handle(
		"/",
		proxyhttp.HTTPRPCMiddlewares(
			logger,
			int64(cfg.MaxRequestBodySize),
			adapters.NewHTTPJSONRPCInterceptor(reverseProxy, interceptor),
		),
	)

	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           proxyhttp.RequestLoggingMiddleware(mux, logger),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 * 1024 * 1024,
	}
}

// createWsServer creates and configures an HTTP server for handling WebSocket
// upgrades and JSON-RPC requests over WebSockets.
//
// TODO: Some addition configuration / middleware work may be warranted here.
// If we can create a middleware setup for Websocket processing similar to
// our http.Handler middlewares, we may be able to setup some logging, and
// other additional middleware criteria.
func createWsServer(logger log.Logger, cfg *Config, interceptor adapters.Interceptor) *http.Server {
	if cfg.WsListenAddr == "" || cfg.WsFullNodeExecutionRPC == "" {
		logger.Info("WebSocket server disabled: both --ws.listen-addr and --ws.full-node-execution-rpc must be set")
		return nil
	}

	upstreamURL, err := url.Parse(cfg.WsFullNodeExecutionRPC)
	if err != nil {
		logger.Warn("received invalid URL for webSocket, disabling websocket", "url", upstreamURL, "err", err)
		return nil
	}

	upgrader := websocket.CoderUpgrader(websocket.SetUpgradeReadSizeLimit(uint64(cfg.MaxRequestBodySize)))
	interceptorUpgrader := adapters.WebSocketUpgraderInterceptor(upgrader, interceptor)
	reverseProxy := NewSingleHostWSReverseProxy(upstreamURL, interceptorUpgrader)

	return &http.Server{
		Addr: cfg.WsListenAddr,
		Handler: proxyhttp.WebSocketUpgrader(
			logger,
			reverseProxy,
		),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 * 1024 * 1024,
	}
}

// startHTTPServers starts the provided HTTP servers in separate goroutines,
// and logs any critical errors that occur during startup.
func startHTTPServers(
	logger log.Logger,
	wg *sync.WaitGroup,
	servers ...*http.Server,
) {
	for _, server := range servers {
		if server == nil {
			// Skip any non-existent serveres, this allows us to conditionally
			// start servers based on configuration.
			continue
		}

		wg.Add(1)
		go func(wg *sync.WaitGroup, server *http.Server) {
			defer wg.Done()
			logger.Info("server listening", "addr", server.Addr)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				// TODO: Re-evalute this logger.Crit usage. Invoking this function
				// will also call os.Exit, forcing the program to exit without
				// cleaning up.
				logger.Crit("server failed to listen and serve", "error", err)
			}
		}(wg, server)
	}
}

// cleanHTTPServerShutdown attempts to gracefully shut down both the HTTP
// and WebSocket servers, allowing any in-flight requests to complete before
// the servers are stopped.
//
// They are ultimately governed by the passed context for a timeout.
// If the context expires, it will trigger the server to finish its
// shutdown regardless of whether or not in-flight requests have completed.
func cleanHTTPServerShutdown(logger log.Logger, servers ...*http.Server) {
	ctx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	var wg sync.WaitGroup

	for _, server := range servers {
		if server == nil {
			// Ignore any nil servers
			continue
		}

		// Spawn a goroutine to shut down the HTTP server
		wg.Add(1)
		go (func(shutdownCtx context.Context, wg *sync.WaitGroup, server *http.Server) {
			defer wg.Done()
			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Error("http server shutdown failed", "error", err)
			} else {
				logger.Info("http server shutdown gracefully")
			}
		})(ctx, &wg, server)
	}

	// Wait for the goroutines to exit
	wg.Wait()
}

func main() {
	cfg := parseConfig()
	logger := configureLogger(cfg)

	// Setup the application context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	espressoStore := mustCreateEspressoStore(logger, cfg)
	fullNodeVerifier := mustCreateVerifier(ctx, logger, cfg, espressoStore)

	fullNodeVerifier.Start(ctx)
	logger.Info("Verifier started")
	interceptor := proxy.NewInterceptor(
		logger,
		espressoStore,
		cfg.EspressoTag,
		cfg.MaxBatchSize,
	)

	httpServer := createHttpServer(logger, cfg, interceptor)
	webSocketServer := createWsServer(logger, cfg, interceptor)

	var serverWaitGroup sync.WaitGroup
	startHTTPServers(logger, &serverWaitGroup, httpServer, webSocketServer)

	sigCh := make(chan os.Signal, 1)
	// Listen for termination signals to gracefully shut down the server
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		logger.Info("received shutdown signal, shutting down server", "signal", sig)

	case <-ctx.Done():
		logger.Info("program context canceled, shutting down server", "err", ctx.Err())
	}

	// Cancel the application context
	cancel()

	// Cleanly shutdown the servers
	cleanHTTPServerShutdown(logger, httpServer, webSocketServer)

	fullNodeVerifier.Stop()

	logger.Info("Shutdown complete")
}

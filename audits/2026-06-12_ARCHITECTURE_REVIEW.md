# Architecture Review — espresso-rollup-node-proxy

**Date:** 2026-06-12
**Branch:** main
**Commit:** e3b7705f872057453c38f01a9b5e33061cfac396
**Reviewer:** Claude AI (claude-sonnet-4-6)

---

## System Overview

`espresso-rollup-node-proxy` is a transparent JSON-RPC reverse proxy that sits between Ethereum rollup clients and a rollup's full node (supporting both OP Stack and Arbitrum Nitro). Its primary function is to resolve a configurable block tag (default: `"espresso"`, optionally `"finalized"`) to the latest L2 block number that has been confirmed by Espresso's HotShot consensus, always using `max(espresso_finalized, eth_finalized)` to prevent regressions.

**Primary data flows:**

1. **Inbound request path (HTTP):** Client POST → `RequestLoggingMiddleware` → `RecoveryMiddleware` → `MethodIsMiddleware` → `RequestBodySizeLimiterMiddleware` → `ContentTypeIsJSONRPCMiddleware` → `httpJSONRPCInterceptor` (tag substitution) → `httputil.ReverseProxy` → upstream full node.

2. **Inbound request path (WebSocket):** Client WS upgrade → `websocketUpgrader` → `interceptorUpgrader` wraps upstream `Upgrader` → `webSocketJSONRPCDownstreamIntercept` rewrites each incoming message before forwarding to `websocketutil.ReverseProxy` → upstream full node.

3. **Background verification loop:** `FinalityPoller` (generic) polls sync status at a configured interval → verifier (`OPEspressoBatchVerifier` or `NitroEspressoBatchVerifier`) compares Espresso streamer batches against full node blocks → on match, `EspressoStore.UpdateIfGreater` persists the new L2 block number atomically.

4. **Tag resolution at intercept time:** `proxy.Interceptor.InterceptRequest` calls `EspressoStore.GetState()` (RW-mutex-protected read) and rewrites the tag in the JSON params tree before forwarding.

---

## Package Structure Diagram

```
main (package main)
├── config.go           — CLI/JSON config, validation, mode switching
├── main.go             — wiring: servers, verifier, store, shutdown
│
├── proxy/              — core business logic: tag interception
│   └── interceptor.go  — Interceptor{store, espressoTag, maxBatchSize}
│                         InterceptRequest / InterceptBatchRequests
│
├── store/              — persistence layer
│   └── espresso_store.go — EspressoStore (RWMutex + atomic file write)
│
├── verifier/           — background batch verification
│   ├── finality_poller.go  — generic FinalityPoller[T] (polling loop)
│   ├── op/
│   │   ├── op_verifier.go  — OPEspressoBatchVerifier
│   │   └── op_eth_client.go
│   └── nitro/
│       ├── nitro_verifier.go
│       ├── feed_client/
│       └── abi/
│
├── adapters/           — glue between transport and business logic
│   ├── helpers_interceptor_glue.go  — PerformRequestIntercept, Interceptor iface
│   ├── interceptor_http.go          — httpJSONRPCInterceptor (http.Handler)
│   ├── interceptor_websocket.go     — webSocketJSONRPCDownstreamIntercept
│   ├── helpers_jsonrpcv2_http.go    — WriteJSONRPCResponse/Error to http.ResponseWriter
│   ├── helpers_jsonrpcv2_websocket.go — WriteJSONRPCResponse/Error to websocket.Conn
│   └── read_closer.go               — ReadCloser utility
│
├── http/               — HTTP middleware stack
│   ├── cors.go
│   ├── body_size_limiter.go
│   ├── body_auto_closer.go
│   ├── content_type_json_rpc.go
│   ├── method_is.go
│   ├── panic_recovery.go
│   ├── request_logger.go
│   ├── middlewares.go  — HTTPRPCMiddlewares() composition
│   └── websocket_bridge.go — WebSocketUpgrader handler
│
├── websocket/          — WebSocket abstraction layer
│   ├── interfaces.go   — Conn, Upgrader, Dialer, Reader, Writer, Closer
│   ├── gorilla.go      — gorilla/websocket adapter
│   ├── coder.go        — coder/websocket adapter
│   ├── header.go       — proxy header utilities
│   ├── websocketutil/
│   │   └── components.go  — ReverseProxy
│   └── websockettest/
│
├── jsonrpcv2/          — JSON-RPC 2.0 types and serialization
│   ├── types.go
│   ├── types_json_extensions.go  — custom Marshal/Unmarshal
│   ├── types_util.go
│   ├── types_errors_extensions.go
│   ├── util.go
│   └── handler.go      — JSONRPCHandler / JSONRPCBatchHandler interfaces (unused)
│
└── espresso_e2e/       — integration tests (Docker Compose)
    └── e2e_utils.go

Dependency direction (→ = imports):
  main → proxy, store, verifier/op, verifier/nitro, adapters, http, websocket
  adapters → jsonrpcv2, websocket, (Interceptor interface defined here)
  proxy → jsonrpcv2, store
  http → websocket
  verifier/op → store, verifier (shared), espresso-streamers
  verifier/nitro → store, verifier (shared)
  store → (only stdlib + renameio)
  jsonrpcv2 → (only stdlib)
  websocket → gorilla, coder (leaf packages)
```

---

## Critical Architecture Issues (AR-C-*)

### AR-C-1: `Interceptor` interface defined in `adapters`, not in `proxy`

**Description:** The `Interceptor` interface (`adapters.Interceptor`) is declared in `adapters/helpers_interceptor_glue.go`, yet the concrete implementation (`proxy.Interceptor`) lives in the `proxy` package. This is a layer inversion: the interface that describes a capability of the business logic layer (`proxy`) is owned by the transport-adaptation layer (`adapters`). The `proxy` package has no interface of its own; consumers must import `adapters` to type-check against it.

**Impact:** Any package that needs to mock or replace the interceptor for testing must import `adapters`. The `proxy` package cannot be tested independently of the interface definition. The interface should travel with the implementor or the consumer, not in a third package.

**Recommendation:** Move the `Interceptor` interface into the `proxy` package (or a new `interceptor` package). The `adapters` package should import `proxy` to use it. This restores the conventional Go pattern where the interface lives with the consumer or is co-located with its primary implementation.

---

### AR-C-2: `proxy.Interceptor` uses `log.Warn` on the global logger, bypassing injected logger

**Description:** In `proxy/interceptor.go`, `InterceptRequest` and `InterceptBatchRequests` call `log.Warn(...)` (the global `go-ethereum` logger) rather than using a logger field on the `Interceptor` struct. Every other component in the codebase (verifiers, middleware, store) receives a `log.Logger` at construction time.

**Impact:** The proxy's core interception loop is untestable for log output, cannot have its log level controlled independently, and silently couples to a global side effect. The `configureLogger` function in `main.go` sets a global default, so this works at runtime, but it is architecturally inconsistent and fragile in tests.

**Recommendation:** Add a `logger log.Logger` field to `proxy.Interceptor` and thread it through `NewInterceptor`. Replace all `log.Warn` calls with `i.logger.Warn`.

---

### AR-C-3: `EspressoStore.UpdateIfGreater` has a read-check / write race

**Description:** In `store/espresso_store.go`, `UpdateIfGreater` calls `es.GetState()` (acquires `RLock`, returns copy, releases lock), then calls `es.writeToDisk(newState)` outside any lock, and only then acquires the write lock to update `es.state`. Between the read check and the write lock, a second concurrent call could observe the same stale `state.L2BlockNumber` and both proceed past the guard.

```go
func (es *EspressoStore) UpdateIfGreater(l2BlockNumber uint64, ...) (bool, error) {
    state := es.GetState()              // RLock acquired and released
    if state.L2BlockNumber >= l2BlockNumber { return false, nil }
    // --- another goroutine could pass the guard here ---
    if err := es.writeToDisk(newState); err != nil { ... }
    es.mu.Lock()                        // write lock acquired here
    es.state = newState
    ...
}
```

In practice, `verifyAndAdvance` is called from a single goroutine per verifier and `syncEspressoStateWithEthereumFinality` is called from the same goroutine, so the race does not currently trigger. But the code is not safe as written.

**Impact:** If two writers ever exist (e.g., a future multi-verifier mode, or a test that races two goroutines), the disk and in-memory state can diverge. The file written could reflect a lower block number than `es.state`.

**Recommendation:** Hold the write lock for the entire check-write-update sequence, or restructure to check-under-lock before writing. The simplest fix: acquire `es.mu.Lock()` at the top of `UpdateIfGreater`, replace the `GetState()` call with a direct read of `es.state`, write to disk inside the lock, and release at the end.

---

### AR-C-4: `main.go` calls `logger.Crit` in `startHTTPServers` but cannot clean up

**Description:** In `startHTTPServers`, the goroutine calls `logger.Crit("server failed to listen and serve", ...)` on a `ListenAndServe` error. The `go-ethereum` `logger.Crit` internally calls `os.Exit(1)`, which bypasses all deferred cleanup, skips the graceful shutdown path (`cleanHTTPServerShutdown`, `fullNodeVerifier.Stop()`), and can leave the store mid-write.

The TODO comment in the code acknowledges this: `// TODO: Re-evalute this logger.Crit usage.`

**Impact:** Under any server error (e.g., port already in use, TLS failure, kernel refusing bind), the process exits without flushing the store or gracefully stopping the verifier. If `EspressoStore` is mid-write via `renameio`, the temporary file may be orphaned.

**Recommendation:** Replace `logger.Crit` in the goroutine with a channel-based error signal back to `main()`, which can then trigger the cancellation of the root context, allowing the normal shutdown path to run. Example: pass an `errCh chan error` to `startHTTPServers` and `select` on it in `main()` alongside the signal channel.

---

## Design Concerns (AR-D-*)

### AR-D-1: `adapters` package conflates four distinct responsibilities

**Description:** The `adapters` package currently contains:
1. The `Interceptor` interface definition (`helpers_interceptor_glue.go`)
2. HTTP-transport adapter (`interceptor_http.go`, `helpers_jsonrpcv2_http.go`)
3. WebSocket-transport adapter (`interceptor_websocket.go`, `helpers_jsonrpcv2_websocket.go`)
4. A general utility (`read_closer.go`)

The package name `adapters` correctly describes items 2–3, but item 1 (the core interface) and item 4 (a general utility) do not belong here. The package is doing glue work that would be clearer if split: the interface with `proxy`, the HTTP adapter as part of `http`, and the WebSocket adapter as part of `websocket` or a dedicated `wsadapter` sub-package.

**Recommendation:** Move `Interceptor` to `proxy`. Move `ReadCloser` to a stdlib-like `ioutils` package or inline it. Consider splitting the HTTP and WebSocket adapters into sub-packages under `adapters` (`adapters/http`, `adapters/ws`) or co-locate them with their respective transport packages.

---

### AR-D-2: `jsonrpcv2/handler.go` defines interfaces that are never used

**Description:** `JSONRPCHandler` and `JSONRPCBatchHandler` are defined in `jsonrpcv2/handler.go` but are not implemented or referenced anywhere in the codebase. The actual request handling does not use these interfaces — it goes through `proxy.Interceptor` and `adapters.PerformRequestIntercept`.

**Impact:** Dead code creates confusion about the intended design. A new contributor might attempt to implement `JSONRPCHandler` and wonder why it has no wiring. This is also specifically noted as the untracked file `jsonrpcv2/handler.go` in the git status, suggesting it may be work-in-progress that was never integrated.

**Recommendation:** Either wire these interfaces into the actual dispatch path (and delete the ad-hoc function-based dispatch in `adapters`) or remove the file. If they represent a future design direction, add a comment explaining the intent.

---

### AR-D-3: Dual-WebSocket-library dependency with no clear policy

**Description:** The codebase imports and adapts both `github.com/gorilla/websocket` and `github.com/coder/websocket` (see `websocket/gorilla.go` and `websocket/coder.go`). The concrete wiring in `main.go` uses only `websocket.CoderDialer()` and `websocket.CoderUpgrader()` (coder), while `gorilla` is present as an alternative implementation. The `go.mod` retains gorilla as a direct dependency.

**Impact:** Two WebSocket libraries means two sets of edge cases, two sets of close-error handling code, and an increased binary size. The gorilla implementation's `Read` method has a non-trivial context-cancellation workaround (spawning a goroutine to set `SetReadDeadline`) that does not exist in the coder implementation. The approaches are not equivalent.

**Recommendation:** Decide on a single library. If gorilla is kept for the WS-to-WS bridge path and coder for the server-side upgrade, document this explicitly. If gorilla is genuinely unused at runtime, remove it from `go.mod`.

---

### AR-D-4: `replaceTagInParams` deserializes through `any` and re-serializes, losing JSON fidelity

**Description:** In `proxy/interceptor.go`, `InterceptRequest` receives a `jsonrpcv2.Request` whose `Params` field is `any`. After JSON unmarshaling by the `encoding/json` decoder (using `json.Number` per `fullDecode`), numeric values in params will be `json.Number`, booleans will be `bool`, nested objects will be `map[string]any`. The recursive `replaceTagInParams` walks and re-marshals these. This pipeline can alter the representation of numeric parameters (e.g., large integers that happen to be represented as hex strings are safe, but `json.Number` values re-marshaled via `json.Marshal(map[string]any{...})` may change precision or representation).

More concretely: the params for something like `eth_call` may include a hex-encoded `data` field that is not the espresso tag. These are passed through untouched. But if the upstream sends `params` as a raw JSON string like `"0xabc"` that is not the espresso tag, it is returned as a Go `string` and re-marshaled as a JSON string — which is fine. The real risk is with `json.Number` values: `json.Marshal` of a `json.Number` renders its string representation, which is correct, but the roundtrip through `map[string]any` discards the original key ordering, which may matter for some implementations.

**Impact:** Low in practice (Ethereum RPC params are positional arrays, not ordered objects), but the architecture couples interception to full deserialization when only a targeted string-scan is needed.

**Recommendation:** Consider operating on the raw `[]byte` via a streaming JSON scanner rather than full deserialization. A scanner can find and replace exact string values (`"espresso"`) without unmarshaling the entire params tree, avoiding the fidelity concern entirely and being significantly faster for large payloads.

---

### AR-D-5: `EspressoState` validity check is fragile and semantically ambiguous

**Description:** In `proxy/interceptor.go`, `getCurrentEspressoFinalizedBlockNumber` checks:
```go
if state.FallbackHotshotHeight == 0 || state.L2BlockNumber == 0 || state.UpdatedAt.IsZero() {
    return 0, ErrUnknownEspressoFinalizedBlockNumber
}
```
This uses zero as a sentinel for "uninitialized", but block 0 is a valid L2 block number (the genesis block). A chain that has just started where Espresso has only confirmed the genesis block (`L2BlockNumber == 0`) would incorrectly be treated as uninitialized, causing the proxy to pass through the raw tag to the full node instead of substituting block 0.

Additionally, this validity check is split across two packages: the store knows nothing about what constitutes a valid state for the proxy, and the proxy is reading internal implementation details of the store's state structure.

**Recommendation:** Use an explicit `initialized bool` field on `EspressoState`, or a dedicated `EspressoStore.IsReady() bool` method. This makes the semantics clear and removes the sentinel-zero ambiguity. The store can expose the readiness concept without the proxy having to reason about internal invariants.

---

### AR-D-6: Config struct leaks rollup-specific types into the shared config layer

**Description:** `config.go` contains `OPConfig` and `NitroConfig` as embedded structs within `Config`. The `validate()` method has mode-switched logic for each, and `toOPVerifierConfig()`/`toNitroVerifierConfig()` are methods on the shared `Config`. This means adding a third rollup type (e.g., a future "ZK" mode) requires touching the shared config struct and its `validate()` method.

**Impact:** The config is not closed for extension. Every new mode adds fields to `Config`, new cases to `validate()`, and new conversion methods. The `ModeOP`/`ModeNitro` string constants live in the same file as the `Config` struct.

**Recommendation:** Consider a small registry pattern: `Config` holds a `Mode string` and `RawExtras json.RawMessage` for mode-specific config, each mode provides its own `validate()` and construction. For the current two modes this may be over-engineering; at minimum, segregate mode-specific validation into per-mode validator functions.

---

## Scalability Concerns (AR-S-*)

### AR-S-1: File-backed store is a single writer with disk I/O on the hot path

**Description:** `EspressoStore.UpdateIfGreater` calls `writeToDisk` (a `renameio.TempFile` + `json.Encode` + `CloseAtomicallyReplace`) on every successful batch advance. At the `VerificationInterval` of 10ms (the default), under high-throughput conditions this is up to 100 disk writes per second. The `renameio` approach is safe but involves syscalls (`tmpfile`, `write`, `fsync` on some filesystems, `rename`).

**Impact:** Under sustained load on slow storage (NFS, spinning disk, encrypted volumes), the verification loop may be I/O-bound rather than network-bound. The store itself is not a bottleneck for proxy request serving (it only reads), but it becomes a bottleneck for how quickly the Espresso-finalized block advances.

**Recommendation:** Batch disk writes: only write to disk when the block number advances by at least N blocks or when a configurable `writeToDisk` interval elapses, keeping the in-memory state always current. This is safe because the README already documents that the store is a "durable cursor" for restarts, not a transaction log.

---

### AR-S-2: Full JSON decode/encode for every intercepted request

**Description:** Every HTTP request to the proxy goes through `io.ReadAll` of the body, `json.Unmarshal` into `jsonrpcv2.Request` or `[]jsonrpcv2.Request`, a recursive walk of the params tree, and `json.Marshal` back to bytes before forwarding. For requests that do not contain the espresso tag (the vast majority), this is wasted work.

**Impact:** For a JSON-RPC-heavy client (e.g., an indexer calling `eth_getLogs` with large filter results), this adds measurable CPU overhead and GC pressure on every single request, not just those containing the tag.

**Recommendation:** Add a fast-path check: scan the raw body bytes for the literal string `"espresso"` (or the configured tag) before attempting deserialization. If the tag is not present, forward the body unchanged. This pre-check can be done in O(n) with a simple `bytes.Contains` and would eliminate deserialization cost for the common case.

---

### AR-S-3: Per-request `io.ReadAll` into memory with no streaming forwarding

**Description:** `adapters/interceptor_http.go` reads the entire request body into memory with `io.ReadAll(r.Body)` before any forwarding occurs. Combined with the 5 MB default body limit, this means up to 5 MB per concurrent request is held in memory simultaneously. For a proxy serving hundreds of concurrent clients, this can be significant.

**Impact:** Memory pressure scales linearly with concurrent batch-heavy requests. This is acceptable for a typical RPC proxy but would need revisiting if this proxy were deployed in front of a high-concurrency indexing workload.

**Recommendation:** The current design is necessary for tag substitution (you cannot stream-forward until you know whether the tag is present). The fast-path from AR-S-2 (skip decode if tag absent) would allow streaming forwarding for the common case. Document the memory model explicitly.

---

## Maintainability Concerns (AR-M-*)

### AR-M-1: E2E test utility file contains hardcoded private keys and well-known test addresses

**Description:** `espresso_e2e/e2e_utils.go` contains:
```go
const loadGenKey = "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
```
This is a well-known Hardhat test private key (account index 1), and is fine for local test use. However, it is an unremarkable hex string embedded in a test utility file with no comment explaining its origin. A security scanner will flag this as a leaked private key.

**Recommendation:** Add a comment: `// This is the well-known Hardhat/Anvil test private key for account index 1. Never use in production.`

---

### AR-M-2: Error string matching in verifier is fragile

**Description:** In `op_verifier.go`, `drainAndVerifyBatches` branches on error strings:
```go
} else if err.Error() == "not found" {
} else if strings.Contains(err.Error(), "retryable") {
```
Error string matching is brittle: a library update that changes the wording of an error message silently breaks the branching logic without a compile-time failure. The "not found" check in particular is indistinguishable from many different not-found conditions.

**Recommendation:** Define sentinel errors or use `errors.Is`/`errors.As`. If the upstream library does not provide typed errors, wrap them at the point of first receipt and match on the wrapper.

---

### AR-M-3: `AutoBodyCloserMiddleware` is wired nowhere but exported

**Description:** `http/body_auto_closer.go` defines and exports `AutoBodyCloserMiddleware`, but it is not included in the `HTTPRPCMiddlewares` composition in `middlewares.go` and is not called from `main.go`. The Go `http.Server` documentation notes that the server itself closes the request body, so this middleware may be intentionally omitted, but the exported symbol suggests it was intended to be used.

**Recommendation:** Either include it in the middleware chain or unexport it (rename to `autoBodyCloserMiddleware`). If it is kept for external use by the package's stated "primarily for use by the binary" disclaimer, add a comment explaining the intended use case.

---

### AR-M-4: `FinalityPoller` initial poll delay equals one full interval

**Description:** `FinalityPoller.run` uses `time.NewTicker(p.interval)` and only polls on `ticker.C`. This means the first poll is delayed by the full interval (default 1 second). During startup, `lastSyncStatus()` in the OP verifier returns `false` until the first poll fires, causing `peekNextBatch` to fail with `"finality poller has no snapshot"` for the entire first interval.

**Impact:** On startup, the verifier cannot make progress for up to one interval. For short intervals this is acceptable; for the 1-second default it is a minor UX issue (logged as an error).

**Recommendation:** Call `p.poll(ctx)` once immediately before entering the ticker loop, so the first snapshot is available without delay.

---

### AR-M-5: Config flag and JSON key naming inconsistency

**Description:** There is a mix of naming conventions in config. Some flags use dot-prefixed namespacing (`--op.light-client-address`, `--ws.listen-addr`) while others do not (`--full-node-execution-rpc`, `--espresso-tag`). In the JSON config file, the OP sub-config is a nested object (`"op": { ... }`) but the root keys like `l1_rpc` are at the top level even though they are only required for some modes. The `l1_rpc` field is at the top level of `Config` but is only used by OP mode (Nitro constructs its own `l1_rpc` from the Nitro config).

**Impact:** The schema of the config file is confusing: `l1_rpc` appears to be a global field, but it is OP-specific in practice. A Nitro operator will set `l1_rpc` even though `toNitroVerifierConfig()` copies `c.L1RPC` directly into the Nitro config, suggesting it is shared — but there is no documentation of this dual-use.

**Recommendation:** Move `l1_rpc` into both `OPConfig` and `NitroConfig` to make the per-mode requirements explicit, or document clearly that it is a shared field.

---

### AR-M-6: `websocket_bridge.go` `WebSocketUpgrader` naming collision with the standard library

**Description:** The `http` package exports `WebSocketUpgrader(logger, upgrader)` which returns an `http.Handler`. However, `websocket.Upgrader` is also a type in the `websocket` package. In `main.go`:
```go
proxyhttp.WebSocketUpgrader(logger, reverseProxy)
```
Here `reverseProxy` is a `*websocketutil.ReverseProxy` which implements `websocket.Upgrader`. The naming is locally coherent but creates confusion because an `Upgrader` is being passed to a function called `WebSocketUpgrader` — the function wraps the upgrader into an `http.Handler`, but its name implies it _is_ the upgrader. The function is not documented with a doc comment (the doc is on the struct, not the factory function).

**Recommendation:** Rename to `NewWebSocketHandler` or `WebSocketHandler` to clearly indicate it returns an `http.Handler`, not an upgrader.

---

## Positive Architectural Decisions

**1. Generic `FinalityPoller[T]`** — Using Go generics for `FinalityPoller` is a well-judged decision. The OP verifier needs `eth.SyncStatus` while the Nitro verifier needs a `uint64` block number. The generic poller cleanly handles both without type assertions or empty interfaces at call sites. The compile-time assertion `var _ FinalityPollerInterface[any] = (*FinalityPoller[any])(nil)` is good practice throughout the codebase.

**2. Atomic file writes via `renameio`** — Using `renameio.TempFile` + `CloseAtomicallyReplace` for store persistence is the correct approach for crash-safe file updates. This prevents the proxy from ever reading a partially-written state file after an unexpected process death.

**3. Monotonic `UpdateIfGreater` semantics** — The store's `UpdateIfGreater` interface and the README's guarantee that the espresso block number never moves backwards is an excellent safety invariant. It is correctly enforced at the storage layer rather than relying on caller discipline.

**4. WebSocket abstraction layer** — The `websocket.Conn` interface with `Closer`, `Reader`, `Writer`, `ErrorChecker`, and `SubProtoRetriever` is a well-designed abstraction that successfully hides the gorilla vs coder library differences. The compile-time interface assertions (`var _ Conn = (*gorillaAdapter)(nil)`) throughout are good Go practice.

**5. Middleware composition in `HTTPRPCMiddlewares`** — The `http.HTTPRPCMiddlewares` composition function provides a single, readable assembly point for the HTTP middleware stack. The individual middleware types all implement `http.Handler` cleanly, each with a single responsibility.

**6. `ExtraFields` passthrough on JSON-RPC types** — The `ExtraFields map[string]any` on `Request`, `Response`, and `Error` ensures that non-standard fields (common in Ethereum tooling) survive the proxy's decode/re-encode cycle without modification. The double-decode pattern in `fullDecode[T]` is clever, though it doubles deserialization cost.

**7. `batchVerifier` interface in `main.go`** — The `batchVerifier` interface with `Start(ctx)` and `Stop()` is minimal and correct. It hides the concrete `OPEspressoBatchVerifier` and `NitroEspressoBatchVerifier` types from `main()`, making the mode-switching clean.

**8. E2E test coverage breadth** — The `espresso_e2e` package tests L1 reorgs, sequencer reorgs, P2P attacks, feed mismatches, and proxy restarts. Testing adversarial conditions (not just the happy path) for a finality proxy is the right investment given the security implications.

**9. Graceful shutdown with timeout** — `cleanHTTPServerShutdown` correctly runs server shutdowns in parallel with a shared 30-second context deadline, rather than sequentially. This is a small but important detail for a proxy that may have in-flight WebSocket connections.

**10. `proxy.DefaultMaxBatchSize` and `DefaultMaxRequestBodySize` as exported constants** — Exporting these defaults allows the E2E test utilities and other consumers to reference the same defaults as the binary without magic numbers, avoiding drift between production and test configurations.

---

## Issue Tracking Table

| ID | Category | Priority | Description | Recommendation |
|----|----------|----------|-------------|----------------|
| AR-C-1 | Critical | High | `Interceptor` interface defined in `adapters`, not `proxy` | Move interface to `proxy` package |
| AR-C-2 | Critical | High | `proxy.Interceptor` uses global logger instead of injected logger | Add `logger` field to `Interceptor`, use in `InterceptRequest`/`InterceptBatchRequests` |
| AR-C-3 | Critical | High | `UpdateIfGreater` has a check/write/lock race on the store | Hold write lock for the entire check-write-update sequence |
| AR-C-4 | Critical | Medium | `logger.Crit` (os.Exit) in server goroutines bypasses graceful shutdown | Replace with error channel signaling back to `main()` |
| AR-D-1 | Design | Medium | `adapters` package conflates interface definition, HTTP adapter, WS adapter, and utility | Split by concern; move `Interceptor` iface to `proxy` |
| AR-D-2 | Design | Medium | `JSONRPCHandler`/`JSONRPCBatchHandler` interfaces defined but never used | Wire or remove; `jsonrpcv2/handler.go` is untracked in git |
| AR-D-3 | Design | Medium | Both gorilla and coder WebSocket libraries present; only coder used at runtime | Remove gorilla from `go.mod` or document the split responsibility |
| AR-D-4 | Design | Low | Full JSON decode/encode on every intercept may alter numeric representation | Consider raw-bytes tag scan; or document the fidelity guarantee explicitly |
| AR-D-5 | Design | Medium | `L2BlockNumber == 0` treated as uninitialized, but 0 is a valid block number | Add `IsReady() bool` to `EspressoStore` or use an explicit `initialized` flag |
| AR-D-6 | Design | Low | `l1_rpc` at top-level `Config` is misleadingly global but is OP-specific in practice | Move into mode-specific config structs or document dual-use explicitly |
| AR-S-1 | Scalability | Medium | Disk write on every batch advance; up to 100 writes/sec at 10ms interval default | Batch disk writes by block-delta or time interval |
| AR-S-2 | Scalability | Medium | Full JSON decode/encode for every request regardless of tag presence | Fast-path `bytes.Contains` scan before deserialization |
| AR-S-3 | Scalability | Low | `io.ReadAll` into memory for all requests; no streaming forward path | Accept as-is or pair with AR-S-2 fast-path for streaming forwarding |
| AR-M-1 | Maintainability | Low | Hardcoded test private key has no origin comment | Add comment: "well-known Hardhat/Anvil test key, never use in production" |
| AR-M-2 | Maintainability | Medium | Error string matching (`err.Error() == "not found"`, `strings.Contains(..., "retryable")`) in verifier | Define or wrap sentinel errors; use `errors.Is`/`errors.As` |
| AR-M-3 | Maintainability | Low | `AutoBodyCloserMiddleware` exported but not used in middleware chain | Include in chain or unexport |
| AR-M-4 | Maintainability | Low | `FinalityPoller` first poll delayed by full interval; verifier errors on startup | Call `p.poll(ctx)` once before entering ticker loop |
| AR-M-5 | Maintainability | Low | `l1_rpc` naming inconsistency in config flags vs. actual per-mode usage | Clarify ownership in docs or struct layout |
| AR-M-6 | Maintainability | Low | `WebSocketUpgrader` function name in `http` package is misleading | Rename to `WebSocketHandler` or `NewWebSocketHandler` |

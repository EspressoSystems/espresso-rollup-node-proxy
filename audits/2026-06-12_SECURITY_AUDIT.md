# Security Audit — espresso-rollup-node-proxy

**Date:** 2026-06-12
**Branch:** main
**Commit:** e3b7705f872057453c38f01a9b5e33061cfac396
**Auditor:** Claude AI (claude-sonnet-4-6)

---

## Executive Summary

The `espresso-rollup-node-proxy` is a Go application that proxies Ethereum JSON-RPC traffic (HTTP and WebSocket) while intercepting requests to substitute an Espresso finality tag (e.g., `"espresso"`) with the current Espresso-verified block number. It consists of:

- An HTTP JSON-RPC proxy with body-size limiting, method enforcement, content-type validation, and panic recovery middleware.
- An optional WebSocket proxy using the `coder/websocket` library.
- A persistence layer (`EspressoStore`) that tracks the latest Espresso-verified L2 block number to disk and memory.
- A batch verifier (OP or Nitro mode) that polls chain state and updates the store.

**Overall security posture is reasonable for an internal infrastructure component**, with meaningful input-bound enforcement (body size, batch size, JSON nesting depth, method filtering), non-root Docker execution, atomic disk writes, and properly scoped error code classification. However, this audit identified **one confirmed race condition** in the state persistence layer that creates a risk of inconsistent disk/memory state, as well as several medium and informational findings. DoS, rate limiting, and transport-layer concerns (TLS, auth) are noted for operational awareness but are excluded from the numbered finding set per audit scope, as they represent deployment-model decisions rather than code vulnerabilities.

**Findings Summary:**

| Severity | Count |
|----------|-------|
| Critical | 0     |
| High     | 0     |
| Medium   | 1     |
| Low      | 5     |

---

## Critical Issues (SEC-C-*)

*No critical issues found.*

---

## High Issues (SEC-H-*)

*No high-severity code vulnerabilities found.*

**Operational note (out of scope for code audit):** Both the HTTP and WebSocket servers use `ListenAndServe` with no TLS and no application-layer authentication. This is architecturally consistent with infrastructure components that rely on network controls for isolation (e.g., Kubernetes network policies, VPN, firewall rules). If this service is or could be exposed beyond a trusted private network boundary, TLS termination at the ingress layer and authentication should be explicitly confirmed as controls in the deployment architecture.

---

## Medium Issues (SEC-M-*)

### SEC-M-1: TOCTOU Race Condition in `EspressoStore.UpdateIfGreater` — Disk/Memory State Divergence

**File:** `store/espresso_store.go:66–86`
**Risk Level:** Medium
**Confidence:** High (8/10)

**Description:**

`UpdateIfGreater` follows a read-check-write-update sequence that is not protected by a single continuous lock:

```go
func (es *EspressoStore) UpdateIfGreater(l2BlockNumber uint64, fallbackHotshotHeight uint64) (bool, error) {
    state := es.GetState()                          // (1) Read lock acquired + released
    if state.L2BlockNumber >= l2BlockNumber {       // (2) Comparison on unlocked copy
        return false, nil
    }
    // ... construct newState ...
    if err := es.writeToDisk(newState); err != nil { // (3) Disk write — NO lock held
        return false, ...
    }
    es.mu.Lock()                                    // (4) Write lock acquired
    defer es.mu.Unlock()
    es.state = newState                             // (5) Memory updated
    return true, nil
}
```

Between steps (1) and (4) there is no lock held. Two concurrent goroutines — call them G1 carrying block N+1 and G2 carrying block N+2 — can both read the current state (say block N), both pass the `>=` check, and then race through steps (3) and (5).

The `renameio` atomic rename means the last caller to invoke `CloseAtomicallyReplace()` (step 3) wins on disk. The last caller to acquire the write lock at step (4) wins in memory. These are two independent races with no coordination between them. Possible outcomes include:

- **Disk: N+2, Memory: N+1** — goroutine G2 wins the disk rename last; G1 wins the write lock last. After restart, `loadFromDisk` loads N+2, which is correct. But until restart, the running proxy incorrectly reports N+1 as the finalized block.
- **Disk: N+1, Memory: N+2** — goroutine G1 wins the disk rename last; G2 wins the write lock last. After restart, the proxy regresses to N+1, lower than what was believed to be committed. This is the more dangerous case: after a restart, the proxy may re-check or re-process blocks that were already finalized, or advance more slowly than expected.

**Security Impact:**

`EspressoStore` tracks the L2 block number used as the proof of Espresso finalization. The proxy's interceptor replaces the configured tag with this block number. If the stored value regresses (disk wins with a lower value after restart), the proxy will serve a stale "finalized" block number to callers that rely on `eth_getBlockByNumber("espresso", ...)` style queries. This undermines the proxy's core correctness guarantee and could be exploited by a racing verifier workload to temporarily manipulate which block is reported as Espresso-finalized.

**Recommendation:**

Hold the write lock for the entire compare-disk-write-memory-update sequence:

```go
func (es *EspressoStore) UpdateIfGreater(l2BlockNumber uint64, fallbackHotshotHeight uint64) (bool, error) {
    es.mu.Lock()
    defer es.mu.Unlock()

    if es.state.L2BlockNumber >= l2BlockNumber {
        return false, nil
    }

    newState := EspressoState{
        L2BlockNumber:         l2BlockNumber,
        FallbackHotshotHeight: fallbackHotshotHeight,
        UpdatedAt:             time.Now(),
    }

    if err := es.writeToDisk(newState); err != nil {
        return false, fmt.Errorf("failed to write updated state to disk: %w", err)
    }

    es.state = newState
    return true, nil
}
```

Also update `GetState` to be consistent (it currently takes an `RLock`, which is correct). With this change `UpdateIfGreater` holds the write lock for the disk write; callers to `GetState` will block briefly during updates, which is acceptable given the infrequency of updates relative to reads.

---

## Low / Informational Issues (SEC-L-*)

### SEC-L-1: CORS Middleware Implemented but Never Wired Into Either Server

**File:** `http/cors.go`, `http/middlewares.go:11–22`, `main.go:159–224`
**Risk Level:** Low / Informational

**Description:**

A full, RFC-conformant CORS middleware (`NewCORSMiddleware`) is implemented in `http/cors.go` with support for allowed-origins lists, method filtering, header filtering, credentials, and preflight caching. It is never instantiated or applied to either the HTTP or WebSocket server. `HTTPRPCMiddlewares` (the sole middleware aggregator called for HTTP) does not include it.

The operational effect depends on intended consumers: if the proxy is accessed from browsers (e.g., dApp frontends making cross-origin requests), CORS headers will never be set, causing browsers to block all cross-origin requests including CORS preflights. There is no way for operators to configure origin restrictions because the middleware is never called.

This is not an active security vulnerability (the absence of CORS headers is the browser-safe default), but it is dead code that exists for a reason — someone intended to use it — and the gap should be acknowledged.

**Recommendation:**

Either wire `NewCORSMiddleware` into `HTTPRPCMiddlewares` (and configure allowed origins via `Config`), or add a clear code comment explaining that CORS is intentionally not enforced and the proxy is not intended to be called from browsers.

---

### SEC-L-2: Internal Error Strings Forwarded to JSON-RPC Clients

**File:** `adapters/interceptor_http.go:40,56`, `adapters/helpers_interceptor_glue.go:65`
**Risk Level:** Low

**Description:**

Three error paths return raw Go error strings verbatim in JSON-RPC error response `message` fields:

1. `interceptor_http.go:40` — `fmt.Sprintf("failed to parse request: %s", err)` — the `io.ReadAll` error text (which may include OS-level messages) is sent to the client.
2. `interceptor_http.go:56` — `fmt.Sprintf("failed to intercept request: %s", err)` — the full wrapped error chain from the interceptor pipeline is sent to the client.
3. `helpers_interceptor_glue.go:65` — `fmt.Sprintf("failed to decode json rpc request: %s", err)` — Go's JSON parser error details including byte offsets, field names, and type information are sent to the client.

An attacker can deliberately submit malformed requests and inspect the error responses to fingerprint the proxy implementation, identify internal package names, and map the error-handling chain.

**Recommendation:**

Separate user-facing messages from internal error details. Use static user-facing strings for the JSON-RPC `message` field, and log the detailed internal error server-side at debug or warn level:

```go
// Before:
WriteJSONRPCErrorToHTTPResponseWriter(w, nil, jsonrpcv2.CodeParseError,
    fmt.Sprintf("failed to parse request: %s", err))

// After:
m.logger.Debug("failed to parse request body", "error", err)
WriteJSONRPCErrorToHTTPResponseWriter(w, nil, jsonrpcv2.CodeParseError, "parse error")
```

---

### SEC-L-3: `MarshalJSON` on `Request` Skips Filtering `jsonrpc` Field from `ExtraFields`

**File:** `jsonrpcv2/types_json_extensions.go:111–131`
**Risk Level:** Low

**Description:**

During `MarshalJSON` on `Request`, the code filters `id` and `params` from `ExtraFields` before merging them into the output map:

```go
for k, v := range r.ExtraFields {
    if k == "id" || k == "params" {
        continue
    }
    toEncode[k] = v
}
```

But `"jsonrpc"` and `"method"` are not filtered at this stage. The canonical values `toEncode["method"] = r.Method` and `toEncode["jsonrpc"] = Version` are written after the loop, so they do correctly overwrite any `ExtraFields["jsonrpc"]` or `ExtraFields["method"]` values. However, the asymmetry is confusing: during `UnmarshalJSON`, all four well-known keys (`jsonrpc`, `id`, `method`, `params`) are deleted from `ExtraFields` (lines 91–95). The `MarshalJSON` only explicitly guards against `id` and `params`. The later writes of `"method"` and `"jsonrpc"` happen to produce the correct result, but the pattern is inconsistent and could cause a subtle bug if the loop-then-overwrite pattern is ever refactored.

**Recommendation:**

For clarity and defensive correctness, filter all four well-known keys in the loop:

```go
for k, v := range r.ExtraFields {
    if k == "id" || k == "params" || k == "method" || k == "jsonrpc" {
        continue
    }
    toEncode[k] = v
}
```

---

### SEC-L-4: `config.validate()` Does Not Validate `WsFullNodeExecutionRPC`

**File:** `config.go:193–244`
**Risk Level:** Low / Informational

**Description:**

`config.validate()` validates `FullNodeExecutionRPC`, `L1RPC`, `QueryServiceURL`, and mode-specific URLs, but does not validate `WsFullNodeExecutionRPC`. When the WebSocket server is enabled (both `WsListenAddr` and `WsFullNodeExecutionRPC` are non-empty), `createWsServer` in `main.go` calls `url.Parse` on `WsFullNodeExecutionRPC` and silently disables the WebSocket server on error with only a `Warn` log message.

This means an operator with a misconfigured `ws_full_node_execution_rpc` (e.g., a typo like `"ws//..."` instead of `"ws://..."`, or an HTTP URL instead of a WS URL) gets no startup error and no indication that the WebSocket proxy is not running until clients begin failing to connect.

**Recommendation:**

Add validation of `WsFullNodeExecutionRPC` when `WsListenAddr` is non-empty:

```go
if c.WsListenAddr != "" {
    if err := validateURL("ws.full-node-execution-rpc", c.WsFullNodeExecutionRPC); err != nil {
        errs = append(errs, err)
    }
}
```

Alternatively, add scheme validation to enforce `ws://` or `wss://` scheme for the WebSocket URL.

---

### SEC-L-5: Panic Recovery May Attempt Double `WriteHeader` After Partial Response Write

**File:** `http/panic_recovery.go:27–35`
**Risk Level:** Low / Informational

**Description:**

The panic recovery deferred handler calls `http.Error(w, "internal server error", http.StatusInternalServerError)` unconditionally. `http.Error` internally calls `w.Header().Set(...)` then `w.WriteHeader(500)` then `w.Write(...)`. If a panic fires after the underlying handler has already called `w.WriteHeader()` or `w.Write()`, Go's `net/http` will silently discard the duplicate `WriteHeader` call (logging a warning to stderr) and append the `http.Error` body to whatever was already written. A partially-written JSON-RPC response followed by the ASCII string "internal server error\n" will break any downstream JSON parser.

In practice this requires a panic to fire mid-response-write (e.g., during reverse proxy response streaming), which is unlikely but not impossible, particularly during upstream connection errors that manifest as panics.

**Recommendation:**

Wrap the response writer with a `sync.Once`-guarded `WriteHeader` tracker, or check whether the response has already been committed before calling `http.Error`. A simple approach:

```go
defer func() {
    if rec := recover(); rec != nil {
        m.logger.Error("panic recovered in HTTP handler", "panic", rec, "stack", string(debug.Stack()))
        if !sw.headerWritten {
            http.Error(w, "internal server error", http.StatusInternalServerError)
        }
    }
}()
```

Where `sw` is a wrapping `ResponseWriter` that tracks whether `WriteHeader` has been called (similar to `statusResponseWriter` in `request_logger.go`).

---

## Strengths / Positive Security Practices

The following security controls were observed and are worth highlighting:

1. **Non-root Docker execution** — `Dockerfile` creates and uses a dedicated `proxyuser` (UID 1000) with no shell, following the principle of least privilege. `adduser -D -u 1000 proxyuser` is correct.

2. **Atomic disk writes** — `store/espresso_store.go` uses `github.com/google/renameio` for atomic disk updates (write to temp file, then rename). This correctly prevents half-written state files from being read on restart, and is race-safe for the disk write itself.

3. **JSON nesting depth limit** — `proxy/interceptor.go` enforces `maxJSONDepth = 32` via the recursive `replaceTagInParams`, preventing deeply nested JSON from causing stack overflows or unbounded recursion.

4. **Batch size limiting** — `InterceptBatchRequests` enforces `DefaultMaxBatchSize = 1000` and returns a well-formed JSON-RPC error (`CodeInvalidRequest`) for oversized batches.

5. **Body size limiting** — `http/body_size_limiter.go` enforces `DefaultMaxRequestBodySize = 5MB` via both `http.MaxBytesReader` (streaming enforcement) and `Content-Length` header pre-check. Both mechanisms are present, preventing bypasses.

6. **HTTP method enforcement** — `MethodIsMiddleware` enforces `POST`-only on the JSON-RPC endpoint, rejecting GET/PUT/DELETE/etc. before they reach any processing logic.

7. **Content-Type enforcement** — `ContentTypeIsJSONRPCMiddleware` validates that incoming requests present `application/json` content type, providing early rejection of non-JSON payloads.

8. **Well-structured JSON-RPC error codes** — All error paths use the correct standard JSON-RPC error codes (`-32700` for parse errors, `-32600` for invalid request, `-32603` for internal errors). Clients can distinguish parse-level failures from application-level errors.

9. **WebSocket read size limit** — `CoderUpgrader` applies `SetReadLimit` using the same `MaxRequestBodySize` config value, ensuring consistent size enforcement across both HTTP and WebSocket transports.

10. **HTTP server hardening parameters** — `createHttpServer` sets `ReadTimeout: 15s`, `ReadHeaderTimeout: 5s`, `WriteTimeout: 30s`, `IdleTimeout: 60s`, and `MaxHeaderBytes: 1MB`. These are reasonable, production-grade defaults that prevent slow-loris and header-stuffing attacks.

11. **Panic recovery middleware** — `RecoveryMiddleware` prevents a single panicking request from taking down the entire HTTP server process. The middleware correctly logs panic recovery with a stack trace to aid diagnosis.

12. **Graceful shutdown** — `cleanHTTPServerShutdown` uses a 30-second context-bounded graceful shutdown, allowing in-flight requests to complete before the server closes.

13. **Extra-field protection on response marshaling** — `Response.MarshalJSON` writes the canonical `id` and `jsonrpc` fields last, with an explicit comment noting it prevents ExtraFields from overwriting them. This correctly prevents malicious upstream responses from spoofing the `id` or `jsonrpc` fields.

14. **Finality poll timeout** — `verifier/finality_poller.go` wraps each poll call with `context.WithTimeout(ctx, 5*time.Second)`, preventing a hung upstream from blocking the poller goroutine indefinitely.

15. **Store validity check on startup** — `loadFromDisk` validates that `FallbackHotshotHeight != 0` and `UpdatedAt` is non-zero before accepting the loaded state, rejecting corrupted or partially-written state files.

---

## Recommendations Summary Table

| ID | Severity | File | Issue | Recommendation |
|----|----------|------|-------|----------------|
| SEC-M-1 | Medium | `store/espresso_store.go:66–86` | TOCTOU race in `UpdateIfGreater` — disk/memory state can diverge under concurrent updates | Hold write lock for the entire read-compare-write-update sequence |
| SEC-L-1 | Low | `http/cors.go`, `http/middlewares.go` | CORS middleware implemented but never wired in | Wire `NewCORSMiddleware` into `HTTPRPCMiddlewares` or document intentional absence |
| SEC-L-2 | Low | `adapters/interceptor_http.go:40,56`, `adapters/helpers_interceptor_glue.go:65` | Raw Go error strings returned in JSON-RPC error `message` field | Use static user-facing error messages; log internal details server-side |
| SEC-L-3 | Low | `jsonrpcv2/types_json_extensions.go:111–131` | `MarshalJSON` on `Request` inconsistently filters `ExtraFields` (misses `jsonrpc`/`method`) | Filter all four well-known keys in the ExtraFields loop |
| SEC-L-4 | Low | `config.go:193–244` | `WsFullNodeExecutionRPC` not validated at startup; misconfiguration silently disables WebSocket server | Add `validateURL` for `WsFullNodeExecutionRPC` when `WsListenAddr` is set |
| SEC-L-5 | Low | `http/panic_recovery.go:27–35` | Panic recovery may call `http.Error` after headers already written, producing garbled responses | Track `WriteHeader` state; skip `http.Error` if response already started |

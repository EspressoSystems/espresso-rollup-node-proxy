# Code Review — espresso-rollup-node-proxy

**Date:** 2026-06-12
**Branch:** main
**Commit:** e3b7705f872057453c38f01a9b5e33061cfac396
**Reviewer:** Claude AI (claude-sonnet-4-6)

---

## Executive Summary

The codebase is generally well-structured and shows real care in several areas: the WebSocket adapter layer is clean and well-tested, JSON-RPC type marshaling handles edge cases (notifications, null IDs, extra fields) thoughtfully, and the `FinalityPoller` generic design is solid. The `EspressoStore` atomic-rename persistence is a good call.

However, there are two races and a handful of logic bugs that need attention before this can be considered production-hardened. The most urgent are the TOCTOU race in `EspressoStore.UpdateIfGreater`, the `FinalityPoller.Stop` cancel-field race, an HTTP double-write-after-headers bug, and a WebSocket error-response routing bug. Several medium-severity issues — a CORS middleware that compiles and tests but is never wired into either server, unused logger fields in four middleware types, and redundant Content-Length parsing — round out the picture. Test coverage has meaningful gaps in the `FinalityPoller` and `store` packages.

---

## Critical Bugs (CR-B-*)

### CR-B-1: TOCTOU Race in `EspressoStore.UpdateIfGreater`

**Location:** `store/espresso_store.go:66-86`

**Description:** `UpdateIfGreater` calls `GetState()` (which acquires and releases `RLock`) to read the current block number, then later acquires `Lock` to write. Between those two lock acquisitions another goroutine can write a higher block number, so the `>=` guard is evaluated against a stale snapshot. If two verifier goroutines race, the lower write wins the second lock and overwrites the higher value.

```go
func (es *EspressoStore) UpdateIfGreater(l2BlockNumber uint64, fallbackHotshotHeight uint64) (bool, error) {
    state := es.GetState()           // RLock released here
    if state.L2BlockNumber >= l2BlockNumber {
        return false, nil
    }
    // <-- another writer can insert here -->
    // ...
    es.mu.Lock()
    defer es.mu.Unlock()
    es.state = newState              // may regress the stored block
    return true, nil
}
```

**Impact:** Non-monotonic block number regression in the store. The proxy can serve a stale `espresso` tag, breaking consensus assumptions.

**Fix:** Hold `mu.Lock()` for the entire read-compare-write sequence, or re-read under the write lock and abort if the condition no longer holds:

```go
es.mu.Lock()
defer es.mu.Unlock()
if es.state.L2BlockNumber >= l2BlockNumber {
    return false, nil
}
// proceed with write
```

---

### CR-B-2: `FinalityPoller.Start`/`Stop` Cancel-Field Race

**Location:** `verifier/finality_poller.go:68-88`

**Description:** `Start` does `CompareAndSwap(false, true)` atomically, but then assigns `p.cancel` on the next line — a plain struct field write with no synchronization. `Stop` checks `p.cancel != nil` from a different goroutine. If `Stop` is called between the `CompareAndSwap` and the `p.cancel =` assignment, it reads a nil `cancel` and skips the cancellation, leaving the goroutine running forever.

```go
func (p *FinalityPoller[T]) Start(ctx context.Context) {
    if !p.running.CompareAndSwap(false, true) { ... }
    ctx, p.cancel = context.WithCancel(ctx)  // unsynchronized write
    p.wg.Add(1)
    go p.run(ctx)
}

func (p *FinalityPoller[T]) Stop() {
    if !p.running.CompareAndSwap(true, false) { ... }
    if p.cancel != nil {  // unsynchronized read — data race
        p.cancel()
    }
    p.wg.Wait()
}
```

**Impact:** Data race (detectable by `-race`). Under adversarial scheduling, `Stop` returns without stopping the background goroutine, causing a goroutine leak and continued polling after shutdown.

**Fix:** Protect `p.cancel` with a mutex or replace it with an `atomic.Pointer[context.CancelFunc]`. The simplest fix is to add a `mu sync.Mutex` that wraps both the `cancel` assignment in `Start` and the read in `Stop`.

---

### CR-B-3: HTTP Double-Write After Headers Sent

**Location:** `adapters/helpers_jsonrpcv2_http.go:14-24`

**Description:** `WriteJSONRPCResponseToHTTPResponseWriter` calls `w.Header().Set(...)`, then `enc.Encode(response)`. The first byte written by `Encode` implicitly calls `WriteHeader(200)`. If `Encode` fails partway through (e.g., a network error or an unencodable value in `Result`), the function then calls `http.Error(w, ..., http.StatusInternalServerError)`, which tries to set headers and write a new status code on a response that is already committed. The net effect is a corrupted response body: the partial JSON followed by the error string.

```go
func WriteJSONRPCResponseToHTTPResponseWriter(w http.ResponseWriter, response jsonrpcv2.Response) {
    w.Header().Set("Content-Type", "application/json")
    enc := json.NewEncoder(w)
    if err := enc.Encode(response); err != nil {
        log.Error("failed to encode JSON-RPC response", "error", err)
        http.Error(w, "failed to send response", http.StatusInternalServerError) // headers already sent
    }
}
```

**Impact:** Corrupted responses to clients on encoding failures. The `http.Error` call emits a superfluous log warning from the Go stdlib (`http: superfluous response.WriteHeader call`).

**Fix:** Buffer the encoded response first (`bytes.Buffer`), then write atomically. If encoding fails, return the error before touching the `ResponseWriter`.

---

### CR-B-4: WebSocket Error Response Written to Wrong Connection

**Location:** `adapters/interceptor_websocket.go:71-93`

**Description:** `webSocketJSONRPCDownstreamIntercept.Read` is called by the TeeReader in the context of the reverse proxy. When `PerformRequestIntercept` fails, the error response is written to `i.Conn` — which is the **downstream** client connection (from the client's perspective, this is the connection the client opened to the proxy). The `Read` method then also returns an error to its caller. The caller (the `teeReader`) will propagate that error, which may trigger closing both upstream and downstream connections. The client ends up receiving the JSON-RPC error response AND then an abrupt close, rather than getting the error response and continuing normally.

**Impact:** Intercept errors terminate the WebSocket session rather than returning a clean JSON-RPC error and allowing the client to continue.

**Fix:** On intercept error, write the JSON-RPC error response to `i.Conn` (correct) but return `nil` error to the caller so the session is not torn down, unless the error is truly unrecoverable. Or return a custom sentinel error the caller can recognize as "error already written, keep session alive."

---

## Error Handling Issues (CR-E-*)

### CR-E-1: `InterceptBatchRequests` Returns Partial State on Per-Item Error

**Location:** `proxy/interceptor.go:111-116`

**Description:** When `interceptRequest` fails on a specific element within a batch, the function returns the **original unmodified `requests` slice** alongside the error. The error is then propagated up through `requestInterceptorGlue` → `ServeHTTP`, which correctly rejects it. However, the return value contract is misleading: the function signature implies the returned slice is either fully processed or nil on error. Callers that check `err != nil` and still use the returned slice (reasonable given the dual return) could process a partially-intercepted batch.

```go
for j, req := range requests {
    r, err := i.interceptRequest(req, finalizedEspressoBlockNumber)
    if err != nil {
        return requests, err  // returns original, not next (partially built)
    }
    next[j] = r
}
```

**Fix:** Return `nil` (not `requests`) on error to make it unambiguous that the output is unusable.

---

### CR-E-2: `errors.Join` + `errors.As` Chain May Fail to Extract `jsonrpcv2.Error`

**Location:** `proxy/interceptor.go:95-100`, `adapters/interceptor_http.go:46-48`, `adapters/interceptor_websocket.go:79-82`

**Description:** `InterceptBatchRequests` produces `errors.Join(ErrMaxBatchSizeExceeded, jsonrpcv2.Error{...})`. This joined error is then wrapped by `requestInterceptorGlue` via `WrapErr("...", err)` which uses `fmt.Errorf("%s: %w", ...)`. The resulting chain is: `fmt.Errorf` wrapping `errors.Join` wrapping `jsonrpcv2.Error`. `errors.As` on this chain should work per Go 1.20 semantics (it traverses joined errors), but `jsonrpcv2.Error` implements `error` with a **value receiver**, and the `errors.As` call uses `var jsonRPCError jsonrpcv2.Error` (not a pointer). This is unconventional and worth verifying carefully; a regression in any intermediate error-wrapping layer could silently cause the `errors.As` to fall through to the `CodeInternalError` path, leaking the internal error message to the client.

**Fix:** Add a targeted unit test that drives the full path: `InterceptBatchRequests` exceeding the limit → `requestInterceptorGlue` → `errors.As(err, &jsonRPCError)` succeeds with `Code == CodeInvalidRequest`. This path currently has no direct test.

---

### CR-E-3: `mustNewOPVerifier` Logs Error Without Including `err` for Light Client Failure

**Location:** `main.go:46-49`

**Description:** The light-client failure branch does not include the error in the log message:

```go
lc, err := espressoLightClient.NewLightclientCaller(cfg.OPConfig.LightClientAddress, l1Client)
if err != nil || lc == nil {
    logger.Crit("failed to create light client")  // err not logged
    os.Exit(1)
}
```

**Impact:** Startup failures due to light client creation are silent — the operator has no actionable error message.

**Fix:** `logger.Crit("failed to create light client", "error", err)`.

---

### CR-E-4: `createHttpServer` Uses `panic` for a Validatable Condition

**Location:** `main.go:160-163`

**Description:** `url.Parse` is called on `cfg.FullNodeExecutionRPC` and panics on failure. This URL is already validated by `cfg.validate()` before `createHttpServer` is called, so the panic is unreachable in practice — but if the call order ever changes, it becomes a silent crash with a non-descriptive stack trace rather than a graceful `logger.Crit`.

**Fix:** Replace with `logger.Crit(...)` + `os.Exit(1)` to match the rest of the startup error pattern, or rely entirely on the pre-validated URL and remove the error check (since `url.Parse` of a non-empty string with a valid scheme cannot fail for HTTP URLs).

---

### CR-E-5: `WriteJSONRPCResponseToWebSocket` Ignores Write-Error Context

**Location:** `adapters/helpers_jsonrpcv2_websocket.go:22`

**Description:** The write call uses `context.Background()` rather than the context carried by the originating `Read` call. If the connection context has been cancelled (e.g., the client disconnected), the write will block until the WebSocket library times out internally or returns its own error.

**Fix:** Thread the calling context through to `WriteJSONRPCResponseToWebSocket` and `WriteJSONRPCErrorToWebSocket`, or use a short-timeout derived context.

---

## Concurrency / Race Conditions (CR-C-*)

### CR-C-1: Gorilla Context-Cancellation Goroutine Can Race Across Calls

**Location:** `websocket/gorilla.go:87-99`, `websocket/gorilla.go:116-128`

**Description:** Both `Read` and `Write` spawn a goroutine that calls `SetReadDeadline(time.Now())` or `SetWriteDeadline(time.Now())` when the context is cancelled. The `done` channel ensures the goroutine exits if the method returns before cancellation. However, if context cancellation fires extremely late — just after the `defer close(done)` races with the goroutine's `case <-ctx.Done()` — the goroutine can set the deadline *after* a subsequent `Read`/`Write` call has already started on the same connection. The Gorilla library does not serialize `SetReadDeadline` and `ReadMessage` calls, so this is a potential data race on the deadline field.

**Impact:** Intermittent deadline corruption under high concurrency. Likely not triggered in practice given typical usage patterns, but detectable with `-race` on a loaded server.

**Fix:** Ensure the goroutine checks a cancelled context flag after acquiring the same lock that guards the underlying `ReadMessage`/`WriteMessage` call, or accept the current approach as a known limitation and document it.

---

### CR-C-2: `FinalityPoller` No Initial Poll on Start

**Location:** `verifier/finality_poller.go:103-115`

**Description:** `run` uses `time.NewTicker` and only polls on `ticker.C`. The first poll therefore happens after a full `interval` delay (default 1 second, configurable). During startup, `LastSnapshot()` returns `(zero, false)` for the entire first interval, causing the interceptor to fall through to `ErrUnknownEspressoFinalizedBlockNumber` and log warnings for that duration on every incoming request.

**Impact:** Every request during the first polling interval logs a warning and passes through unintercepted. Not a correctness bug (pass-through is safe), but it creates log noise and an observable service quality gap at startup.

**Fix:** Call `p.poll(ctx)` once immediately before starting the ticker loop.

---

## Resource Management (CR-R-*)

### CR-R-1: `startHTTPServers` Goroutine Calls `logger.Crit` / `os.Exit` Directly

**Location:** `main.go:241-251`

**Description:** If `ListenAndServe` returns a non-`ErrServerClosed` error (e.g., address already in use), the goroutine calls `logger.Crit(...)` which internally calls `os.Exit(1)`. As the comment acknowledges, this skips `cleanHTTPServerShutdown`, `fullNodeVerifier.Stop()`, and any deferred cleanup. The `EspressoStore` write buffer and open WebSocket connections are abandoned without a graceful close.

**Impact:** Loss of graceful shutdown on server bind errors. `EspressoStore` state is still safe due to atomic rename, but in-flight requests are dropped silently.

**Fix:** Send the error on a channel that `main` selects on alongside the signal channel, then fall through to the existing graceful shutdown path.

---

### CR-R-2: Reverse Proxy Background Goroutine Uses Request Context

**Location:** `websocket/websocketutil/reverse_proxy.go:108-127`

**Description:** The goroutine that bridges upstream→downstream messages is passed `ctx := req.Context()`. The HTTP request context is typically cancelled when the HTTP handler returns. In a WebSocket upgrade scenario, the handler (`websocketUpgrader.ServeHTTP`) returns only after `ReadAllMessages` finishes — which is the *downstream* read loop. The upstream→downstream goroutine uses the same context. This creates a subtle ordering dependency: if the downstream loop finishes and the handler returns, the request context cancels, which cancels the upstream goroutine's reads even if there are still messages in flight.

In practice the goroutine's `ReadAllMessages` will error out quickly once the connection closes, so this is low severity. But it is fragile and could cause race-condition-style errors under certain close sequences.

**Fix:** Use `context.Background()` for the background bridge goroutine and rely on the connection close (via `MultiCloser`) to terminate it, rather than context cancellation.

---

### CR-R-3: `startTestProxy` in E2E Utils Ignores Server Error

**Location:** `espresso_e2e/e2e_utils.go:474`

```go
go func() { _ = server.Serve(listener) }()
```

**Description:** Test-only code, but the error from `Serve` is silently discarded. If the server fails to start (e.g., listener already closed), tests will hang or produce confusing results rather than failing immediately.

**Fix:** `go func() { if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) { t.Errorf("proxy server error: %v", err) } }()` (requires passing `t` into the closure, which is safe in tests).

---

## Test Coverage Gaps (CR-T-*)

### CR-T-1: `FinalityPoller` Has Zero Unit Tests

**Location:** `verifier/finality_poller.go` — no corresponding `*_test.go` exists anywhere.

**Description:** The poller is the only component that drives the store forward in production. There are no unit tests for: Start/Stop lifecycle, the `LastSnapshot` availability after a poll, the `running` guard against double-Start, or the `interval == 0` default substitution.

**Fix:** Add tests covering: single poll produces a snapshot, double-Start is a no-op, Stop terminates the goroutine (verified via `wg.Wait` timing), and `LastSnapshot` returns `false` before first poll.

---

### CR-T-2: `EspressoStore.UpdateIfGreater` Race Not Tested

**Location:** `store/espresso_store_test.go`

**Description:** The existing tests are all single-goroutine. The TOCTOU race described in CR-B-1 is not exercised by any test. The store is a critical shared-state component used by both the verifier and the interceptor.

**Fix:** Add a concurrent test: N goroutines each calling `UpdateIfGreater` with monotonically increasing values; assert the final stored block equals the largest value submitted and the state never regresses.

---

### CR-T-3: WebSocket Interceptor Error Path Untested

**Location:** `adapters/interceptor_websocket.go` — no test file exists for this package's WebSocket path.

**Description:** The `webSocketJSONRPCDownstreamIntercept.Read` error handling — both the `jsonrpcv2.Error` path and the fallback path — is not covered by any test. The behavior described in CR-B-4 (error written to wrong connection direction) would only be caught by a test that verifies what is written to `i.Conn` on intercept failure.

**Fix:** Add unit tests using `fakeWebSocketConn`-style mocks that verify: (a) intercept errors result in a JSON-RPC error frame sent to the connection, (b) the returned error stops the session as intended.

---

### CR-T-4: `InterceptBatchRequests` Error Return Value Untested

**Location:** `proxy/interceptor.go:92-121`

**Description:** The test in `proxy/interceptor_test.go` checks that a batch exceeding max size returns an `InvalidRequest` error code. It does not check that the returned slice is `nil` (it's `requests` per CR-E-1), nor does it test per-element intercept failure returning `requests` vs. `next`.

**Fix:** Add assertions on the returned slice value when `InterceptBatchRequests` returns an error.

---

### CR-T-5: `FinalityPoller` No-Initial-Poll Behavior Untested

**Location:** `verifier/finality_poller.go`

**Description:** Related to CR-C-2: there is no test that verifies `LastSnapshot` returns `(zero, false)` immediately after `Start` but before the first tick, which is the behavior that produces startup log warnings.

---

## Code Quality / Maintainability (CR-Q-*)

### CR-Q-1: CORS Middleware Is Implemented, Tested, but Never Used in Production

**Location:** `http/cors.go`, `main.go`

**Description:** `NewCORSMiddleware` and its full option set are defined and thoroughly tested, but neither `createHttpServer` nor `createWsServer` in `main.go` wires it in. The HTTP and WebSocket servers accept requests from any origin without CORS headers. This means browser-based clients cannot make cross-origin requests to the proxy.

**Fix:** Either add `NewCORSMiddleware` to `HTTPRPCMiddlewares` (and expose a config option for allowed origins), or document explicitly that CORS is intentionally omitted (e.g., proxy is not meant to be accessed directly from browsers). As written, the implementation is dead code.

---

### CR-Q-2: `AutoBodyCloserMiddleware` Is Implemented but Never Used

**Location:** `http/body_auto_closer.go`, `http/middlewares.go`

**Description:** `AutoBodyCloserMiddleware` is defined and tested but not applied in `HTTPRPCMiddlewares` or anywhere in the production stack. Per Go's `net/http` documentation, the server closes the request body automatically; the middleware comment acknowledges this but still implements it. Since it is never used, it is dead code.

**Fix:** Either remove it or add it to the middleware chain if there is a specific reason it was written (e.g., early close for streaming requests).

---

### CR-Q-3: Unused Logger Fields in `httpBodySizeLimiterMiddleware` and `httpEnsureContentTypeIsJSONRPCMiddleware`

**Location:** `http/body_size_limiter.go:16`, `http/content_type_json_rpc.go:14`

**Description:** Both structs store a `logger log.Logger` field, and their constructor functions accept a `logger` parameter. Neither `ServeHTTP` implementation ever calls `m.logger`. The logger is accepted for API consistency with other middlewares, but this is misleading — callers pass a logger expecting it to be used, and it is silently dropped.

**Fix:** Either remove the `logger` parameter and field from both, or add at least debug-level logging (e.g., log rejected content types or oversized bodies).

---

### CR-Q-4: Redundant Content-Length Parsing in `httpBodySizeLimiterMiddleware`

**Location:** `http/body_size_limiter.go:33-48`

**Description:** The middleware checks `r.ContentLength > m.maxRequestBodySize` (line 33), then also manually re-parses `r.Header.Get("Content-Length")` and checks again (lines 38-48). The Go HTTP server populates `r.ContentLength` from the `Content-Length` header during request parsing. The two checks are therefore equivalent for well-formed requests. The manual re-parse adds an inconsistency: if the header value is not a valid integer, it returns `StatusLengthRequired` (411) — but `r.ContentLength` would be -1 in that case, which would not trigger the first check. This creates a test-visible behavioral difference (`TestBodySizeLimiterDetectsContentLengthHeaderIsInvalid`) but the behavior (rejecting malformed `Content-Length`) is only reachable if the Go HTTP server somehow accepted a request with an unparseable `Content-Length` — which it does not do.

**Fix:** Remove the manual header re-parse. The `r.ContentLength` check plus `http.MaxBytesReader` is sufficient.

---

### CR-Q-5: `healthCheckHandler` Uses `http.Error` for a 200 OK Response

**Location:** `main.go:131`

```go
func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
    http.Error(w, "OK", http.StatusOK)
}
```

**Description:** `http.Error` sets `Content-Type: text/plain; charset=utf-8` and `X-Content-Type-Options: nosniff` and appends a newline to the body. For a health check endpoint this is harmless, but semantically `http.Error` is for error responses. The intent is clear but the usage is subtly wrong.

**Fix:** `w.WriteHeader(http.StatusOK); _, _ = w.Write([]byte("OK"))` or just `fmt.Fprint(w, "OK")`.

---

### CR-Q-6: Four Comment Blocks Say "ServeHttp" Instead of "ServeHTTP"

**Location:**
- `http/body_size_limiter.go:20`
- `http/panic_recovery.go:19`
- `http/request_logger.go:30`
- `http/body_auto_closer.go:16`

**Description:** All four read `// ServeHttp implements http.Handler` but the method name is `ServeHTTP`. These are doc comments on the implementing method. `godoc` and IDEs will not link them correctly.

**Fix:** s/ServeHttp/ServeHTTP/ in all four files.

---

### CR-Q-7: Truncated Type Doc Comment on `httpPanicRecoveryMiddleware`

**Location:** `http/panic_recovery.go:10`

**Description:** The type-level doc comment begins mid-sentence:

```go
// that may occur in the process of processing a request.
```

The opening clause is missing (likely "// httpPanicRecoveryMiddleware is a middleware that recovers from panics").

**Fix:** Restore the full sentence: `// httpPanicRecoveryMiddleware is a middleware that recovers from panics that may occur in the process of processing a request.`

---

### CR-Q-8: `ErrCloseFailed` Declared in Two Files in the Same Test Package

**Location:** `http/body_auto_closer_test.go:102`, `http/websocket_bridge_test.go:99` (reference)

**Description:** `ErrCloseFailed` is declared in `body_auto_closer_test.go` and used in `websocket_bridge_test.go`. Both files are in `package http_test`. The variable is shared implicitly. If either test file is moved or the declaration is refactored, compilation will break in non-obvious ways.

**Fix:** Move `ErrCloseFailed` (and any other shared test utilities) to a dedicated `http_test_helpers_test.go` file in the same package, with a comment noting it is shared.

---

### CR-Q-9: `go.mod` Declares `go 1.25.1`

**Location:** `go.mod:3`

**Description:** As of the review date, Go 1.25 has not been released (latest stable is 1.24.x). The directive `go 1.25.1` is either a pre-release version or a typo for `go 1.24.1`. The `go` directive in `go.mod` affects language feature availability and toolchain selection; an incorrect value can cause compatibility issues with CI toolchains and `go install` invocations.

**Fix:** Verify the intended minimum Go version and update accordingly (`go 1.24.1` if that is the target).

---

### CR-Q-10: `InterceptBatchRequests` Returns Unmodified `requests` on Error — Inconsistent with Single-Request Path

**Location:** `proxy/interceptor.go:113-115`

**Description:** The single-request path (`InterceptRequest`) returns the unmodified `request` on `ErrUnknownEspressoFinalizedBlockNumber` (pass-through). The batch path returns the original `requests` on that same condition — but on a *per-element* `interceptRequest` error it also returns the original `requests` rather than `nil`. This makes the return value semantics inconsistent and harder to reason about.

**Fix:** Return `nil, err` on any error path where the returned slice is not safe to use.

---

### CR-Q-11: `validateAddressString` vs. `validateAddress` Asymmetry for Nitro vs. OP

**Location:** `config.go:176-191`

**Description:** OP addresses are validated as `common.Address` (already parsed by pflag's `TextVar`) via `validateAddress`. Nitro batcher addresses are stored as `string` in `BatcherAddressConfig` and validated by `validateAddressString` using `common.IsHexAddress`. The `common.Address` type enforces checksum and length at unmarshal time; the string path only checks `IsHexAddress` which accepts non-checksummed addresses and does not normalize them. Two valid representations of the same address ("0xabc..." lowercase vs. checksummed) would compare as different strings later.

**Fix:** Parse Nitro batcher address strings into `common.Address` during config validation (or store them as `common.Address` directly).

---

### CR-Q-12: Hardcoded Private Key in E2E Test Load Generator

**Location:** `espresso_e2e/e2e_utils.go:680`

```go
const loadGenKey = "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
```

**Description:** This is a well-known Hardhat/Anvil test key (account index 1), so it poses no real security risk in the test context. However, it will trigger secret-scanning tools (GitHub Advanced Security, gitleaks, etc.) and fail CI pipelines configured with those checks.

**Fix:** Add a `//nolint:gosec` or equivalent comment, or store the key in a test fixtures file explicitly marked as test-only with a `// This is a public test key` comment.

---

### CR-Q-13: Proxy Test File Comment Acknowledges Tests Are Misplaced

**Location:** `proxy/proxy_test.go:44-48`

**Description:** The comment on `TestServe` explicitly states these are pseudo-integration tests that should be relocated:

```go
// TODO: These tests are **NOT** isolated to the Proxy type and its behavior
// directly, but instead act as some psuedo integration tests for combining
// multiple components together.
// As such, it is recommended that we should probaly relocate them...
```

**Fix:** Relocate the integration-style tests to an `integration/` or `proxy_integration_test.go` file, and add true unit tests for `Interceptor` in isolation.

---

### CR-Q-14: `mockLightClient.FinalizedState` Uses Package-Level `context.Background()`

**Location:** `espresso_e2e/e2e_utils.go:56`

**Description:** The `FetchLatestBlockHeight` call ignores the `CallOpts` context and uses `context.Background()`. In a test that times out, this call will block indefinitely rather than respecting the test deadline.

**Fix:** Use `opts.Context` if non-nil, falling back to `context.Background()`.

---

## Positive Highlights

**Clean WebSocket abstraction layer.** The `websocket.Conn` interface, the Gorilla and Coder adapters, and the `teeReader` / `ReverseProxy` composition are well-designed. The compile-time interface assertions (`var _ Conn = (*gorillaAdapter)(nil)`) are present throughout. The option-function pattern for `UpgradeConfig` and `DialerConfig` is idiomatic and extensible.

**`jsonrpcv2` marshaling handles edge cases correctly.** Notification IDs (omitting the `id` field entirely), `null` IDs (preserved as explicit JSON null), and extra/unknown fields are all handled faithfully. The double-decode approach via `fullDecode[T]` is clever and avoids reflection-heavy approaches.

**`EspressoStore` uses atomic rename for durability.** The `renameio.TempFile` + `CloseAtomicallyReplace` pattern is the right call for a critical state file; it avoids partial writes on crash.

**Config validation is comprehensive.** `cfg.validate()` checks all required fields, validates URL syntax (including scheme and host), validates Ethereum addresses, and reports all errors at once via `errors.Join`. The corresponding tests cover valid and invalid cases thoroughly.

**Graceful shutdown is wired end-to-end.** `cleanHTTPServerShutdown` uses a 30-second timeout context and shuts down both servers in parallel via goroutines. Signal handling covers both `SIGINT` and `SIGTERM`.

**`FinalityPoller` generic design is clean.** Using `atomic.Pointer[T]` for the snapshot avoids a mutex for the common read path. `CompareAndSwap` guards for double-Start/Stop are correct in intent (modulo the race in CR-B-2).

**Middleware chain is composed cleanly.** `HTTPRPCMiddlewares` assembles the chain in the right order (content type → body limit → method check → panic recovery) with the conditional body-size check when the limit is 0.

---

## Issue Tracking Table

| ID | Category | Severity | File:Line | Issue | Fix |
|----|----------|----------|-----------|-------|-----|
| CR-B-1 | Bug | Critical | `store/espresso_store.go:66` | TOCTOU race in `UpdateIfGreater` — read under RLock, write under separate Lock | Hold write lock for entire read-compare-write |
| CR-B-2 | Bug | Critical | `verifier/finality_poller.go:73` | `p.cancel` assigned outside atomic scope — data race with `Stop` | Protect with mutex or `atomic.Pointer` |
| CR-B-3 | Bug | High | `adapters/helpers_jsonrpcv2_http.go:17` | `http.Error` called after `Encode` has committed headers | Buffer response before writing |
| CR-B-4 | Bug | High | `adapters/interceptor_websocket.go:81` | Error response written to wrong WebSocket leg; session torn down unnecessarily | Return nil error after writing error response to allow session to continue |
| CR-E-1 | Error Handling | Medium | `proxy/interceptor.go:114` | `InterceptBatchRequests` returns `requests` (not `nil`) on per-item error | Return `nil, err` on all error paths |
| CR-E-2 | Error Handling | Medium | `adapters/interceptor_http.go:47` | `errors.As` on `errors.Join` wrapped chain — semantics untested | Add targeted unit test for this error path |
| CR-E-3 | Error Handling | Low | `main.go:47` | Light-client failure doesn't log the actual error | Add `"error", err` to `logger.Crit` |
| CR-E-4 | Error Handling | Low | `main.go:162` | `panic` used for a URL-parse failure that is already validated | Replace with `logger.Crit` |
| CR-E-5 | Error Handling | Low | `adapters/helpers_jsonrpcv2_websocket.go:22` | `context.Background()` used for write on cancelled connection | Thread calling context through |
| CR-C-1 | Concurrency | Medium | `websocket/gorilla.go:88,117` | Deadline-setting goroutine can fire on subsequent Read/Write call | Document or serialize deadline mutations |
| CR-C-2 | Concurrency | Low | `verifier/finality_poller.go:103` | No immediate poll on Start — first poll delayed by full interval | Call `p.poll(ctx)` before entering ticker loop |
| CR-R-1 | Resource Mgmt | High | `main.go:248` | `logger.Crit` in server goroutine calls `os.Exit`, bypassing graceful shutdown | Send error on channel; handle in `main` |
| CR-R-2 | Resource Mgmt | Low | `websocket/websocketutil/reverse_proxy.go:108` | Background goroutine tied to HTTP request context which may cancel prematurely | Use `context.Background()` for bridge goroutine |
| CR-R-3 | Resource Mgmt | Low | `espresso_e2e/e2e_utils.go:474` | Test proxy server error silently discarded | Log error through `t.Errorf` |
| CR-T-1 | Test Coverage | High | `verifier/finality_poller.go` | Zero unit tests for `FinalityPoller` | Add lifecycle, snapshot, and double-start tests |
| CR-T-2 | Test Coverage | High | `store/espresso_store_test.go` | No concurrent test for `UpdateIfGreater` race | Add N-goroutine concurrent update test |
| CR-T-3 | Test Coverage | Medium | `adapters/interceptor_websocket.go` | WebSocket intercept error path untested | Add mock-based tests for error response routing |
| CR-T-4 | Test Coverage | Low | `proxy/interceptor_test.go` | Batch error return value not asserted | Assert returned slice is `nil` on error |
| CR-T-5 | Test Coverage | Low | `verifier/finality_poller.go` | No-initial-poll startup behavior untested | Test `LastSnapshot` returns false before first tick |
| CR-Q-1 | Quality | High | `http/cors.go`, `main.go` | CORS middleware implemented and tested but never wired into either server | Wire into `HTTPRPCMiddlewares` or document as intentionally omitted |
| CR-Q-2 | Quality | Medium | `http/body_auto_closer.go` | `AutoBodyCloserMiddleware` implemented but never used | Remove or wire in |
| CR-Q-3 | Quality | Low | `http/body_size_limiter.go:16`, `http/content_type_json_rpc.go:14` | Logger fields stored but never read | Remove unused field+parameter or add logging |
| CR-Q-4 | Quality | Low | `http/body_size_limiter.go:38` | Manual `Content-Length` header re-parse is redundant with `r.ContentLength` | Remove header re-parse |
| CR-Q-5 | Quality | Low | `main.go:131` | `http.Error` misused for 200 OK response | Use `w.WriteHeader` + `fmt.Fprint` |
| CR-Q-6 | Quality | Low | 4 files in `http/` | Doc comments say `ServeHttp` instead of `ServeHTTP` | s/ServeHttp/ServeHTTP/ |
| CR-Q-7 | Quality | Low | `http/panic_recovery.go:10` | Type doc comment starts mid-sentence | Restore opening clause |
| CR-Q-8 | Quality | Low | `http/body_auto_closer_test.go:102` | `ErrCloseFailed` shared implicitly across test files | Move to shared test helper file |
| CR-Q-9 | Quality | Medium | `go.mod:3` | `go 1.25.1` is not a released Go version | Verify and correct to `go 1.24.x` |
| CR-Q-10 | Quality | Low | `proxy/interceptor.go:114` | Inconsistent nil vs. original-slice return on error across batch vs. single paths | Normalize to `nil` on error |
| CR-Q-11 | Quality | Low | `config.go:227` | Nitro batcher addresses stored as strings, validated loosely; OP addresses use `common.Address` | Parse to `common.Address` for Nitro as well |
| CR-Q-12 | Quality | Info | `espresso_e2e/e2e_utils.go:680` | Hardcoded test private key will trigger secret-scanners | Add nolint comment or fixture annotation |
| CR-Q-13 | Quality | Info | `proxy/proxy_test.go:44` | Integration tests acknowledge they are misplaced | Relocate to integration package |
| CR-Q-14 | Quality | Info | `espresso_e2e/e2e_utils.go:56` | `context.Background()` in mock ignores test deadline | Use `opts.Context` |

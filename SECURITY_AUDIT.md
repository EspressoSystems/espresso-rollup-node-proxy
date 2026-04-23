# Security Audit Report: Espresso Rollup Node Proxy

**Date**: 2026-04-22  
**Scope**: Full codebase at `espresso-rollup-node-proxy`  
**Auditor**: Automated deep audit (4 parallel analysis vectors + manual code review)

---

## Executive Summary

The Espresso Rollup Node Proxy is a Go service that intercepts JSON-RPC calls to enforce Espresso finality on rollup full nodes. This audit identified **23 findings** across security, consensus logic, infrastructure, and supply chain dimensions.

| Severity | Count |
|----------|-------|
| Critical | 4 |
| High     | 6 |
| Medium   | 10 |
| Low      | 3 |
| **Total** | **23** |

The most impactful issues are: complete lack of authentication/rate-limiting on the HTTP proxy, unbounded request body reads enabling OOM denial-of-service, missing HTTP server timeouts enabling slowloris attacks, unrestricted RPC method forwarding to the backend node, and non-atomic state updates in the verifier that can cause finality regressions under concurrency.

---

## CRITICAL Findings

### C-01: No Authentication on HTTP/RPC Endpoints

**Files**: `main.go:66-78`, `proxy/proxy.go:45-63`  
**Component**: HTTP Server / Proxy

The HTTP server binds to `:8080` with **zero authentication**. Every JSON-RPC request hitting `/` is forwarded to the full node without any credential check, API key validation, or client identity verification.

```go
// main.go:66-74
mux := http.NewServeMux()
mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    _, err := w.Write([]byte("OK"))
    // ...
})
mux.HandleFunc("/", fullNodeProxy.Serve) // No auth middleware
```

**Impact**: Any network-reachable client can send arbitrary RPC calls through the proxy, including potentially dangerous admin/debug methods on the full node.

**Recommendation**: Add authentication middleware (JWT, API key, or mTLS) before the proxy handler.

---

### C-02: Unrestricted RPC Method Forwarding

**File**: `proxy/interceptor.go:119-148`  
**Component**: Interceptor

The interceptor only replaces the espresso tag in parameters. It **never filters or validates** the `Method` field. All RPC methods -- including `admin_*`, `debug_*`, `miner_*`, `personal_*`, `eth_sendTransaction`, `eth_sign` -- are transparently forwarded to the full node.

```go
// interceptor.go:119-148 -- replaceEspressoTag
func (i *Interceptor) replaceEspressoTag(rawRequest []byte, blockNumber uint64) ([]byte, bool, error) {
    var req JSONRPCRequest
    if err := json.Unmarshal(rawRequest, &req); err != nil {
        return nil, false, fmt.Errorf("failed to parse JSON-RPC request: %w", err)
    }
    // req.Method is NEVER checked or filtered
    if req.Params == nil {
        return rawRequest, false, nil
    }
    // ... only processes params, forwards everything
}
```

**Impact**: If the full node exposes admin or debug namespaces, an attacker can execute arbitrary administrative operations through the proxy (e.g., `admin_addPeer`, `debug_setHead`, `personal_unlockAccount`).

**Recommendation**: Implement an allowlist of permitted RPC methods. Deny by default.

---

### C-03: Unbounded Request Body Read (OOM DoS)

**File**: `proxy/proxy.go:46`  
**Component**: Proxy Handler

`io.ReadAll(r.Body)` reads the entire request body into memory with **no size limit**. An attacker can send a multi-gigabyte request to exhaust server memory.

```go
// proxy/proxy.go:45-51
func (p *Proxy) Serve(w http.ResponseWriter, r *http.Request) {
    body, err := io.ReadAll(r.Body) // No size limit
    if err != nil {
        log.Error("failed to read request body", "error", err)
        writeJSONRPCError(w, nil, PARSE_ERROR_CODE, "failed to read request body")
        return
    }
    // ...
}
```

**Impact**: Trivial denial-of-service. A single curl command with a large payload crashes the proxy.

**Recommendation**: Use `io.LimitReader`:
```go
body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024)) // 10MB limit
```

---

### C-04: Non-Atomic State Update in Verifier (Finality Regression)

**File**: `verifier/op/op_verifier.go:375-396`  
**Component**: OPEspressoBatchVerifier

The `advanceStreamerAndEspressoState` function performs a read-check-update-advance sequence across **separate lock acquisitions**. The store's `GetState()` at line 379 and `Update()` at line 387 are not within the same critical section. Between these calls, another goroutine or a concurrent verification tick could modify the store.

```go
// op_verifier.go:375-396
func (v *OPEspressoBatchVerifier) advanceStreamerAndEspressoState(ctx context.Context, blockNumber uint64, ethFinalizedBlockNumber uint64) error {
    hotshotFallbackPos := v.streamer.GetFallbackHotshotPos()
    blockNumberToStore := v.blockNumberToStore(blockNumber, ethFinalizedBlockNumber)

    espressoState := v.espressoStore.GetState() // READ (lock 1)
    if espressoState.L2BlockNumber >= blockNumberToStore {
        v.streamer.Next(ctx) // Advances streamer even when skipping store update
        return nil
    }

    err := v.espressoStore.Update(blockNumberToStore, hotshotFallbackPos) // WRITE (lock 2)
    if err != nil {
        return err
    }

    v.streamer.Next(ctx)
    return nil
}
```

**Impact**: Under concurrent access or rapid verification intervals, the store could regress to a lower block number, violating the monotonicity invariant that the proxy guarantees to clients. The streamer also advances independently of store success, causing desynchronization.

**Recommendation**: Wrap the entire read-check-update sequence in a single lock, or add a compare-and-swap operation to the store that rejects non-monotonic updates.

---

## HIGH Findings

### H-01: No HTTP Server Timeouts (Slowloris DoS)

**File**: `main.go:76-79`  
**Component**: HTTP Server

The `http.Server` is created without `ReadTimeout`, `WriteTimeout`, `ReadHeaderTimeout`, or `IdleTimeout`. This enables slowloris attacks where an attacker opens many connections, sends headers slowly, and exhausts server resources.

```go
// main.go:76-79
server := &http.Server{
    Addr:    cfg.ListenAddr,
    Handler: mux,
    // No ReadTimeout, WriteTimeout, IdleTimeout, ReadHeaderTimeout
}
```

**Impact**: Denial of service via slow-read/slow-write attacks. A single attacker can exhaust all available connections.

**Recommendation**:
```go
server := &http.Server{
    Addr:              cfg.ListenAddr,
    Handler:           mux,
    ReadTimeout:       15 * time.Second,
    ReadHeaderTimeout: 5 * time.Second,
    WriteTimeout:      30 * time.Second,
    IdleTimeout:       60 * time.Second,
    MaxHeaderBytes:    1 << 20, // 1MB
}
```

---

### H-02: No Rate Limiting

**Files**: `main.go`, `proxy/proxy.go`  
**Component**: HTTP Server / Proxy

No rate limiting, request throttling, or connection limits exist anywhere in the codebase. Combined with C-01 (no auth), any client can flood the proxy with unlimited requests.

**Impact**: Denial of service against the proxy and the backend full node. Resource exhaustion on both the proxy and the upstream RPC endpoint.

**Recommendation**: Add per-IP rate limiting middleware (e.g., `golang.org/x/time/rate` or a middleware like `tollbooth`).

---

### H-03: No Panic Recovery Middleware

**Files**: `main.go`, `proxy/proxy.go`  
**Component**: HTTP Handler

No `recover()` middleware wraps the HTTP handler. A panic in the proxy handler, interceptor, or any downstream code crashes the entire process.

```go
// No recovery middleware anywhere
mux.HandleFunc("/", fullNodeProxy.Serve) // Panic here = process crash
```

**Impact**: A crafted request that triggers a panic (e.g., via malformed JSON hitting an unexpected code path) kills the proxy, causing complete downtime.

**Recommendation**: Add panic recovery middleware that catches panics, logs them, and returns a 500 error.

---

### H-04: Unbounded Batch Request Size

**File**: `proxy/interceptor.go:68-104`  
**Component**: Interceptor

Batch JSON-RPC requests are parsed without any limit on the number of entries. An attacker can send a batch with millions of entries, causing CPU and memory exhaustion.

```go
// interceptor.go:68-72
func (i *Interceptor) interceptBatch(rawRequest []byte) ([]byte, error) {
    var batch []json.RawMessage
    if err := json.Unmarshal(rawRequest, &batch); err != nil {
        return nil, fmt.Errorf("failed to parse batch JSON-RPC request: %w", err)
    }
    // No limit on len(batch)
    for idx, raw := range batch { ... }
}
```

**Impact**: CPU/memory exhaustion. Amplification attack where the proxy processes each batch element, multiplying the cost of a single request.

**Recommendation**: Enforce a maximum batch size (e.g., 100 requests).

---

### H-05: Verifier `running` Flag Race Condition

**File**: `verifier/op/op_verifier.go:135-146, 398-410`  
**Component**: OPEspressoBatchVerifier

The `running` bool is read and written without synchronization in `Start()` and `Stop()`. While currently called from the main goroutine, this is a data race if the API changes.

```go
// op_verifier.go:135-146
func (v *OPEspressoBatchVerifier) Start(ctx context.Context) {
    if v.running {           // READ without lock
        return
    }
    v.running = true         // WRITE without lock
    // ...
    go v.run(ctx)
}

func (v *OPEspressoBatchVerifier) Stop() {
    if !v.running {          // READ without lock
        return
    }
    // ...
    v.running = false        // WRITE without lock
}
```

**Impact**: Double-start or missed-stop under concurrent access. The verifier could start multiple goroutines, each advancing the streamer independently.

**Recommendation**: Use `sync.Once` for Start, or protect `running` with a mutex/atomic.

---

### H-06: TOCTOU in Proxy State Read

**File**: `proxy/interceptor.go:74-85, 107-113`  
**Component**: Interceptor

State is read once at the start of request processing, then used for all operations. The verifier can update state between the read and the use.

```go
// interceptor.go:74 (batch) and 107 (single)
state := i.store.GetState() // Single read
// ... state.L2BlockNumber used for all batch entries
for idx, raw := range batch {
    result, singleChanged, err := i.replaceEspressoTag(raw, state.L2BlockNumber)
}
```

**Impact**: Proxy can serve slightly stale block numbers. In practice, this is a consistency issue within a single batch -- all entries get the same block number, which is actually consistent behavior. The real risk is if the state becomes **invalid** (zeroed) between read and use.

**Recommendation**: This is low-risk given the current single-verifier design. Document the intentional snapshot semantics. If consistency is critical, re-read state per request.

---

## MEDIUM Findings

### M-01: No TLS/HTTPS Support

**File**: `main.go:83`  
**Component**: HTTP Server

The server uses `ListenAndServe` (plaintext HTTP only). No TLS configuration exists anywhere in the codebase.

**Impact**: All RPC traffic is unencrypted. Man-in-the-middle attacks can observe or modify requests/responses.

**Recommendation**: Add `ListenAndServeTLS` option with certificate configuration, or document that TLS termination is expected at the load balancer/reverse proxy layer.

---

### M-02: Store File Permissions Too Permissive

**File**: `store/espresso_store.go:110`  
**Component**: EspressoStore

The state file is written with `0644` (world-readable) permissions.

```go
if err := os.WriteFile(tmp, data, 0644); err != nil {
```

**Impact**: Other users on the system can read the proxy's state file, which contains block numbers and timing information.

**Recommendation**: Use `0600` (owner read/write only).

---

### M-03: No Config Validation

**File**: `config.go:46-85`  
**Component**: Configuration

No validation that critical config values are set or valid. `FullNodeExecutionRPC`, `L1RPC`, `OPConfig.LightClientAddress` etc. can be empty strings. URL format is not validated.

```go
func parseConfig() *Config {
    cfg := defaultConfig()
    // ... flag parsing ...
    pflag.Parse()
    return cfg // No validation!
}
```

**Impact**: Empty or malformed config causes runtime panics or connections to wrong endpoints. Empty `QueryServiceURL` creates a client that talks to localhost.

**Recommendation**: Add a `validate()` method that checks all required fields are non-empty and URLs are parseable.

---

### M-04: Error String Matching (Fragile Error Handling)

**File**: `verifier/op/op_verifier.go:180-183`  
**Component**: OPEspressoBatchVerifier

Error classification uses string comparison on error messages, which is fragile and breaks silently when dependencies change error text.

```go
if err.Error() == "not found" {
    v.logger.Debug("batch not found on OP node yet, will try again on next interval")
    return
} else if strings.Contains(err.Error(), "retryable") {
    v.logger.Debug("espresso has not finalized the batch yet", "error", err)
    return
}
```

**Impact**: If upstream libraries change their error messages, the verifier's retry/skip logic changes silently, potentially treating retryable errors as fatal or vice versa.

**Recommendation**: Use sentinel errors (`errors.Is`) or typed errors instead of string matching.

---

### M-05: Forked Dependencies May Miss Security Patches

**File**: `go.mod:140-142`  
**Component**: Supply Chain

Both `go-ethereum` and `optimism` are replaced with Espresso/Celo forks:

```go
replace github.com/ethereum-optimism/optimism => github.com/EspressoSystems/optimism-espresso-integration v0.0.0-20260320193702-1e85078aed7b
replace github.com/ethereum/go-ethereum => github.com/celo-org/op-geth v1.101411.1-0.20260316145005-3a40c398c038
```

**Impact**: Security patches in upstream `go-ethereum` and `optimism` may not be reflected in the forks. These are critical cryptographic and consensus libraries.

**Recommendation**: Establish a process to regularly merge upstream security patches into forks. Subscribe to security advisories for both upstream projects.

---

### M-08: Recursive JSON Parsing Without Depth Limit

**File**: `proxy/interceptor.go:152-233`  
**Component**: Interceptor

`replaceTagInParams()` recursively walks JSON structures (objects and arrays) without any depth limit. Deeply nested JSON can cause stack overflow.

```go
func (i *Interceptor) replaceTagInParams(params json.RawMessage, ...) (...) {
    // Case 2: JSON object -- recurse into each value
    var obj map[string]json.RawMessage
    if err := json.Unmarshal(params, &obj); err == nil {
        for key, value := range obj {
            result, c, err := i.replaceTagInParams(value, ...) // Recursive call, no depth limit
        }
    }
    // Case 3: JSON array -- recurse into each element
    var arr []json.RawMessage
    if err := json.Unmarshal(params, &arr); err == nil {
        for j, value := range arr {
            result, c, err := i.replaceTagInParams(value, ...) // Recursive call, no depth limit
        }
    }
}
```

**Impact**: Stack overflow / crash via crafted deeply-nested JSON payload.

**Recommendation**: Add a `depth` parameter and reject requests exceeding a reasonable depth (e.g., 32 levels).

---

### M-09: Silent Error Swallowing in State Sync

**File**: `verifier/op/op_verifier.go:192-194`  
**Component**: OPEspressoBatchVerifier

When `syncEspressoStateWithEthereumFinality()` fails, the error is logged but execution continues. The proxy keeps serving stale finality data.

```go
if espressoBatch == nil {
    if err := v.syncEspressoStateWithEthereumFinality(ethFinalizedBlockNumber); err != nil {
        v.logger.Error("failed to update espresso state to ethereum finalized block", ...)
        // Error NOT returned -- execution continues
    }
    v.logger.Debug("no new batches to verify")
    return
}
```

**Impact**: If the Ethereum finality sync fails persistently (e.g., network partition), clients receive outdated finality information without any indication of staleness.

**Recommendation**: Track consecutive sync failures and expose a health check that degrades when the proxy falls behind.

---

### M-10: Default Verification Interval Too Aggressive

**File**: `config.go:41`  
**Component**: Configuration

The default `VerificationInterval` is `1 * time.Millisecond` -- 1000 ticks per second.

```go
func defaultConfig() *Config {
    return &Config{
        // ...
        OPConfig: OPConfig{
            VerificationInterval: 1 * time.Millisecond,
        },
    }
}
```

**Impact**: If deployed with defaults, the verifier hammers the full node and Espresso query service with 1000 requests per second, potentially causing self-DoS.

**Recommendation**: Use a more reasonable default like `250ms` or `1s`.

---

### M-12: Missing `.dockerignore` Exposes Full Repository to Docker Build Context

**Files**: `Dockerfile:5-9`, repository root (no `.dockerignore`)  
**Component**: Container Build / Supply Chain

The build uses `COPY . .` with no `.dockerignore` file in the repository:

```dockerfile
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -trimpath -o espresso-rollup-node-proxy .
```

**Impact**: The entire repository is sent to the Docker build context, including `.git/`, CI metadata, docs, E2E fixtures, and already-committed secrets/test credentials. Even though the final runtime image is multi-stage, the builder context still expands the attack surface for CI logs, cache layers, and any future build-step leakage.

**Recommendation**: Add a `.dockerignore` that excludes `.git/`, `.github/`, `espresso_e2e/`, local env files, docs, and other non-build inputs.

---

### M-13: Initial Store State Is Persisted in a Format the Loader Rejects

**File**: `store/espresso_store.go:45-53, 83-97`  
**Component**: EspressoStore

When the store file does not exist, `NewEspressoStore()` initializes and persists a state with `FallbackHotshotHeight` and `UpdatedAt`, but leaves `L2BlockNumber` at its zero value:

```go
// store/espresso_store.go:45-53
store.state = EspressoState{
    FallbackHotshotHeight: hotshotHeight,
    UpdatedAt:             time.Now(),
}
if err := store.writeToDisk(); err != nil {
    return nil, fmt.Errorf("failed to write initial state to disk: %w", err)
}

// store/espresso_store.go:83-97
if state.FallbackHotshotHeight == 0 || state.L2BlockNumber == 0 || state.UpdatedAt.IsZero() {
    return fmt.Errorf("invalid state file: missing required fields")
}
```

**Impact**: If the proxy restarts before the verifier advances the store for the first time, the persisted bootstrap state can become unloadable on restart. This breaks the documented "resume from where it left off" behavior and can force a reset or startup failure precisely during early lifecycle or failure-recovery scenarios.

**Recommendation**: Either persist a valid bootstrap `L2BlockNumber`, or relax `loadFromDisk()` validation so the initial zero-state written by `NewEspressoStore()` is accepted.

---

## LOW Findings

### L-02: `CountUniqueEntries` Uses `any` as Map Key

**File**: `streamer/nitro/utils.go:186`

```go
func CountUniqueEntries[T any](arr *[]T) uint64 {
    entriesMap := make(map[any]bool)
    // ...
}
```

**Impact**: If called with a non-comparable type, this panics at runtime.

---

### L-03: No Request/Audit Logging

**Files**: `proxy/proxy.go`, `main.go`  
**Component**: Proxy

The proxy does not log incoming RPC methods, client IPs, or request patterns. No audit trail exists for forensic analysis.

**Impact**: Inability to detect or investigate attacks, abuse, or anomalous usage patterns.

---

### L-05: Docker Image Tags Not Digest-Pinned

**File**: `Dockerfile:1,11`

```dockerfile
FROM golang:1.25-alpine AS builder   # Tag, not digest
FROM alpine:3.21                      # Tag, not digest
```

**Impact**: Supply chain risk from tag mutation. Low probability but high impact.

**Recommendation**: Pin to SHA256 digests.

---

## Remediation Priority

### Immediate (Deploy Blockers)

| ID | Finding | Effort |
|----|---------|--------|
| C-01 | Add authentication middleware | Medium |
| C-02 | Implement RPC method allowlist | Low |
| C-03 | Add `io.LimitReader` on request body | Low |
| H-01 | Set HTTP server timeouts | Low |
| H-02 | Add rate limiting middleware | Medium |
| H-03 | Add panic recovery middleware | Low |

### Short-term (Next Sprint)

| ID | Finding | Effort |
|----|---------|--------|
| C-04 | Make verifier state update atomic | Medium |
| H-04 | Enforce batch request size limit | Low |
| H-05 | Protect `running` flag with mutex/atomic | Low |
| M-03 | Add config validation | Low |
| M-12 | Add `.dockerignore` for build context | Low |
| M-13 | Fix bootstrap store state validation | Low |
| M-10 | Change default verification interval | Low |

### Medium-term

| ID | Finding | Effort |
|----|---------|--------|
| H-06 | Document TOCTOU snapshot semantics | Low |
| M-01 | Add TLS support or document TLS termination | Medium |
| M-02 | Fix store file permissions to 0600 | Low |
| M-04 | Replace string error matching with sentinel errors | Medium |
| M-05 | Establish fork patch-merge process | Ongoing |
| M-08 | Add recursion depth limit | Low |
| M-09 | Track sync failures in health check | Medium |

### Low Priority

| ID | Finding | Effort |
|----|---------|--------|
| L-02 | Fix `CountUniqueEntries` type constraint | Low |
| L-03 | Add request audit logging | Medium |
| L-05 | Pin Docker base images to digests | Low |

# Approach Review — espresso-rollup-node-proxy

**Date:** 2026-06-12
**Branch:** main
**Commit:** e3b7705f872057453c38f01a9b5e33061cfac396
**Reviewer:** Claude AI (claude-sonnet-4-6)

---

## Purpose and Scope

This service acts as a transparent JSON-RPC proxy that sits between clients and a rollup full node. Its single, well-defined job is to intercept calls containing an Espresso-specific block tag (`"espresso"`, or optionally `"finalized"`) and replace that tag with a verified, monotonically-increasing L2 block number that reflects Espresso consensus — before forwarding the request to the actual node.

**Is this the right problem to solve this way?**

Yes, broadly. The proxy-as-middleware pattern is the correct architectural choice for a lightweight, drop-in finality accelerator. It avoids modifying rollup node software, keeps the scope narrow, and can be deployed or removed without disrupting the rollup stack. The key invariant — the espresso-finalized block number is monotonically increasing and persists across restarts — is a well-chosen design constraint that protects clients from unsafe rollbacks.

The design choice to target two rollup platforms (OP Stack and Arbitrum Nitro) within one binary is reasonable given the shared proxy core and divergent verifier backends. The use of a `mode` switch rather than separate binaries keeps deployment simple at the cost of some internal branching complexity.

One meaningful open question: the README states that no open-source license has been selected and "all rights are reserved for the time being." For a piece of infrastructure intended to be deployed by rollup operators, this is a non-trivial adoption barrier. It should be resolved before the project reaches production scale.

---

## Testing Strategy Assessment (AP-T-*)

### AP-T-01: E2E-heavy testing pyramid is inverted but defensible given the domain

The test suite is predominantly end-to-end: 7 Docker-Compose-based e2e tests vs. 18 non-e2e unit/integration tests. For most software this ratio signals a testing smell, but this project has an unusual characteristic: the correctness property being tested is a cross-system invariant (Espresso consensus matching L2 block state). That invariant genuinely cannot be verified without running the actual sequencer, L1, and Espresso nodes. The e2e suite tests adversarial scenarios (L1 reorgs, L2 reorgs, feed tampering, sequencer state wipes, p2p attacks) that would be nearly impossible to mock meaningfully. The investment here is appropriate.

**The real gap:** The critical-path proxy logic — tag interception, store advancement — has sparse unit test coverage. `proxy/interceptor_test.go` has 8 test cases covering the happy path and basic structural variants, but no tests for concurrent reads against a store that is being updated by a verifier goroutine (the actual production scenario), no property-based fuzzing of the JSON walk, and no test for the `ErrMaxJSONDepthExceeded` path.

### AP-T-02: E2e tests use `time.Sleep` and wall-clock polling — flake risk exists

The pattern `pollUntil(t, timeout, msg, cond)` with a 1-second sleep body is used universally. For a blockchain e2e test this is hard to avoid, but there are several cases where timeouts are tight relative to the expected blockchain behavior. `waitForNitroServicesReady` waits up to 5 minutes for some services — if CI runners are slow, these can legitimately fail. The CI workflow gives each e2e job 30 minutes. There is one unconditional `time.Sleep(5 * time.Second)` in `op_rollup_e2e_l2_reorg_test.go` line 64 that is used as a "let blocks build" delay — this is fragile and should be replaced with a deterministic condition poll.

### AP-T-03: No property-based or fuzz testing of the JSON-RPC path

The `replaceTagInParams` recursive JSON walker is security-relevant code — it processes untrusted client input before forwarding to an upstream node. The depth limit (`maxJSONDepth = 32`) protects against stack overflow, but there is no fuzz corpus to verify the depth check fires correctly, that deeply-nested arrays/objects do not cause unexpected panics, or that the tag substitution is correct across the full space of JSON values Go's `encoding/json` can produce. `go test -fuzz` is available and trivially applicable here.

### AP-T-04: Store concurrency test coverage is absent

`UpdateIfGreater` has a TOCTOU-style check: it calls `GetState()` (which acquires a read lock) then drops the lock before calling `writeToDisk` and re-acquiring a write lock. In the single-writer production topology this is benign, but the pattern is not enforced by any test. Adding a concurrent-write stress test (multiple goroutines calling `UpdateIfGreater` simultaneously) run with `-race` would confirm the behavior and protect against future changes.

### AP-T-05: Dual websocket library approach validated by test suite

`gorilla_test.go` runs a protocol compliance suite against both the gorilla and coder adapters using a shared `BasicSuite` abstraction. This is good practice — it ensures both adapters remain interchangeable under the unified `Conn` interface.

### AP-T-06: Config validation has no dedicated fuzz/property tests

`config_test.go` exists (not listed in the original file set but present on disk). URL and address validation paths (`validateURL`, `validateAddress`) accept arbitrary strings. A fuzz test on `cfg.validate()` would catch any parser divergence between the config-file JSON unmarshaling and the flag-based path.

---

## Operational Posture Assessment (AP-O-*)

### AP-O-01: No metrics endpoint — biggest operational gap

The codebase has `prometheus/client_golang` as a transitive dependency (pulled in via go-ethereum/optimism), but there are zero Prometheus metrics exposed by the proxy itself. Operators cannot observe:
- Current espresso-finalized block number (the most important operational signal)
- Block advancement rate / lag behind the sequencer tip
- Number of intercepted vs. pass-through requests
- Verifier error rate
- WebSocket connection count or message throughput
- Store write latency

The `--track-batch-latency` flag logs per-batch latency at `Info` level, which is better than nothing but unusable for alerting. Without metrics, operators have no way to set up alerts for "espresso finalization has stalled for N minutes," which is the most critical failure mode.

**Recommendation:** Add a `/metrics` Prometheus endpoint (using `promhttp.Handler()`) with at minimum: `espresso_finalized_l2_block_number` (gauge), `espresso_verifier_errors_total` (counter), `espresso_intercepted_requests_total` (counter).

### AP-O-02: Health check exists but is readiness-unaware

`/health` returns HTTP 200 with body "OK" unconditionally from process start. This means a load balancer or Kubernetes readiness probe would declare the service healthy immediately, before the verifier has confirmed any blocks. A client hitting the proxy during this cold-start window gets the "espresso state is empty" path silently — requests with the espresso tag are forwarded to the full node without substitution (because `ErrUnknownEspressoFinalizedBlockNumber` is swallowed and the raw request is passed through). This is documented behavior for the `"espresso"` tag, but operators pointed at `"finalized"` mode get no indication of which tag semantics are active.

**Recommendation:** Add a `/ready` endpoint that returns 503 until the store has at least one verified block. Keep `/health` as a liveness probe.

### AP-O-03: Logging is structured and well-deployed

The project uses `go-ethereum`'s `log` package (backed by `log/slog`) with configurable JSON or terminal output — a good choice for production deployability. Log levels are used consistently throughout. The `CaptureLogger` in `log/logutil` for test log assertions is a well-engineered approach that avoids brittle string matching in tests.

One gap: the global `log.SetDefault(logger)` in `configureLogger` and in e2e test helpers means the global logger is shared across test goroutines when multiple sub-tests run concurrently. This can produce test log interference.

### AP-O-04: Graceful shutdown is implemented but has a noted deficiency

The 30-second HTTP shutdown timeout is reasonable. The shutdown sequence (signal -> cancel context -> HTTP servers shutdown -> verifier stop) is correct in ordering. The comment in `startHTTPServers` honestly flags the issue:

> "TODO: Re-evaluate this logger.Crit usage. Invoking this function will also call os.Exit, forcing the program to exit without cleaning up."

If the HTTP server fails to bind (port conflict, permission error), the program exits immediately via `logger.Crit` without allowing any cleanup. This should be changed to return an error that the caller can handle.

WebSocket connections do not appear to receive any close frames on shutdown — the WS server is shut down via `http.Server.Shutdown()`, which stops accepting new connections and waits for active HTTP handlers to complete, but active WebSocket connections have already been "handed off" to a separate goroutine model. The behavior of active WS connections during shutdown deserves explicit testing.

### AP-O-05: No distributed tracing

There is no OpenTelemetry or equivalent instrumentation. For a proxy in a blockchain finality stack, request traces that span from client request to upstream response would be valuable for diagnosing latency spikes. This is a gap but not a blocker for early production.

---

## Dependency Strategy (AP-DEP-*)

### AP-DEP-01: Two WebSocket libraries is the right pragmatic call, but creates ongoing maintenance overhead

The project ships two WebSocket library adapters:
- `github.com/coder/websocket` — used for the proxy server side (upgrading client connections and dialing the upstream node)
- `github.com/gorilla/websocket` — used for the Nitro feed client

The Nitro sequencer feed uses a custom `Arbitrum-Requested-Sequence-Number` header and a specific handshake protocol that gorilla handles more naturally. The proxy's own connection handling uses coder/websocket, which is context-aware by design (avoiding the goroutine-per-connection workaround visible in `gorilla.go`'s `Read` implementation: spawning a cancellation goroutine for every read).

This is a reasonable decision but the gorilla adapter's context-cancellation approach (spawning a goroutine to set a deadline when the context is cancelled) is a goroutine-per-active-read overhead under high WebSocket connection counts. The coder library handles this natively. Whether gorilla can eventually be replaced for the feed client should be tracked.

### AP-DEP-02: Replace directives introduce a supply-chain opacity risk

```
replace github.com/ethereum-optimism/optimism => github.com/EspressoSystems/optimism-espresso-integration v0.0.0-20260320193702-1e85078aed7b
replace github.com/ethereum/go-ethereum => github.com/celo-org/op-geth v1.101411.1-0.20260316145005-3a40c398c038
```

Both replaces point to Espresso-controlled or partner-controlled forks. This is necessary to integrate Espresso consensus support. However:
1. The upstream fork of `go-ethereum` is a pseudo-version date from March 2026 — tracking a floating commit on a fork makes `go mod tidy` less predictable and makes auditing the supply chain difficult.
2. The replace directive for `optimism` points to an integration fork that is not the upstream. Changes in either fork must be manually tracked and rebased. This creates a compounding integration debt if upstream optimism or go-ethereum release breaking changes.
3. Operators who want to audit what they're running will have difficulty since the replaced modules are not the canonical upstream packages.

**Recommendation:** Pin to tagged releases of the forks where possible. Maintain a CHANGELOG noting divergence from upstream to make it clear what modifications are present.

### AP-DEP-03: Dependency footprint is very large for the problem solved

The `go.mod` has approximately 120 indirect dependencies, including libp2p, pion/DTLS, cockroachdb/pebble, and bitcoin libraries — all pulled in transitively through go-ethereum and optimism. The binary therefore carries a significant amount of code that the proxy never executes. This is a supply-chain and binary-size concern rather than a functional one, but it increases the attack surface for transitive vulnerabilities.

The `go-ethereum` logger dependency in the proxy core means the project imports an entire Ethereum node framework just to get structured logging. Using `log/slog` directly would eliminate this coupling in the core packages. (The verifier legitimately needs go-ethereum for contract calls and block parsing.)

### AP-DEP-04: `google/renameio` is a good choice for atomic file writes

Using `renameio.TempFile` + `CloseAtomicallyReplace()` for the state file is correct practice for crash-safe state persistence on Linux filesystems. The comment correctly notes the filesystem rename atomicity assumption.

---

## Build & Deployment Assessment (AP-BD-*)

### AP-BD-01: Dockerfile is well-structured

Multi-stage build with CGO-enabled builder and a minimal alpine runtime image. The non-root `proxyuser` (UID 1000) and `HEALTHCHECK` directive are good operational practice. `HEALTHCHECK` uses `wget` against `localhost:8080/health` — this hardcodes the default port and will fail silently if `--listen-addr` is changed. Consider parameterizing via an environment variable or removing the HEALTHCHECK in favor of external probe configuration.

`-ldflags="-s -w" -trimpath` reduces binary size and strips debug info. Acceptable for production but means post-mortem debugging requires separate symbol retention or rebuild.

### AP-BD-02: Nix flake is minimal but correct

The `flake.nix` provides a dev shell with `go`, `gopls`, `golangci-lint`, `just`, and an `abigen` wrapper. Using `nixpkgs-unstable` as the nixpkgs input means the dev environment is reproducible but tracks a moving target — on any given day, the pinned `go` version in nixpkgs-unstable may not match `go 1.25.1` in `go.mod`. The `flake.lock` (present on disk) pins the exact revision, so reproducibility is maintained for contributors who use it. This is acceptable for a dev shell.

The Nix flake does not produce a reproducible build artifact (no `packages` output) — it is dev-tooling only. For a production deployment, reproducible builds via Nix would be a meaningful security property. Not building it yet is fine, but worth noting as a future investment.

### AP-BD-03: CI covers the full e2e matrix with appropriate parallelism

The CI configuration splits each e2e scenario into its own job (e2e-op, e2e-op-reorg, e2e-op-l2-reorg, e2e-nitro, e2e-nitro-l1-reorg, e2e-nitro-l2-reorg, e2e-nitro-feed-mismatch). This is the right call: e2e tests sharing a runner would conflict on fixed ports (8545, 8546, etc.). The 30-minute per-job timeout is reasonable given the 5-minute startup waits for Nitro services.

The unit/lint/build job correctly uses `-race` flag and golangci-lint. The Docker publish job publishes on every push to main and on all PRs — this means every PR produces a container image, which is operationally useful for testing but creates image registry clutter.

### AP-BD-04: No semver tagging strategy is visible

The module path is `github.com/EspressoSystems/espresso-rollup-node-proxy` (no `/vN` suffix), signaling v0 or v1. The Docker workflow tags images with branch name, PR number, commit SHA, and `latest`. There is no tag-based version workflow — production deployments would pin to a SHA, not a semver tag. For infrastructure software consumed by rollup operators, establishing a semver release cadence with changelogs would significantly reduce operational risk.

---

## JSON-RPC Implementation Strategy (AP-RPC-*)

### AP-RPC-01: Rolling a custom JSON-RPC v2 implementation is justified

The proxy's job is to intercept and rewrite JSON-RPC requests without parsing the full method-specific semantics. Standard JSON-RPC libraries (including go-ethereum's internal RPC) are designed for *serving* RPC methods — they marshal/unmarshal method-specific parameter types. Using such a library here would force the proxy to know about every Ethereum method signature, which defeats the purpose of a transparent proxy.

The custom `jsonrpcv2` package takes the right approach: define a `Request` with `Params any` (to preserve the raw structure), an `ExtraFields map[string]any` (to pass through non-standard fields without losing them), and do the tag substitution with a recursive tree walk over the generic `any` values that `encoding/json` produces.

The `ExtraFields` approach is thoughtful — it ensures clients that send non-standard fields (some Ethereum clients do) get those fields back in the response without the proxy dropping them.

### AP-RPC-02: The recursive tag-walk strategy is correct but has a subtle flaw

`replaceTagInParams` is a recursive descent over the `any` JSON tree. The depth limit of 32 is a reasonable defense, but the implementation reconstructs new map/slice copies even when `changed == false` for objects (it builds `nextParams := map[string]any{}` before iterating). For objects, when no change occurs, it returns the original `cast` rather than the freshly allocated `nextParams`, which is correct — but the code allocates `nextParams` unconditionally before the loop. For deeply nested structures with many keys and no matches, this generates unnecessary allocations per level. Not a correctness issue, but a performance characteristic in high-throughput paths.

### AP-RPC-03: Batch request handling correctly reads the store once per batch

`InterceptBatchRequests` calls `getCurrentEspressoFinalizedBlockNumber()` once and uses that value for the entire batch. This is correct: a batch should be atomic in the block number it sees, and prevents a race where the store is updated mid-batch.

### AP-RPC-04: Error path returns raw request rather than failing on parse errors

When `getCurrentEspressoFinalizedBlockNumber()` returns `ErrUnknownEspressoFinalizedBlockNumber`, the interceptor logs a warning and returns the original unmodified request — the espresso tag remains as a literal string in the forwarded request, which the full node will reject with an error (since it does not understand the `"espresso"` tag). This is the documented behavior for the cold-start case and is correct for the `"espresso"` custom tag. However, if the operator has configured `espresso_tag = "finalized"`, the fallback passes through `"finalized"` to the full node unchanged, which the full node handles natively — this is the correct fallback behavior and is explicitly tested.

---

## Error Recovery Strategy (AP-ER-*)

### AP-ER-01: Verifier failure modes are well-reasoned

The verifier uses a tight polling loop with a configurable interval (default 10ms for verification, 1s for finality polling). On a mismatch, it logs and retries at the next interval — the store is not updated. This is the right conservative approach: only advance the finalized block when Espresso and the full node agree. The feed-mismatch e2e test validates this: the store freezes when the feed is tampered and recovers when the real feed is restored.

### AP-ER-02: No circuit breaker or backoff on upstream RPC failures

When the full node is unreachable, the verifier will log an error and retry after `VerificationInterval` (10ms by default). At 10ms intervals, a sustained outage of the full node will generate ~100 log lines per second. The finality poller has a 5-second per-poll timeout (`finalityPollTimeout`) and 1-second poll interval, which is more reasonable. The verifier loop itself has no such timeout. Under high-frequency failure scenarios, the error log volume could overwhelm log aggregation.

**Recommendation:** Add exponential backoff with a cap (e.g., max 5-second retry interval) on verifier errors. Increment a `verifier_errors_total` counter.

### AP-ER-03: Store write failures halt advancement, not the process

In `UpdateIfGreater`, if `writeToDisk` fails, the in-memory state is not updated and `false, err` is returned. The verifier must decide what to do with this error. If the disk is full or the path is unwriteable, the proxy continues serving requests with a stale finalized block number — the espresso tag stops advancing, but the proxy remains live and passes through untagged requests. This is the safest possible behavior (never serve a block number you haven't durably committed to) but operators have no visibility into this state without checking logs.

### AP-ER-04: No reconnection logic for WebSocket feed disconnect is visible

The Nitro feed client connects to the sequencer feed WebSocket. If this connection drops, the feed client presumably errors out. How the nitro verifier responds to a feed client error — whether it reconnects automatically or requires a process restart — is not visible in the files reviewed. For a production service, automatic reconnection with backoff is essential.

---

## Positive Approach Highlights

**Monotonic store guarantee.** The `UpdateIfGreater` semantics are a simple, correct implementation of the monotonicity invariant. Using an atomic file rename for persistence (`renameio`) is the right primitive for crash safety.

**Clean adapter pattern for transports.** The `Interceptor` interface in `adapters` is well-abstracted. The same interceptor logic runs for both HTTP and WebSocket transports, with transport-specific glue kept separate. Adding a third transport (gRPC, for example) would require only a new adapter, not changes to the core.

**Explicit compile-time interface assertions.** Multiple packages use `var _ Interface = (*Impl)(nil)` to catch interface drift at compile time. This is good Go practice.

**Test infrastructure quality.** The e2e test helpers (`pollUntil`, `monitorStoredBlockProgress`, `captureBlockHashes`, `CaptureLogger`, `newMockFeedProxy`) are well-engineered reusable primitives. The mock feed proxy in `nitro_e2e_feed_mismatch_test.go` is particularly clever — it intercepts real Nitro feed messages and injects corruption at a specific sequence number, testing an adversarial scenario with real components.

**HTTP server configuration is hardened.** Read/write timeouts, header size limits, and body size limits are all set. The body size limit is configurable and defaults to 5MB (matching go-ethereum's own default).

**CODEOWNERS is populated.** Eleven reviewers are listed for the entire repo. This is appropriately broad for a small team and ensures no PR can land without cross-team visibility.

**Config dual-mode (file + flags, flags override).** The config system correctly handles both JSON file and CLI flags with flags taking precedence. Eager validation at startup (`cfg.validate()`) surfaces misconfiguration before the process becomes live.

**Graceful shutdown sequence is correct in ordering.** Signal -> HTTP servers drain -> verifier stop ensures in-flight requests complete before the verifier goroutine is torn down, preventing a window where an in-flight request could see a store update that was not yet persisted.

---

## Issue Tracking Table

| ID | Category | Priority | Description | Recommendation |
|----|----------|----------|-------------|----------------|
| AP-O-01 | Operational | Critical | No Prometheus metrics endpoint; operators cannot observe finalization lag, error rates, or throughput | Add `/metrics` with at minimum `espresso_finalized_l2_block_number` gauge and `espresso_verifier_errors_total` counter |
| AP-O-02 | Operational | High | `/health` returns 200 from process start; no readiness distinction | Add `/ready` that returns 503 until the store has one verified block |
| AP-ER-02 | Error Recovery | High | No backoff on verifier upstream RPC failures; 10ms interval generates ~100 errors/s during full-node outage | Add exponential backoff (cap at ~5s) in the verifier retry loop |
| AP-T-03 | Testing | High | No fuzz testing of `replaceTagInParams` JSON walker; security-relevant code processes untrusted input | Add `FuzzReplaceTagInParams` using `go test -fuzz` |
| AP-DEP-02 | Dependencies | High | `replace` directives point to floating commits on Espresso-controlled forks; supply chain opacity | Pin forks to tagged releases; maintain a divergence CHANGELOG |
| AP-ER-04 | Error Recovery | High | Feed client reconnection behavior on WebSocket disconnect is not visible or tested | Confirm/implement auto-reconnect with backoff in the Nitro feed client |
| AP-T-02 | Testing | Medium | Unconditional `time.Sleep(5 * time.Second)` in `op_rollup_e2e_l2_reorg_test.go:64` is fragile | Replace with a deterministic `pollUntil` condition |
| AP-T-04 | Testing | Medium | No concurrent-write stress test for `EspressoStore.UpdateIfGreater` | Add a `-race` concurrent goroutine test |
| AP-O-04 | Operational | Medium | HTTP server bind failure calls `logger.Crit` -> `os.Exit` without cleanup; WebSocket shutdown behavior under active connections is untested | Return errors from `startHTTPServers`; add WS shutdown test |
| AP-BD-01 | Build | Medium | `HEALTHCHECK` in Dockerfile hardcodes port 8080; breaks if `--listen-addr` is changed | Parameterize via env var or document the constraint clearly |
| AP-BD-04 | Build | Medium | No semver release workflow; production deployments must pin to commit SHAs | Establish a tagged release cadence with a CHANGELOG |
| AP-T-01 | Testing | Medium | Core proxy logic (tag interception, concurrent store reads during verifier updates) lacks concurrency unit tests | Add tests that run a verifier goroutine while issuing interceptor calls |
| AP-RPC-02 | Performance | Low | `replaceTagInParams` allocates `nextParams` map unconditionally before checking for changes | Allocate lazily on first change detection |
| AP-DEP-01 | Dependencies | Low | gorilla adapter spawns a goroutine per active WebSocket read for context cancellation; coder adapter handles this natively | Track whether gorilla can be replaced for the feed client |
| AP-DEP-03 | Dependencies | Low | Very large transitive dependency footprint (libp2p, pebble, bitcoin libs) due to go-ethereum/optimism | Consider vendoring or using `go mod vendor` to make the full dep tree auditable |
| AP-O-03 | Operational | Low | `log.SetDefault(logger)` is called from e2e test helpers; global logger shared across concurrent sub-tests can produce log interference | Pass logger explicitly in test helpers rather than using global |

# Espresso Rollup Node Proxy

The Espresso Rollup Node Proxy is a Go service that sits between clients and a rollup's full node to enforce [Espresso](https://docs.espressosys.com/) finality. Instead of relying solely on Ethereum for finality, clients can use an Espresso-specific block tag (default: `"espresso"`) in their JSON-RPC calls; the proxy resolves that tag to the latest L2 block number confirmed by Espresso's HotShot consensus.

The proxy can also be configured to intercept **any** block tag — the standard `"finalized"`, `"safe"`, `"latest"`, `"pending"` and `"earliest"` tags, or an arbitrary custom string — and back it with Espresso finality, so existing clients that already use a standard tag get faster finality with no code changes at all. Multiple tags can be intercepted at once (e.g. `"espresso_tag": ["safe", "finalized"]`); every configured tag resolves to the same Espresso-finalized block number. See [Intercepted tags](#intercepted-tags) for exactly which strings are rewritten.

In both modes, the proxy resolves the block number as `max(espresso finalized, eth finalized)`. This means clients always get Espresso's faster finality when it is ahead, but can safely fall back to Ethereum finality.

The `"espresso"` block number is **monotonically increasing** — it will never move backwards, even across restarts, Ethereum reorgs, or sequencer reorgs. The finalized block number is persisted to disk, so on restart the proxy resumes from where it left off rather than resetting to zero. This gives clients a stable, safe cursor into the chain that only ever moves forward.

## How it works

**Proxy** — Intercepts every JSON-RPC request (including batch requests) before forwarding it to the full node. Any occurrence of a configured tag in the request params is replaced with the current Espresso-finalized L2 block number, then the request is forwarded transparently.

**Verifier** — A background loop continuously compares batches produced by the Espresso streamer against the corresponding blocks on the full node. On a match, the loop advances the store and persists the new L2 block number to disk; on a mismatch, it retries at the next interval.

**Store** — Persists the Espresso-finalized L2 block number and HotShot fallback position to a JSON file so state survives restarts.

The proxy starts in non-Espresso mode and automatically switches to Espresso mode once the verifier confirms the first batch.

### Intercepted tags

**Any tag can be intercepted.** The proxy keeps no allowlist of block tags: whatever strings are listed in `espresso_tag` are the tags it rewrites, and nothing else is touched. In particular:

| Configured tag | Effect |
| -------------- | ------ |
| `espresso` (default) | Opt-in. Only clients that explicitly send `"espresso"` get Espresso finality; every standard tag is passed through to the full node untouched. |
| `finalized` | Existing clients that already send `"finalized"` get Espresso finality (`max(espresso finalized, eth finalized)`) with no code changes. |
| `safe` | Same as `finalized`; the two are typically configured together as `["safe", "finalized"]`. |
| `latest` / `pending` | Accepted like any other tag. Note that these normally refer to the chain head, so intercepting them makes clients observe only the Espresso-finalized state, which lags the head. |
| `earliest` | Accepted like any other tag. Intercepting it changes its meaning from the genesis block to the Espresso-finalized block. |
| any other string | A custom tag such as `"my-tag"` behaves exactly like `"espresso"`. |

The following matching rules apply identically to every configured tag:

- **Exact, case-sensitive string equality.** With `latest` configured, `"Latest"`, `"latest "` and `"latest-ish"` are *not* rewritten. Tags are never matched by prefix or substring.
- **Any string value in `params`, at any depth.** The interceptor walks positional arrays, object values (e.g. `{"blockTag": "finalized"}`), nested structures, and every request in a batch. Object *keys*, the `method`, and the `id` are never rewritten.
- **Method-agnostic.** The interceptor does not know which parameter of which method is the block parameter; a string equal to a configured tag is rewritten wherever it appears. Avoid configuring a tag that could legitimately show up as some other parameter value (for example a hex quantity such as `"0x1"`).
- **One block number for every tag.** All configured tags resolve to the same value, encoded as a hex quantity (e.g. `"0x64"`).
- **Pass-through until Espresso state exists.** Until the verifier has confirmed the first batch, every request is forwarded unchanged. An intercepted `"latest"` is then still served by the full node as the chain head, while an intercepted `"espresso"` reaches the full node as-is (which will typically reject it as an unknown block tag).

## Architecture

![Architecture](docs/proxy_diagram.png)

## Running the Proxy

### Build

```sh
go build -o espresso-rollup-node-proxy .
```

### Configuration

The proxy is configured via CLI flags or a JSON config file (or both — flags override file values). Pass `--config <path>` to load a file first, then apply any additional flags on top.

**Example config file (OP Stack):**

```json
{
  "full_node_execution_rpc": "<op-geth-rpc>",
  "ws_full_node_execution_rpc": "<op-geth-ws-rpc>",
  "eth_rpc": "<eth-rpc>",
  "mode": "op",
  "namespace": <l2-chain-id>,
  "listen_addr": ":8080",
  "ws_listen_addr": ":8081",
  "espresso_tag": "espresso",
  "store_file_path": "/data/espresso_store.json",
  "query_service_url": "<espresso-query-service-url>",
  "verification_interval": "250ms",
  "initial_hotshot_height": 0,
  "op": {
    "light_client_address": "<light-client-contract-address>",
    "batcher_address": "<batcher-address>",
    "batch_authenticator_address": "<batch-authenticator-contract-address>"
  }
}
```

**Example config file (Nitro):**

```json
{
  "full_node_execution_rpc": "<nitro-rpc>",
  "ws_full_node_execution_rpc": "<nitro-ws-rpc>",
  "eth_rpc": "<eth-rpc>",
  "mode": "nitro",
  "namespace": <l2-chain-id>,
  "listen_addr": ":8080",
  "ws_listen_addr": ":8081",
  "espresso_tag": "espresso",
  "store_file_path": "/data/espresso_store.json",
  "query_service_url": "<espresso-query-service-url>",
  "verification_interval": "250ms",
  "nitro": {
    "feed_url": "<nitro-sequencer-feed-ws-url>",
    "valid_signing_key_addresses": [
      { "address": "<signing-key-address>", "from": 0, "to": 18446744073709551615 }
    ],
    "eth_log_scan_block_range": 10000
  }
}
```

**Run with a config file:**

```sh
./espresso-rollup-node-proxy --config config.json
```

**Run with flags only (OP Stack):**

```sh
./espresso-rollup-node-proxy \
  --full-node-execution-rpc <op-geth-rpc> \
  --ws.full-node-execution-rpc <op-geth-ws-rpc> \
  --mode op \
  --namespace <l2-chain-id> \
  --listen-addr :8080 \
  --ws.listen-addr :8081 \
  --espresso-tag espresso \
  --store-file-path /data/espresso_store.json \
  --query-service-url <espresso-query-service-url> \
  --eth-rpc <eth-rpc> \
  --op.light-client-address <light-client-contract-address> \
  --op.batcher-address <batcher-address> \
  --op.batch-authenticator-address <batch-authenticator-contract-address>
```

**Run with flags only (Nitro):**

```sh
./espresso-rollup-node-proxy \
  --full-node-execution-rpc <nitro-rpc> \
  --ws.full-node-execution-rpc <nitro-ws-rpc> \
  --mode nitro \
  --namespace <l2-chain-id> \
  --listen-addr :8080 \
  --ws.listen-addr :8081 \
  --espresso-tag espresso \
  --store-file-path /data/espresso_store.json \
  --query-service-url <espresso-query-service-url> \
  --eth-rpc <eth-rpc> \
  --nitro.feed-url <nitro-sequencer-feed-ws-url> \
  --nitro.bridge-address <bridge-contract-address> \
  --nitro.valid-signing-key-addresses <signing-key-address>
```

### Docker

**Run with a config file:**

```sh
docker run --rm \
  -v /path/to/config.json:/config.json \
  -v /path/to/data:/data \
  -p 8080:8080 \
  ghcr.io/espressosystems/espresso-rollup-node-proxy:latest \
  --config /config.json
```

**Run with flags only:**

```sh
docker run --rm \
  -v /path/to/data:/data \
  -p 8080:8080 \
  -p 8081:8081 \
  ghcr.io/espressosystems/espresso-rollup-node-proxy:latest \
  --full-node-execution-rpc <op-geth-rpc> \
  --ws.full-node-execution-rpc <op-geth-ws-rpc> \
  --ws.listen-addr :8081 \
  --mode op \
  --namespace <l2-chain-id> \
  --store-file-path /data/espresso_store.json \
  --query-service-url <espresso-query-service-url> \
  --eth-rpc <eth-rpc> \
  --op.light-client-address <light-client-contract-address> \
  --op.batcher-address <batcher-address> \
  --op.batch-authenticator-address <batch-authenticator-contract-address>
```

Mount a volume for `--store-file-path` so the persisted state survives container restarts.

**Example `docker-compose.yml`:**

```yaml
services:
  espresso-rollup-node-proxy:
    image: ghcr.io/espressosystems/espresso-rollup-node-proxy:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
      - "8081:8081"
    volumes:
      - proxy-data:/data
    command:
      - --full-node-execution-rpc=http://op-geth:8545
      - --ws.full-node-execution-rpc=http://op-geth:8546
      - --mode=op
      - --namespace=<l2-chain-id>
      - --listen-addr=:8080
      - --ws.listen-addr=:8081
      - --store-file-path=/data/espresso_store.json
      - --query-service-url=<espresso-query-service-url>
      - --eth-rpc=<eth-rpc>
      - --op.light-client-address=<light-client-contract-address>
      - --op.batcher-address=<batcher-address>
      - --op.batch-authenticator-address=<batch-authenticator-contract-address>
    depends_on:
      op-geth:
        condition: service_healthy

  op-geth:
    image: <op-geth-image>
    # ... your op-geth configuration

volumes:
  proxy-data:
```

Clients should point at the proxy (`http://localhost:8080`) rather than directly at `op-geth`.

### Configuration Reference

The **Required** column shows ✅ for settings required in all modes, and ✅ OP / ✅ Nitro for settings required only in that mode. Everything else is optional and falls back to the listed default.

#### Required

| Flag | JSON key | Required | Description |
| ---- | -------- | -------- | ----------- |
| `--mode` | `mode` | ✅ | Verifier mode: `op` or `nitro` |
| `--namespace` | `namespace` | ✅ | Espresso namespace; must equal the chain's own chain ID |
| `--full-node-execution-rpc` | `full_node_execution_rpc` | ✅ | Rollup execution layer RPC URL (the full node the proxy sits in front of) |
| `--eth-rpc` | `eth_rpc` | ✅ | Ethereum / parent-chain RPC URL |
| `--query-service-url` | `query_service_url` | ✅ | Espresso query service URL |
| `--op.light-client-address` | `op.light_client_address` | ✅ OP | Espresso light client contract address on Ethereum |
| `--op.batcher-address` | `op.batcher_address` | ✅ OP | OP batcher address |
| `--op.batch-authenticator-address` | `op.batch_authenticator_address` | ✅ OP | Batch Authenticator contract address on Ethereum |
| `--nitro.feed-url` | `nitro.feed_url` | ✅ Nitro | Nitro full node feed WebSocket URL |
| `--nitro.bridge-address` | `nitro.bridge_address` | ✅ Nitro | Nitro Bridge contract address on Ethereum |
| `--nitro.valid-signing-key-addresses` | `nitro.valid_signing_key_addresses` | ✅ Nitro | Valid signing key addresses (at least one) |

#### Optional

| Flag | JSON key | Default | Description |
| ---- | -------- | ------- | ----------- |
| `--listen-addr` | `listen_addr` | `:8080` | Address the proxy listens on |
| `--ws.listen-addr` | `ws_listen_addr` | — | WebSocket listen address; set together with `ws.full-node-execution-rpc` to enable the WebSocket proxy |
| `--ws.full-node-execution-rpc` | `ws_full_node_execution_rpc` | — | Execution layer WebSocket RPC URL; required to enable the WebSocket proxy |
| `--espresso-tag` | `espresso_tag` | `espresso` | JSON-RPC block tag(s) to intercept. Any tag can be intercepted — the standard `finalized`, `safe`, `latest`, `pending`, `earliest`, or any custom string; e.g. set to `safe,finalized` to back the standard finality tags with Espresso. The flag accepts a comma-separated list or may be repeated; the JSON field accepts a string or an array of strings. All configured tags resolve to the same Espresso-finalized block number. See [Intercepted tags](#intercepted-tags) |
| `--store-file-path` | `store_file_path` | `espresso_store.json` | Path to the state persistence file |
| `--verification-interval` | `verification_interval` | `10ms` | How often the verifier polls for new confirmed batches |
| `--finality-poll-interval` | `finality_poll_interval` | `1s` | How often the finality poller queries the full node for the latest finalized block |
| `--initial-hotshot-height` | `initial_hotshot_height` | `0` | HotShot height to start streaming from; **used only on first run, when no state file exists**. Must be non-zero on a fresh start — the proxy exits otherwise |
| `--max-batch-size` | `max_batch_size` | `1000` | Maximum requests in a JSON-RPC batch (0 = unlimited) |
| `--max-request-body-size` | `max_request_body_size` | `5242880` | Maximum request body size in bytes (0 = unlimited) |
| `--log-level` | `log_level` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `--log-format` | `log_format` | `json` | Log output format (`text` or `json`) |
| `--track-batch-latency` | `track_batch_latency` | `false` | Log per-batch and average latency from HotShot finalization to verification |
| `--nitro.wait-for-eth-finality` | `nitro.wait_for_eth_finality` | `false` | Wait for Ethereum block finalization before fetching delayed messages |
| `--nitro.eth-log-scan-block-range` | `nitro.eth_log_scan_block_range` | `10000` | Max Ethereum blocks scanned per `eth_getLogs` query when fetching delayed messages; lower it for RPC providers that cap log ranges (`0` uses the default) |

## WebSockets

In order to utilize the WebSocket proxy, two optional configurations **MUST**
be present. We need to know the WebSocket Listening Address, and the
Execution Websocket RPC URL:

| Flag                           | JSON key                     |
| ------------------------------ | ---------------------------- |
| `--ws.listen-addr`             | `ws_listen_addr`             |
| `--ws.full-node-execution-rpc` | `ws_full_node_execution_rpc` |

If one or both of these are not specified, the WebSocket port will not be
enabled. You will see a log message indicating that the server is listening
on the specified WebSocket address, or you will see a log indicating
that the WebSocket proxy is not in effect, or that an error has occurred
parsing the provided Execution WebSocket RPC URL.

## E2E Tests

The `espresso_e2e/` directory contains a battery of integration tests that spin up a full rollup environment via Docker Compose and verify the proxy behaves correctly under adversarial and failure conditions — including Ethereum reorgs, sequencer reorgs, malicious sequencer feeds, and proxy restarts. In all cases two invariants must hold: the `"espresso"` tag must never move backwards, and it must only advance when the full node's state matches that of Espresso.

Run the e2e tests with `just e2e`.

## Development

Enter the Nix dev shell and use `just`.

```sh
nix develop
```

Run `just test` to execute the unit tests, or `just` with no arguments to list all available recipes.

## License

Copyright (c) 2022 Espresso Systems. The Espresso Rollup Node Proxy was developed by Espresso Systems. While we plan to adopt an open source license, we have not yet selected one. As such, all rights are reserved for the time being. Please reach out to us if you have thoughts on licensing.

## Disclaimer

DISCLAIMER: This software is provided "as is" and its security has not been externally audited. Use at your own risk.

DISCLAIMER: The Go packages provided in this repository are intended primarily for use by the binary targets in this repository. We make no guarantees of public API stability. If you are building on these packages, reach out by opening an issue to discuss the APIs you need.

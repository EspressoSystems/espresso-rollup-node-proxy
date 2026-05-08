# Espresso Rollup Node Proxy

The Espresso Rollup Node Proxy is a Go service that sits between clients and a rollup's full node to enforce [Espresso](https://docs.espressosys.com/) finality. Instead of relying solely on Ethereum for finality, clients can use an Espresso-specific block tag (default: `"espresso"`) in their JSON-RPC calls; the proxy resolves that tag to the latest L2 block number confirmed by Espresso's HotShot consensus.

The proxy can also be configured to intercept the standard `"finalized"` tag and back it with Espresso finality, so existing clients that already use `"finalized"` get faster finality with no code changes at all.

In both modes, the proxy resolves the block number as `max(espresso finalized, eth finalized)`. This means clients always get Espresso's faster finality when it is ahead, but can safely fall back to Ethereum finality.

The `"espresso"` block number is **monotonically increasing** — it will never move backwards, even across restarts, L1 reorgs, or sequencer reorgs. The finalized block number is persisted to disk, so on restart the proxy resumes from where it left off rather than resetting to zero. This gives clients a stable, safe cursor into the chain that only ever moves forward.

## How it works

**Proxy** — Intercepts every JSON-RPC request (including batch requests) before forwarding it to the full node. Any occurrence of the Espresso tag in the request params is replaced with the current Espresso-finalized L2 block number, then the request is forwarded transparently.

**Verifier** — A background loop continuously compares batches produced by the Espresso streamer against the corresponding blocks on the full node. On a match, the loop advances the store and persists the new L2 block number to disk; on a mismatch, it retries at the next interval.

**Store** — Persists the Espresso-finalized L2 block number and HotShot fallback position to a JSON file so state survives restarts.

The proxy starts in non-Espresso mode and automatically switches to Espresso mode once the verifier confirms the first batch.

## Architecture

![Architecture](docs/proxy_diagram.png)

## Running the Proxy

### Build

```sh
go build -o espresso-rollup-node-proxy .
```

### Configuration

The proxy is configured via CLI flags or a JSON config file (or both — flags override file values). Pass `--config <path>` to load a file first, then apply any additional flags on top.

**Example config file:**

```json
{
  "full_node_execution_rpc": "<op-geth-rpc>",
  "l1_rpc": "<l1-rpc>",
  "listen_addr": ":8080",
  "espresso_tag": "espresso",
  "store_file_path": "/data/espresso_store.json",
  "initial_hotshot_height": 0,
  "op": {
    "enable": true,
    "full_node_consensus_rpc": "<op-node-rpc>",
    "query_service_url": "<espresso-query-service-url>",
    "light_client_address": "<light-client-contract-address>",
    "batcher_address": "<batcher-address>",
    "batch_authenticator_address": "<batch-authenticator-contract-address>",
    "verification_interval": "250ms"
  }
}
```

**Run with a config file:**

```sh
./espresso-rollup-node-proxy --config config.json
```

**Run with flags only:**

```sh
./espresso-rollup-node-proxy \
  --full-node-execution-rpc <op-geth-rpc> \
  --l1-rpc <l1-rpc> \
  --listen-addr :8080 \
  --espresso-tag espresso \
  --store-file-path /data/espresso_store.json \
  --op.enable \
  --op.full-node-consensus-rpc <op-node-rpc> \
  --op.query-service-url <espresso-query-service-url> \
  --op.light-client-address <light-client-contract-address> \
  --op.batcher-address <batcher-address> \
  --op.batch-authenticator-address <batch-authenticator-contract-address>
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
  ghcr.io/espressosystems/espresso-rollup-node-proxy:latest \
  --full-node-execution-rpc <op-geth-rpc> \
  --l1-rpc <l1-rpc> \
  --store-file-path /data/espresso_store.json \
  --op.enable \
  --op.full-node-consensus-rpc <op-node-rpc> \
  --op.query-service-url <espresso-query-service-url> \
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
    volumes:
      - proxy-data:/data
    command:
      - --full-node-execution-rpc=http://op-geth:8545
      - --l1-rpc=<l1-rpc>
      - --listen-addr=:8080
      - --store-file-path=/data/espresso_store.json
      - --op.enable
      - --op.full-node-consensus-rpc=http://op-node:9545
      - --op.query-service-url=<espresso-query-service-url>
      - --op.light-client-address=<light-client-contract-address>
      - --op.batcher-address=<batcher-address>
      - --op.batch-authenticator-address=<batch-authenticator-contract-address>
    depends_on:
      op-geth:
        condition: service_healthy
      op-node:
        condition: service_healthy

  op-geth:
    image: <op-geth-image>
    # ... your op-geth configuration

  op-node:
    image: <op-node-image>
    # ... your op-node configuration

volumes:
  proxy-data:
```

Clients should point at the proxy (`http://localhost:8080`) rather than directly at `op-geth`.

### Configuration Reference

| Flag | JSON key | Default | Description |
|------|----------|---------|-------------|
| `--full-node-execution-rpc` | `full_node_execution_rpc` | — | OP execution layer RPC URL (required) |
| `--l1-rpc` | `l1_rpc` | — | L1 RPC URL (required) |
| `--listen-addr` | `listen_addr` | `:8080` | Address the proxy listens on |
| `--espresso-tag` | `espresso_tag` | `espresso` | JSON-RPC block tag to intercept; set to `finalized` to back the standard finality tag with Espresso |
| `--store-file-path` | `store_file_path` | `espresso_store.json` | Path to the state persistence file |
| `--initial-hotshot-height` | `initial_hotshot_height` | `0` | HotShot block height to start streaming from on first run |
| `--max-batch-size` | `max_batch_size` | `1000` | Maximum requests in a JSON-RPC batch (0 = unlimited) |
| `--max-request-body-size` | `max_request_body_size` | `5242880` | Maximum request body size in bytes (0 = unlimited) |
| `--op.enable` | `op.enable` | `false` | Enable OP stack mode |
| `--op.full-node-consensus-rpc` | `op.full_node_consensus_rpc` | — | OP consensus layer (op-node) RPC URL |
| `--op.query-service-url` | `op.query_service_url` | — | Espresso query service URL |
| `--op.light-client-address` | `op.light_client_address` | — | Espresso light client contract address on L1 |
| `--op.batcher-address` | `op.batcher_address` | — | OP batcher address |
| `--op.batch-authenticator-address` | `op.batch_authenticator_address` | — | Batch Authenticator contract address on L1 |
| `--op.verification-interval` | `op.verification_interval` | `10ms` | How often the verifier polls for new confirmed batches |
| `--log-level` | `log_level` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `--log-format` | `log_format` | `json` | Log output format (`text` or `json`) |
| `--track-batch-latency` | `track_batch_latency` | `false` | Log per-batch and average latency from HotShot finalization to verification |

## E2E Tests

The `espresso_e2e/` directory contains a battery of integration tests that spin up a full rollup environment via Docker Compose and verify the proxy behaves correctly under adversarial and failure conditions — including L1 reorgs, sequencer reorgs, malicious sequencer feeds, and proxy restarts. In all cases two invariants must hold: the `"espresso"` tag must never move backwards, and it must only advance when the full node's state matches that of Espresso.

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

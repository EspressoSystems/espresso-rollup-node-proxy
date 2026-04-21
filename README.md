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

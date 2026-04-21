# Espresso Rollup Node Proxy

<<<<<<< li/update-readme
The Espresso Rollup Node Proxy sits between JSON-RPC clients and a rollups Full Node, exposing Espresso's finality as a block tag.

Clients can use `"espresso"` anywhere a block tag is accepted — `eth_getBalance`, `eth_call`, `eth_getBlockByNumber`, etc. — and the proxy transparently replaces it with the latest Espresso-finalized L2 block number before forwarding the request to the underlying full node.

The proxy can also be configured to intercept the standard `"finalized"` tag and back it with Espresso finality, so existing clients that already use `"finalized"` get faster finality with no code changes at all.

### How it works

A background verifier continuously compares batches finalized by Espresso against the state of the full node. When a batch matches, the internal store advances to that L2 block number. When a client request references the `"espresso"` tag, the proxy substitutes it with the current Espresso-finalized block number and forwards the request to the full node.

The `"espresso"` block number is **monotonically increasing** — it will never move backwards, even across restarts, L1 reorgs, or sequencer reorgs. This gives clients a stable, safe cursor into the chain that only moves forward as Espresso confirms new blocks.

In both the `"espresso"` tag and the optional `"finalized"` tag interception mode, the proxy resolves the block number as `max(espresso finalized, eth finalized)`. This means clients always get Espresso's faster finality when it is ahead, but can safely fall back to Ethereum finality.

### Architecture

![Architecture](docs/proxy_diagram.png)

### Repository layout

| Path | Description |
|------|-------------|
| `proxy/` | HTTP reverse proxy and JSON-RPC interceptor. Rewrites the `"espresso"` block tag in incoming requests before forwarding them to the full node. |
| `store/` | Persistent store that tracks the current Espresso-finalized L2 block number. Written atomically to disk so the proxy survives restarts. |
| `verifier/` | Background verifier implementations per rollup type. Continuously fetches blocks from Espresso and compares them against the full node, and advancing the tag when they match. |
| `espresso_e2e/` | End-to-end integration tests that spin up a full rollup environment via Docker Compose and exercise the proxy against a live network, and run various edge case tests, such as proxy restarts, l1 reorgs, l2 reorgs, malicious sequencer feed, etc.. |
=======
The Espresso Rollup Node Proxy is a Go service that sits between clients and an
[OP stack](https://docs.optimism.io/) L2 full node to enforce
[Espresso](https://docs.espressosys.com/) finality. Instead of relying solely on
Ethereum for finality, clients can use an Espresso-specific block tag (default:
`"espresso"`) in their JSON-RPC calls; the proxy resolves that tag to the latest
L2 block number confirmed by Espresso's HotShot consensus.

## How it works

**Proxy** — Intercepts every JSON-RPC request (including batch requests) before
forwarding it to the full node. Any occurrence of the Espresso tag in the
request params is replaced with the current Espresso-finalized L2 block number,
then the request is forwarded transparently.

**Verifier** — A background loop continuously compares the next batch produced
by the Espresso streamer against the corresponding block on the OP full node.
On a match, the loop advances the streamer and persists the new L2 block number to disk; on a mismatch, it retries at the next interval.

**Store** — Persists the Espresso-finalized L2 block number and HotShot fallback
position to a JSON file so state survives restarts.

The proxy starts in non-Espresso mode and automatically switches to Espresso
mode once the verifier confirms the first batch.

## Development

Enter the Nix dev shell and use `just`.

```sh
nix develop

```

Run just test to execute the tests, or run just with no arguments to list all available recipes.

## License
Copyright
(c) 2022 Espresso Systems. The Espresso Rollup Node Proxy was developed by Espresso Systems. While we plan to adopt an open source license, we have not yet selected one. As such, all rights are reserved for the time being. Please reach out to us if you have thoughts on licensing.

## Disclaimer
DISCLAIMER: This software is provided "as is" and its security has not been externally audited. Use at your own risk.

DISCLAIMER: The Go packages provided in this repository are intended primarily for use by the binary targets in this repository. We make no guarantees of public API stability. If you are building on these packages, reach out by opening an issue to discuss the APIs you need.
>>>>>>> main

# Espresso Rollup Node Proxy

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

# Espresso Rollup Node Proxy

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
On a match the loop
advances the streamer and persists the new L2 block number to disk; on a
mismatch it retries at the next interval.

**Store** — Persists the Espresso-finalized L2 block number and HotShot fallback
position to a JSON file so state survives restarts.

The proxy starts in non-Espresso mode and automatically switches to Espresso
mode once the verifier confirms the first batch.

## Development

Enter the Nix dev shell and use `just`.

```sh
nix develop

```

Run `just` with no arguments to list all available recipes.

## License
Copyright
(c) 2022 Espresso Systems espresso-network was developed by Espresso Systems. While we plan to adopt an open source license, we have not yet selected one. As such, all rights are reserved for the time being. Please reach out to us if you have thoughts on licensing.

## Disclaimer
DISCLAIMER: This software is provided "as is" and its security has not been externally audited. Use at your own risk.

DISCLAIMER: The Go packages provided in this repository are intended primarily for use by the binary targets in this repository. We make no guarantees of public API stability. If you are building on these packages, reach out by opening an issue to discuss the APIs you need.
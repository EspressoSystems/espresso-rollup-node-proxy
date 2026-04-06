# P2P Attacker

A man-in-the-middle service that sits between the sequencer and full node on the OP Stack gossip network. It intercepts gossiped blocks and optionally replaces them with malicious ones, then forwards them downstream to the full node. The intent is to cause the full node's state to differ from the sequencer as batches are posted with the non-malicious sequencer feed. So, when blocks are streamed in from `Espresso` in the `Verifier` component and compared against the full node's now malicious fork, the `Verifier` component should reject and not advance the `EspressoTag` until the full node reorgs and the state reflects the same as what was submitted to `Espresso` by the batcher.

## Architecture

### Initialization
The attacker runs two libp2p hosts:

- **Client host** — connects to the sequencer and subscribes to the block gossip topic. Receives every block the sequencer publishes.
- **Server host** — acts as a peer for the full node to connect to, republishing blocks (original or malicious) via gossip.

### Normal gossip
```
Sequencer ──gossip──> [libp2p client] P2P Attacker [libp2p server] ──gossip──> Full Node
                                             |
                                        Engine RPC
                                        (op-geth-fullnode)
```
Note: We only communicate with full node geth engine when we want to create a malicious block and start a fork

### Catchup requests
```
Full Node ──request data──> [libp2p server] P2P Attacker [libp2p client] ──request data──> Sequencer
Full Node <──response data── [libp2p server] P2P Attacker [libp2p client] <──response data── Sequencer
```
Note: Since we do not cache any data inside the service we are just a proxy handling catchup requests between the full node and sequencer.
Currently nothing malicious is done in this path.

## How It Works

### Normal operation (no fork configured)

Every gossiped block received from the sequencer is gossiped as-is to the full node.

### Fork attack (`forkBlock` is set)

When a block arrives at the configured `forkBlock` height:

1. The P2P attacker calls `engine_forkchoiceUpdatedV3` on the full node's engine RPC, pointing the head at the block's parent and injecting modified `PayloadAttributes` (first transaction duplicated) — this causes geth to produce a new valid but malicious block via the EVM.
2. Then calls `engine_getPayloadV4` to retrieve the newly built block from geth.
3. The new block is signed with the sequencer's private key (so downstream nodes accept it as legitimate).
4. Lastly, the malicious block is gossiped to the full node in place of the real one.

Subsequent blocks from the sequencer will have a `parentHash` pointing to the original chain, not the malicious one. When the attacker sees this, it rebuilds the block on top of the correct malicious parent, keeping the fork alive.

### Block signing

The attacker holds the sequencer's ECDSA private key. After building a malicious block, it signs the payload with the sequencer private key, so the full node accepts it.

### Request/response forwarding

When a full node misses a gossiped block it can request it by block number via the libp2p request/response protocol (`/opstack/req/payload_by_number/<chainId>/0`). The P2P Attacker's libp2p server host handles these requests by forwarding them as-is to the sequencer and sending the response as-is back. This keeps the full node from stalling if for some reason gossip blocks are missed.

## Message topics

Gossip messages are on the topic `/optimism/<chainId>/3/blocks` which the P2P attacker subscribes to.

## HTTP API

The HTTP server listens on `:8080` inside the docker network (other containers reach it at `http://p2p-attacker:8080`).
The host machine port is configurable via `P2P_ATTACKER_PORT` in `.env`, currently set to `8560`, so from your local machine you can hit `http://localhost:8560`.

| Method | Path | Body | Description |
|--------|------|------|-------------|
| GET | `/peer-id` | — | Returns the attacker's libp2p peer ID (used so the full node can retrieve it, then connect to this service instead of the sequencer) |
| POST | `/create-fork-at-block` | `{"blockNumber": N}` | Triggers a fork starting at block N |

## Configuration

Hardcoded constants in `main.go`, sourced from the docker compose setup:

| Constant | Description |
|----------|-------------|
| `p2pListenerAddress` | Address the libp2p server listens on |
| `sequencerRpcAddress` | op-node RPC used to discover the sequencer's peer ID |
| `sequencerP2PAddress` | Sequencer's libp2p address |
| `opFullNodeEngineRpc` | Engine RPC used to build malicious blocks |
| `jwtPath` | JWT secret for authenticating engine RPC requests |
| `l2ChainId` | Chain id of the rollup, used to subscribe to gossip topics |
| `sequencerPrivateKey` | Sequencer's signing key — used to sign malicious blocks so they pass signature verification |

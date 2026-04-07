#!/bin/bash
set -euo pipefail

BLOCKS=${1:-2}
OP_GETH_RPC="http://localhost:8546"
# OP_NODE_RPC="http://localhost:9545"

# SENDER_KEYS=(
#   "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
#   "0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
#   "0x7c852118294e51e653712a81e05800f419141751be58f605c371e15141b007a6"
# )
KEY=0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d

load_gen() {
  echo "Load gen started for key $KEY..."
  while true; do
    cast send --rpc-url "$OP_GETH_RPC" --private-key "$KEY" "0x0000000000000000000000000000000000000001" --value "1wei" >/dev/null 2>&1
  done
}

load_gen &
LOAD_GEN_PID=$!
trap 'kill "$LOAD_GEN_PID" 2>/dev/null' EXIT

echo "Rewinding sequencer by $BLOCKS blocks..."

echo "Stopping op-node sequencer..."
docker compose stop op-node-sequencer >/dev/null 2>&1

echo "Getting current block number..."
BLOCK=$(cast block --rpc-url $OP_GETH_RPC | awk '/number/ {print $2}')

SAFE=$(cast block safe --rpc-url $OP_GETH_RPC | awk '/number/ {print $2}')
FINALIZED=$(cast block finalized --rpc-url $OP_GETH_RPC | awk '/number/ {print $2}')
echo "Unsafe:    $BLOCK"
echo "Safe:      $SAFE"
echo "Finalized: $FINALIZED"
TARGET=$(( BLOCK - BLOCKS ))

ORIGINAL_HASH=$(cast block $BLOCK --rpc-url $OP_GETH_RPC | awk '/hash/ {print $2}')
echo "Original hash at block $BLOCK: $ORIGINAL_HASH"

TARGET_HEX=$(printf '0x%x' $TARGET)
echo "Current block: $BLOCK, rewinding to: $TARGET ($TARGET_HEX)"

echo "Setting geth head..."

curl -sf -X POST "$OP_GETH_RPC" \
  -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"method\":\"debug_setHead\",\"params\":[\"$TARGET_HEX\"],\"id\":1}" | jq .

echo "Getting new head hash..."
NEW_BLOCK_HEAD=$(cast block --rpc-url $OP_GETH_RPC | awk '/hash|number/ {print $2}')
echo "New block head: $NEW_BLOCK_HEAD"

echo "Restarting op-node sequencer at new head..."
docker compose start op-node-sequencer >/dev/null 2>&1

echo "Done. Sequencer rewound $BLOCKS blocks to block $TARGET."

echo "Waiting for sequencer to reach block $BLOCK again..."
while true; do
  CURRENT=$(cast block --rpc-url $OP_GETH_RPC | awk '/number/ {print $2}')
  if (( CURRENT >= BLOCK )); then
    NEW_HASH_AT_BLOCK=$(cast block $BLOCK --rpc-url $OP_GETH_RPC | awk '/hash/ {print $2}')
    echo "New hash at block $BLOCK:      $NEW_HASH_AT_BLOCK"
    echo "Original hash at block $BLOCK: $ORIGINAL_HASH"
    if [[ "$NEW_HASH_AT_BLOCK" == "$ORIGINAL_HASH" ]]; then
      echo "Hashes match — no reorg"
    else
      echo "Hashes differ — reorg confirmed"
    fi
    break
  fi
  sleep 0.5
done

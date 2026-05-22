#!/usr/bin/env bash
set -euo pipefail

SEQ_URL="${SEQ_URL:-http://localhost:8547}"
PROXY_URL="${PROXY_URL:-http://localhost:8080}"
ESPRESSO_TAG="${ESPRESSO_TAG:-espresso}"

seq_block=$(cast block --rpc-url "$SEQ_URL" | awk '/^number/ {print $2}')
espresso_block_hex=$(cast rpc --rpc-url "$PROXY_URL" eth_getBlockByNumber "\"$ESPRESSO_TAG\"" false | jq -r '.number')
espresso_block=$(cast to-dec "$espresso_block_hex")

echo "sequencer: $seq_block"
echo "espresso:  $espresso_block"
echo "lag:       $((seq_block - espresso_block))"

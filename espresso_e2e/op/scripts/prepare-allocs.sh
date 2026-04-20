#!/usr/bin/env bash
set -euxo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
E2E_DIR="$(dirname "${SCRIPT_DIR}")"

source "${E2E_DIR}/.env"

DEPLOYMENT_DIR="${E2E_DIR}/deployment"
DEPLOYER_DIR="${DEPLOYMENT_DIR}/deployer"
L1_CONFIG_DIR="${DEPLOYMENT_DIR}/l1-config"

rm -rf "${DEPLOYMENT_DIR}"
mkdir -p "${DEPLOYER_DIR}" "${L1_CONFIG_DIR}"

SUFFIX="$$"
NETWORK_NAME="espresso-prepare-${SUFFIX}"
ANVIL_CONTAINER="anvil-prepare-${SUFFIX}"

cleanup() {
    echo "Cleaning up..."
    docker rm -f "${ANVIL_CONTAINER}" 2>/dev/null || true
    docker network rm "${NETWORK_NAME}" 2>/dev/null || true
}
trap cleanup EXIT

docker network create "${NETWORK_NAME}"

docker run -d \
    --name "${ANVIL_CONTAINER}" \
    --network "${NETWORK_NAME}" \
    --entrypoint anvil \
    "ghcr.io/foundry-rs/foundry:v1.5.1" \
    --host 0.0.0.0 --port 8545 \
    --chain-id "${L1_CHAIN_ID}" \
    --disable-gas-limit --disable-code-size-limit

echo "Waiting for Anvil..."
for i in $(seq 1 30); do
    if docker exec "${ANVIL_CONTAINER}" cast bn --rpc-url http://localhost:8545 >/dev/null 2>&1; then
        echo "Anvil ready"
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "ERROR: Anvil failed to start"
        exit 1
    fi
    sleep 1
done

docker exec "${ANVIL_CONTAINER}" cast rpc anvil_setBalance "${OPERATOR_ADDRESS}" 0x100000000000000000000000000000000000 --rpc-url http://localhost:8545
docker exec "${ANVIL_CONTAINER}" cast rpc anvil_setBalance "${PROPOSER_ADDRESS}" 0x100000000000000000000000000000000000 --rpc-url http://localhost:8545

docker run --rm \
    --network "${NETWORK_NAME}" \
    -v "${DEPLOYER_DIR}:/deployer" \
    -v "${SCRIPT_DIR}:/scripts:ro" \
    -e OPERATOR_ADDRESS="${OPERATOR_ADDRESS}" \
    -e OPERATOR_PRIVATE_KEY="${OPERATOR_PRIVATE_KEY}" \
    -e PROPOSER_ADDRESS="${PROPOSER_ADDRESS}" \
    -e ESPRESSO_BATCHER_ADDRESS="${ESPRESSO_BATCHER_ADDRESS}" \
    -e FALLBACK_BATCHER_ADDRESS="${FALLBACK_BATCHER_ADDRESS}" \
    -e BATCH_AUTHENTICATOR_OWNER_ADDRESS="${BATCH_AUTHENTICATOR_OWNER_ADDRESS}" \
    -e L1_CHAIN_ID="${L1_CHAIN_ID}" \
    -e L2_CHAIN_ID="${L2_CHAIN_ID}" \
    -e ANVIL_CONTAINER="${ANVIL_CONTAINER}" \
    -e LOG_LEVEL=debug \
    --entrypoint sh \
    "${OP_GETH_IMAGE}" \
    -c /scripts/deploy-contracts.sh

ANVIL_STATE="${DEPLOYMENT_DIR}/anvil_state.json"
docker exec "${ANVIL_CONTAINER}" cast rpc anvil_dumpState --rpc-url http://localhost:8545 > "${ANVIL_STATE}"

docker run --rm \
    -v "${DEPLOYMENT_DIR}:/deployment" \
    -v "${SCRIPT_DIR}:/scripts:ro" \
    -v "${E2E_DIR}/environment:/environment:ro" \
    --entrypoint sh \
    "${OP_GETH_IMAGE}" \
    -c '
        /scripts/reshape-allocs.jq /deployment/anvil_state.json \
            | jq "{ \"alloc\": map_values(.state) }" \
            > /tmp/deployer_allocs.json

        jq -s "reduce .[] as \$item ({}; . * \$item)" \
            <(jq "{ \"alloc\": map_values(.state) }" /environment/allocs.json) \
            /tmp/deployer_allocs.json \
            /environment/devnet-genesis-template.json \
            > /deployment/l1-config/genesis.json
    '

rm -f "${ANVIL_STATE}"

BATCH_AUTH=$(jq -r \
    '.opChainDeployments[0].batchAuthenticatorAddress // empty' \
    "${DEPLOYER_DIR}/state.json" 2>/dev/null || true)

if [ -n "${BATCH_AUTH}" ] && [ "${BATCH_AUTH}" != "null" ]; then
    echo ""
    echo "=========================================="
    echo "BatchAuthenticator address: ${BATCH_AUTH}"
    echo "=========================================="

    if grep -q "^BATCH_AUTH_ADDR=" "${E2E_DIR}/.env"; then
        CURRENT=$(grep "^BATCH_AUTH_ADDR=" "${E2E_DIR}/.env" | cut -d= -f2)
        if [ "${CURRENT}" != "${BATCH_AUTH}" ]; then
            sed -i.bak "s|^BATCH_AUTH_ADDR=.*|BATCH_AUTH_ADDR=${BATCH_AUTH}|" "${E2E_DIR}/.env"
            rm -f "${E2E_DIR}/.env.bak"
            echo "Updated BATCH_AUTH_ADDR in .env"
        fi
    else
        echo "BATCH_AUTH_ADDR=${BATCH_AUTH}" >> "${E2E_DIR}/.env"
        echo "Added BATCH_AUTH_ADDR to .env"
    fi
else
    echo "WARNING: BatchAuthenticator address not found in state.json"
fi

echo ""
echo "Deployment complete. Files generated in ${DEPLOYMENT_DIR}"

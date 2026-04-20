#!/bin/sh
# Runs inside OP_GETH_IMAGE container. Called by prepare-allocs.sh.
set -euxo pipefail

ANVIL_URL="http://${ANVIL_CONTAINER}:8545"
DEPLOYER_DIR="/deployer"

op-deployer bootstrap superchain \
    --l1-rpc-url="${ANVIL_URL}" \
    --private-key="${OPERATOR_PRIVATE_KEY}" \
    --artifacts-locator=embedded \
    --outfile="${DEPLOYER_DIR}/bootstrap_superchain.json" \
    --superchain-proxy-admin-owner="${OPERATOR_ADDRESS}" \
    --protocol-versions-owner="${OPERATOR_ADDRESS}" \
    --guardian="${OPERATOR_ADDRESS}"

SUPERCHAIN_JSON="${DEPLOYER_DIR}/bootstrap_superchain.json"

op-deployer bootstrap implementations \
    --l1-rpc-url="${ANVIL_URL}" \
    --private-key="${OPERATOR_PRIVATE_KEY}" \
    --artifacts-locator=embedded \
    --protocol-versions-proxy="$(jq -r .protocolVersionsProxyAddress < "${SUPERCHAIN_JSON}")" \
    --superchain-config-proxy="$(jq -r .superchainConfigProxyAddress < "${SUPERCHAIN_JSON}")" \
    --superchain-proxy-admin="$(jq -r .proxyAdminAddress < "${SUPERCHAIN_JSON}")" \
    --upgrade-controller="${OPERATOR_ADDRESS}" \
    --challenger="${OPERATOR_ADDRESS}" \
    --proof-maturity-delay-seconds=12 \
    --dispute-game-finality-delay-seconds=6 \
    --outfile="${DEPLOYER_DIR}/bootstrap_implementations.json"

op-deployer init \
    --l1-chain-id "${L1_CHAIN_ID}" \
    --l2-chain-ids "${L2_CHAIN_ID}" \
    --intent-type standard-overrides \
    --outdir "${DEPLOYER_DIR}"

INTENT="${DEPLOYER_DIR}/intent.toml"
IMPL_JSON="${DEPLOYER_DIR}/bootstrap_implementations.json"

dasel put -f "${INTENT}" -s .chains.[0].espressoEnabled -t bool -v true
dasel put -f "${INTENT}" -s .chains.[0].espressoBatcher -v "${ESPRESSO_BATCHER_ADDRESS}"

dasel put -f "${INTENT}" -s .l1ContractsLocator -v embedded
dasel put -f "${INTENT}" -s .l2ContractsLocator -v embedded
dasel put -f "${INTENT}" -s .opcmAddress -v "$(jq -r .opcmAddress < "${IMPL_JSON}")"
dasel put -f "${INTENT}" -s .fundDevAccounts -t bool -v true

dasel put -f "${INTENT}" -s .globalDeployOverrides.faultGameMaxClockDuration -t int -v 302400
dasel put -f "${INTENT}" -s .globalDeployOverrides.faultGameClockExtension -t int -v 10800
dasel put -f "${INTENT}" -s .globalDeployOverrides.preimageOracleChallengePeriod -t int -v 0
dasel put -f "${INTENT}" -s .globalDeployOverrides.dangerouslyAllowCustomDisputeParameters -t bool -v true
dasel put -f "${INTENT}" -s .globalDeployOverrides.proofMaturityDelaySeconds -t int -v 12
dasel put -f "${INTENT}" -s .globalDeployOverrides.disputeGameFinalityDelaySeconds -t int -v 6

dasel put -f "${INTENT}" -s .chains.[0].baseFeeVaultRecipient -v "${OPERATOR_ADDRESS}"
dasel put -f "${INTENT}" -s .chains.[0].l1FeeVaultRecipient -v "${OPERATOR_ADDRESS}"
dasel put -f "${INTENT}" -s .chains.[0].sequencerFeeVaultRecipient -v "${OPERATOR_ADDRESS}"
dasel put -f "${INTENT}" -s .chains.[0].operatorFeeVaultRecipient -v "${OPERATOR_ADDRESS}"
dasel put -f "${INTENT}" -s .chains.[0].chainFeesRecipient -v "${OPERATOR_ADDRESS}"
dasel put -f "${INTENT}" -s .chains.[0].roles.systemConfigOwner -v "${OPERATOR_ADDRESS}"
dasel put -f "${INTENT}" -s .chains.[0].roles.unsafeBlockSigner -v "${OPERATOR_ADDRESS}"
dasel put -f "${INTENT}" -s .chains.[0].roles.batcher -v "${FALLBACK_BATCHER_ADDRESS}"
dasel put -f "${INTENT}" -s .chains.[0].roles.proposer -v "${PROPOSER_ADDRESS}"
dasel put -f "${INTENT}" -s .chains.[0].roles.l1ProxyAdminOwner -v "${OPERATOR_ADDRESS}"
dasel put -f "${INTENT}" -s .chains.[0].roles.challenger -v "${OPERATOR_ADDRESS}"

dasel put -f "${INTENT}" -s .chains.[0].dangerousAltDAConfig.useAltDA -t bool -v true
dasel put -f "${INTENT}" -s .chains.[0].dangerousAltDAConfig.daCommitmentType -v "GenericCommitment"
dasel put -f "${INTENT}" -s .chains.[0].dangerousAltDAConfig.daChallengeWindow -t int -v 6
dasel put -f "${INTENT}" -s .chains.[0].dangerousAltDAConfig.daResolveWindow -t int -v 1

dasel put -f "${DEPLOYER_DIR}/state.json" -s create2Salt \
    -v "0xaecea4f57fadb2097ccd56594f2f22715ac52f92971c5913b70a7f1134b68feb"

BATCH_AUTHENTICATOR_OWNER_ADDRESS="${BATCH_AUTHENTICATOR_OWNER_ADDRESS}" \
op-deployer apply \
    --l1-rpc-url "${ANVIL_URL}" \
    --workdir "${DEPLOYER_DIR}" \
    --private-key="${OPERATOR_PRIVATE_KEY}"

echo "BatchAuthenticator: $(jq -r '.opChainDeployments[0].batchAuthenticatorAddress // "not found"' "${DEPLOYER_DIR}/state.json")"

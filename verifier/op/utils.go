package verifier

import (
	"encoding/binary"
	"fmt"

	"github.com/EspressoSystems/espresso-streamers/op/derivation"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// L1 origin offsets in the L1-info deposit calldata. This is found:
//   - Bedrock:  https://github.com/EspressoSystems/optimism-espresso-integration/blob/1e85078aed7b/op-node/rollup/derive/l1_block_info.go#L88-L101
//   - Ecotone+: https://github.com/EspressoSystems/optimism-espresso-integration/blob/1e85078aed7b/op-node/rollup/derive/l1_block_info.go#L175-L189
const (
	functionSelectorLength  = 4
	l1OriginNumberOffset    = 28
	l1OriginNumberOffsetEnd = l1OriginNumberOffset + 8
	l1OriginHashOffset      = 100
	l1OriginHashOffsetEnd   = l1OriginHashOffset + 32
)

// knownL1InfoSelectors are the L1-info "setL1BlockValues*" function selectors found:
// https://github.com/EspressoSystems/optimism-espresso-integration/blob/1e85078aed7b/op-node/rollup/derive/l1_block_info.go#L36-L39
var knownL1InfoSelectors = map[string]struct{}{
	string(derive.L1InfoFuncBedrockBytes4): {},
	string(derive.L1InfoFuncEcotoneBytes4): {},
	string(derive.L1InfoFuncIsthmusBytes4): {},
	string(derive.L1InfoFuncJovianBytes4):  {},
}

// l1OriginFromL2Block extracts the L1 origin recorded in an OP L2 block's
// L1-info deposit transaction, which is always the block's first transaction.
func l1OriginFromL2Block(block *types.Block) (eth.BlockID, error) {
	txs := block.Transactions()
	if len(txs) == 0 || !txs[0].IsDepositTx() {
		return eth.BlockID{}, fmt.Errorf("L2 block %d has no L1 info deposit transaction", block.NumberU64())
	}
	data := txs[0].Data()
	if len(data) < l1OriginHashOffsetEnd {
		return eth.BlockID{}, fmt.Errorf("L1 info deposit calldata too short: got %d bytes, need at least %d", len(data), l1OriginHashOffsetEnd)
	}
	if _, ok := knownL1InfoSelectors[string(data[:functionSelectorLength])]; !ok {
		return eth.BlockID{}, fmt.Errorf("unrecognized L1 info deposit selector %#x: calldata layout is not a known fork (Bedrock/Ecotone/Isthmus/Jovian)", data[:4])
	}
	return eth.BlockID{
		Number: binary.BigEndian.Uint64(data[l1OriginNumberOffset:l1OriginNumberOffsetEnd]),
		Hash:   common.BytesToHash(data[l1OriginHashOffset:l1OriginHashOffsetEnd]),
	}, nil
}

// filterUserDeposits returns a copy of block with L1-derived user deposit
// transactions removed
func filterUserDeposits(block *types.Block) *types.Block {
	if block == nil {
		return nil
	}
	txs := block.Transactions()
	if len(txs) == 0 {
		return block
	}
	// Keep tx[0] (the L1-info deposit) and every non-deposit transaction.
	filtered := make(types.Transactions, 0, len(txs))
	filtered = append(filtered, txs[0])
	for _, tx := range txs[1:] {
		if tx.IsDepositTx() {
			continue
		}
		filtered = append(filtered, tx)
	}
	if len(filtered) == len(txs) {
		return block
	}
	return block.WithBody(types.Body{
		Transactions: filtered,
		Uncles:       block.Uncles(),
		Withdrawals:  block.Withdrawals(),
	})
}

func espressoBatchToBlock(fullNodeBlock *types.Block, batch *derivation.EspressoBatch) (*types.Block, error) {
	// Re-insert the deposit transaction
	txs := []*types.Transaction{batch.L1InfoDeposit}
	for i, opaqueTx := range batch.Batch.Transactions {
		var tx types.Transaction
		err := tx.UnmarshalBinary(opaqueTx)
		if err != nil {
			return nil, fmt.Errorf("could not decode tx %d: %w", i, err)
		}
		txs = append(txs, &tx)
	}
	return types.NewBlockWithHeader(batch.BatchHeader).WithBody(types.Body{
		Transactions: txs,
		Uncles:       fullNodeBlock.Uncles(),
		Withdrawals:  fullNodeBlock.Withdrawals(),
	}), nil
}

package verifier

import (
	"math/big"
	"testing"

	"github.com/EspressoSystems/espresso-streamers/op/derivation"
	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/testutils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// Verifies l1OriginFromL2Block's fixed offsets (28 / 100) recover the L1 origin
// from L1-info deposit calldata produced by the real OP-stack encoder
// (derive.L1InfoDeposit), across every fork layout
func TestL1OriginFromL2Block_AgainstOPEncoders(t *testing.T) {
	targetL1Num := uint64(2_000)
	targetL1Hash := common.HexToHash("0xaabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")

	l1Info := &testutils.MockBlockInfo{
		InfoHash:        targetL1Hash,
		InfoNum:         targetL1Num,
		InfoTime:        1_000,
		InfoBaseFee:     big.NewInt(1_000_000_000),
		InfoBlobBaseFee: big.NewInt(1),
	}
	sysCfg := eth.SystemConfig{
		BatcherAddr: common.HexToAddress("0x00000000000000000000000000000000000000bb"),
	}

	forkTime := uint64(1_000)
	base := rollup.Config{BlockTime: 0}

	bedrock := base
	ecotone := base
	ecotone.EcotoneTime = &forkTime
	isthmus := ecotone
	isthmus.IsthmusTime = &forkTime
	jovian := isthmus
	jovian.JovianTime = &forkTime

	const l2Timestamp = 2000
	l1ChainCfg := &params.ChainConfig{}

	cases := []struct {
		name     string
		cfg      rollup.Config
		selector []byte
	}{
		{"bedrock", bedrock, derive.L1InfoFuncBedrockBytes4},
		{"ecotone", ecotone, derive.L1InfoFuncEcotoneBytes4},
		{"isthmus", isthmus, derive.L1InfoFuncIsthmusBytes4},
		{"jovian", jovian, derive.L1InfoFuncJovianBytes4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			depositTxn, err := derive.L1InfoDeposit(&tc.cfg, l1ChainCfg, sysCfg, 0, l1Info, l2Timestamp)
			require.NoError(t, err)

			// Confirm correct function selector
			require.Equal(t, tc.selector, depositTxn.Data[:functionSelectorLength], "%s: unexpected L1-info selector", tc.name)

			// Build an L2 block whose first tx is this L1-info deposit.
			block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(123)}).
				WithBody(types.Body{Transactions: types.Transactions{types.NewTx(depositTxn)}})

			blockId, err := l1OriginFromL2Block(block)
			require.NoError(t, err)
			require.Equal(t, targetL1Num, blockId.Number, "%s: L1 origin number", tc.name)
			require.Equal(t, targetL1Hash, blockId.Hash, "%s: L1 origin hash", tc.name)
		})
	}
}

// TestEspressoBatchToBlock checks that espressoBatchToBlock re-inserts the L1-info
// deposit as the first transaction, decodes the batch's opaque transactions in
// order, and pulls uncles/withdrawals from the full-node block.
func TestEspressoBatchToBlock(t *testing.T) {
	deposit := types.NewTx(&types.DepositTx{
		SourceHash: common.HexToHash("0x01"),
		Data:       []byte{0xde, 0xad},
	})

	// Create two transactions
	tx1 := types.NewTx(&types.LegacyTx{Nonce: 1, Gas: 21000, GasPrice: big.NewInt(1)})
	tx2 := types.NewTx(&types.DynamicFeeTx{Nonce: 2, Gas: 21000, GasFeeCap: big.NewInt(2), GasTipCap: big.NewInt(1)})
	raw1, err := tx1.MarshalBinary()
	require.NoError(t, err)
	raw2, err := tx2.MarshalBinary()
	require.NoError(t, err)

	header := &types.Header{Number: big.NewInt(123)}
	batch := &derivation.EspressoBatch{
		BatchHeader:   header,
		L1InfoDeposit: deposit,
		Batch:         derive.SingularBatch{Transactions: []hexutil.Bytes{raw1, raw2}},
	}

	// The full-node block contributes uncles/withdrawals
	withdrawals := types.Withdrawals{{Index: 1, Validator: 2, Address: common.HexToAddress("0x03"), Amount: 4}}
	uncles := []*types.Header{{Number: big.NewInt(122), Extra: []byte("uncle")}}
	fullNodeBlock := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(123)}).
		WithBody(types.Body{Withdrawals: withdrawals, Uncles: uncles})

	block, err := espressoBatchToBlock(fullNodeBlock, batch)
	require.NoError(t, err)

	txs := block.Transactions()
	require.Len(t, txs, 3)
	require.Equal(t, deposit.Hash(), txs[0].Hash(), "first tx should be the re-inserted deposit")
	require.Equal(t, tx1.Hash(), txs[1].Hash())
	require.Equal(t, tx2.Hash(), txs[2].Hash())

	require.Equal(t, withdrawals, block.Withdrawals(), "withdrawals should come from the full-node block")
	require.Equal(t, fullNodeBlock.Uncles(), block.Uncles(), "uncles should come from the full-node block")
	require.Len(t, block.Uncles(), 1, "uncle from the full-node block should be present")
	require.Equal(t, header.Number.Uint64(), block.NumberU64())
}

// TestEspressoBatchToBlock_BadTx checks that an undecodable transaction errors
func TestEspressoBatchToBlock_BadTx(t *testing.T) {
	batch := &derivation.EspressoBatch{
		BatchHeader:   &types.Header{Number: big.NewInt(1)},
		L1InfoDeposit: types.NewTx(&types.DepositTx{}),
		Batch:         derive.SingularBatch{Transactions: []hexutil.Bytes{{0xff, 0xff, 0xff}}},
	}
	fullNodeBlock := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})

	_, err := espressoBatchToBlock(fullNodeBlock, batch)
	require.ErrorContains(t, err, "could not decode tx 0")
}

func createBlockWithTxn(tx *types.Transaction) *types.Block {
	body := types.Body{}
	if tx != nil {
		body.Transactions = types.Transactions{tx}
	}
	return types.NewBlockWithHeader(&types.Header{Number: big.NewInt(123)}).WithBody(body)
}

func TestL1OriginFromL2Block_Rejects(t *testing.T) {
	// Long-enough deposit calldata, but an unrecognized leading selector.
	invalid := make([]byte, l1OriginHashOffsetEnd)
	copy(invalid, []byte{0x00, 0x00, 0x00, 0x00})
	unknownSelector := types.NewTx(&types.DepositTx{Data: invalid})

	// Deposit tx, recognized selector, but truncated below the hash offset.
	shortData := append([]byte{}, derive.L1InfoFuncEcotoneBytes4...)
	tooShort := types.NewTx(&types.DepositTx{Data: shortData})

	// A non-deposit first tx.
	nonDeposit := types.NewTx(&types.LegacyTx{Nonce: 1})

	cases := []struct {
		name  string
		block *types.Block
		msg   string
	}{
		{"unknown selector", createBlockWithTxn(unknownSelector), "unrecognized L1 info deposit selector"},
		{"too short", createBlockWithTxn(tooShort), "calldata too short"},
		{"non-deposit first tx", createBlockWithTxn(nonDeposit), "no L1 info deposit transaction"},
		{"no transactions", createBlockWithTxn(nil), "no L1 info deposit transaction"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := l1OriginFromL2Block(tc.block)
			require.ErrorContains(t, err, tc.msg)
		})
	}
}

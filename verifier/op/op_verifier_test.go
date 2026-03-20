package verifier

import (
	"context"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	espressoStore "proxy/store"
	opStreamer "proxy/streamer/op"

	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-service/dial"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockStreamer struct {
	mock.Mock
}

func (m *mockStreamer) Update(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}
func (m *mockStreamer) Refresh(ctx context.Context, finalizedL1 eth.L1BlockRef, safeBatchNumber uint64, safeL1Origin eth.BlockID) error {
	args := m.Called(ctx, finalizedL1, safeBatchNumber, safeL1Origin)
	return args.Error(0)
}

func (m *mockStreamer) RefreshSafeL1Origin(safeL1Origin eth.BlockID) {
	m.Called(safeL1Origin)
}
func (m *mockStreamer) Reset() {
	m.Called()
}

func (m *mockStreamer) UnmarshalBatch(b []byte) (*opStreamer.EspressoBatch, error) {
	args := m.Called(b)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*opStreamer.EspressoBatch), args.Error(1)
}

func (m *mockStreamer) HasNext(ctx context.Context) bool {
	args := m.Called(ctx)
	return args.Bool(0)
}

func (m *mockStreamer) Next(ctx context.Context) *opStreamer.EspressoBatch {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*opStreamer.EspressoBatch)
}

func (m *mockStreamer) Peek(ctx context.Context) *opStreamer.EspressoBatch {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*opStreamer.EspressoBatch)
}

func (m *mockStreamer) GetFallbackHotshotPos() uint64 {
	args := m.Called()
	return args.Get(0).(uint64)
}

type mockEndpointProvider struct {
	mock.Mock
}

func (m *mockEndpointProvider) RollupClient(ctx context.Context) (dial.RollupClientInterface, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(dial.RollupClientInterface), args.Error(1)
}

func (m *mockEndpointProvider) EthClient(ctx context.Context) (dial.EthClientInterface, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(dial.EthClientInterface), args.Error(1)
}

func (m *mockEndpointProvider) Close() {}

type mockRollupClient struct {
	mock.Mock
}

func (m *mockRollupClient) SyncStatus(ctx context.Context) (*eth.SyncStatus, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*eth.SyncStatus), args.Error(1)
}

func (m *mockRollupClient) RollupConfig(ctx context.Context) (*rollup.Config, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rollup.Config), args.Error(1)
}

func (m *mockRollupClient) OutputAtBlock(ctx context.Context, blockNum uint64) (*eth.OutputResponse, error) {
	panic("not implemented")
}

func (m *mockRollupClient) StartSequencer(ctx context.Context, unsafeHead common.Hash) error {
	panic("not implemented")
}

func (m *mockRollupClient) SequencerActive(ctx context.Context) (bool, error) {
	panic("not implemented")
}
func (m *mockRollupClient) Close() {}

type mockEthClient struct {
	mock.Mock
}

func (m *mockEthClient) BlockByNumber(ctx context.Context, number *big.Int) (*types.Block, error) {
	args := m.Called(ctx, number)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.Block), args.Error(1)
}

func (m *mockEthClient) Client() *rpc.Client { return nil }
func (m *mockEthClient) Close()              {}

type testHarness struct {
	verifier     *OpVerifier
	streamer     *mockStreamer
	endpointProv *mockEndpointProvider
	rollupClient *mockRollupClient
	ethClient    *mockEthClient
	store        *espressoStore.EspressoStore
}

func tempFilePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state.json")
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	streamer := new(mockStreamer)
	endpointProvider := new(mockEndpointProvider)
	rollupClient := new(mockRollupClient)
	ethClient := new(mockEthClient)
	store, err := espressoStore.NewEspressoStore(tempFilePath(t), 1, 1)
	require.NoError(t, err)
	verifier := &OpVerifier{
		streamer:      streamer,
		espressoStore: store,
		opVerifierConfig: &OpVerifierConfig{
			VerificationInterval: time.Millisecond,
		},
		endpointProvider: endpointProvider,
		rollupConfig:     &rollup.Config{},
		logger:           log.NewLogger(log.DiscardHandler()),
	}
	return &testHarness{
		verifier:     verifier,
		streamer:     streamer,
		endpointProv: endpointProvider,
		rollupClient: rollupClient,
		ethClient:    ethClient,
		store:        store,
	}

}

func TestAdvanceStreamerAndEspressoState(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	h.streamer.On("GetFallbackHotshotPos").Return(uint64(2))
	h.streamer.On("Next", mock.Anything).Return(nil)

	err := h.verifier.advanceStreamerAndEspressoState(ctx, 100)
	require.NoError(t, err)

	state, err := h.store.GetState()
	require.NoError(t, err)
	require.Equal(t, uint64(2), state.FallbackHotshotHeight)
	require.Equal(t, uint64(100), state.L2BlockNumber)
	h.streamer.AssertCalled(t, "Next", mock.Anything)
	h.streamer.AssertExpectations(t)
}

func TestPeekNextBatch(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()
	batch := &opStreamer.EspressoBatch{
		BatchHeader: &types.Header{Number: big.NewInt(100)},
	}
	syncStatus := &eth.SyncStatus{
		FinalizedL1: eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
		SafeL2: eth.L2BlockRef{
			Number:   5,
			L1Origin: eth.BlockID{Number: 10, Hash: common.Hash{1}},
		},
	}
	h.endpointProv.On("RollupClient", mock.Anything).Return(h.rollupClient, nil)
	h.rollupClient.On("SyncStatus", mock.Anything).Return(syncStatus, nil)
	h.streamer.On("Refresh", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	h.streamer.On("HasNext", mock.Anything).Return(true).Once()
	h.streamer.On("Peek", mock.Anything).Return(batch).Once()

	peekedBatch, err := h.verifier.peekNextBatch(ctx)
	require.NoError(t, err)
	require.Equal(t, batch, peekedBatch)
	h.streamer.AssertNotCalled(t, "Update", mock.Anything)

	h.streamer.On("HasNext", mock.Anything).Return(false)
	h.streamer.On("Update", mock.Anything).Return(nil)
	h.streamer.On("Peek", mock.Anything).Return(nil)

	result, err := h.verifier.peekNextBatch(ctx)
	require.NoError(t, err)
	require.Nil(t, result)
	h.streamer.AssertCalled(t, "Update", mock.Anything)
}

func TestVerify(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	l1InfoData := make([]byte, 4+32*8)
	selector := crypto.Keccak256([]byte("setL1BlockValues(uint64,uint64,uint256,bytes32,uint64,bytes32,uint256,uint256)"))[:4]
	copy(l1InfoData[:4], selector)

	depositTx := types.NewTx(&types.DepositTx{
		Data: l1InfoData,
	})
	blockHeader := &types.Header{Number: big.NewInt(100)}
	block := types.NewBlockWithHeader(blockHeader).WithBody(types.Body{
		Transactions: []*types.Transaction{depositTx},
	})

	// Derive the expected EspressoBatch from the block so the RLP comparison in verify() passes
	batch, err := opStreamer.BlockToEspressoBatch(h.verifier.rollupConfig, block)
	require.NoError(t, err)

	syncStatus := &eth.SyncStatus{
		FinalizedL1: eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
		SafeL2: eth.L2BlockRef{
			Number:   5,
			L1Origin: eth.BlockID{Number: 10, Hash: common.Hash{1}},
		},
	}
	h.endpointProv.On("RollupClient", mock.Anything).Return(h.rollupClient, nil)
	h.endpointProv.On("EthClient", mock.Anything).Return(h.ethClient, nil)
	h.ethClient.On("BlockByNumber", mock.Anything, mock.Anything).Return(block, nil)
	h.rollupClient.On("SyncStatus", mock.Anything).Return(syncStatus, nil)
	h.streamer.On("Refresh", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	h.streamer.On("HasNext", mock.Anything).Return(true).Once()
	h.streamer.On("Peek", mock.Anything).Return(batch).Once()
	h.streamer.On("GetFallbackHotshotPos").Return(uint64(2))
	h.streamer.On("Next", mock.Anything).Return(batch)

	h.verifier.verify(ctx)

	state, err := h.store.GetState()
	require.NoError(t, err)
	require.Equal(t, uint64(2), state.FallbackHotshotHeight)
	require.Equal(t, uint64(100), state.L2BlockNumber)
}

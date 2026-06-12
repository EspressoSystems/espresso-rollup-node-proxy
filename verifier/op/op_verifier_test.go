package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	sharedVerifier "proxy/verifier"
	"testing"
	"time"

	"proxy/adapters"
	proxyhttp "proxy/http"
	"proxy/jsonrpcv2"
	proxypkg "proxy/proxy"

	espressoStore "proxy/store"

	opStreamer "github.com/EspressoSystems/espresso-streamers/op"
	"github.com/EspressoSystems/espresso-streamers/op/derivation"

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

type logCapturer struct {
	errorMessages []string
}

func (c *logCapturer) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (c *logCapturer) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		c.errorMessages = append(c.errorMessages, r.Message)
	}
	return nil
}
func (c *logCapturer) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *logCapturer) WithGroup(_ string) slog.Handler      { return c }

type mockFinalityPoller struct {
	mock.Mock
}

func (m *mockFinalityPoller) LastSnapshot() (*eth.SyncStatus, bool) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, false
	}
	return args.Get(0).(*eth.SyncStatus), args.Bool(1)
}
func (m *mockFinalityPoller) Start(_ context.Context) {}
func (m *mockFinalityPoller) Stop()                   {}

var _ sharedVerifier.FinalityPollerInterface[*eth.SyncStatus] = (*mockFinalityPoller)(nil)

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

func (m *mockStreamer) UnmarshalBatch(b []byte) (*derivation.EspressoBatch, error) {
	args := m.Called(b)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*derivation.EspressoBatch), args.Error(1)
}

func (m *mockStreamer) HasNext(ctx context.Context) bool {
	args := m.Called(ctx)
	return args.Bool(0)
}

func (m *mockStreamer) Next(ctx context.Context) *derivation.EspressoBatch {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*derivation.EspressoBatch)
}

func (m *mockStreamer) Peek(ctx context.Context) *derivation.EspressoBatch {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*derivation.EspressoBatch)
}

func (m *mockStreamer) GetFallbackHotshotPos() uint64 {
	args := m.Called()
	return args.Get(0).(uint64)
}

func (m *mockStreamer) GetBatchFinalizationTimestamp(hash common.Hash) (uint64, bool) {
	args := m.Called(hash)
	return args.Get(0).(uint64), args.Bool(1)
}

func (m *mockStreamer) SetProperHead(_ common.Hash) {}

var _ opStreamer.EspressoStreamer[derivation.EspressoBatch] = (*mockStreamer)(nil)

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
	verifier       *OPEspressoBatchVerifier
	streamer       *mockStreamer
	endpointProv   *mockEndpointProvider
	rollupClient   *mockRollupClient
	ethClient      *mockEthClient
	finalityPoller *mockFinalityPoller
	store          *espressoStore.EspressoStore
}

func tempFilePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state.json")
}

func newTestHarness(t *testing.T, logger log.Logger) *testHarness {
	t.Helper()
	if logger == nil {
		logger = log.NewLogger(log.DiscardHandler())
	}
	streamer := new(mockStreamer)
	endpointProvider := new(mockEndpointProvider)
	rollupClient := new(mockRollupClient)
	ethClient := new(mockEthClient)
	finalityPoller := new(mockFinalityPoller)
	store, err := espressoStore.NewEspressoStore(tempFilePath(t), 1)
	require.NoError(t, err)
	updated, err := store.UpdateIfGreater(1, 1)
	require.True(t, updated)
	require.NoError(t, err)
	verifier := &OPEspressoBatchVerifier{
		streamer:      streamer,
		espressoStore: store,
		config: &OPEspressoBatchVerifierConfig{
			VerificationInterval: time.Millisecond,
		},
		endpointProvider: endpointProvider,
		rollupConfig:     &rollup.Config{},
		logger:           logger,
		finalityPoller:   finalityPoller,
	}
	return &testHarness{
		verifier:       verifier,
		streamer:       streamer,
		endpointProv:   endpointProvider,
		rollupClient:   rollupClient,
		ethClient:      ethClient,
		finalityPoller: finalityPoller,
		store:          store,
	}
}

func TestPeekNextBatch(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()
	batch := &derivation.EspressoBatch{
		BatchHeader: &types.Header{Number: big.NewInt(100)},
	}
	syncStatus := &eth.SyncStatus{
		FinalizedL1: eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
		SafeL2: eth.L2BlockRef{
			Number:   5,
			L1Origin: eth.BlockID{Number: 10, Hash: common.Hash{1}},
		},
	}
	h.finalityPoller.On("LastSnapshot").Return(syncStatus, true)
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
	capturer := &logCapturer{}
	h := newTestHarness(t, log.NewLogger(capturer))
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
	batch, err := derivation.BlockToEspressoBatch(h.verifier.rollupConfig, block)
	require.NoError(t, err)

	syncStatus := &eth.SyncStatus{
		FinalizedL1: eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
		SafeL2: eth.L2BlockRef{
			Number:   5,
			L1Origin: eth.BlockID{Number: 10, Hash: common.Hash{1}},
		},
	}
	h.endpointProv.On("RollupClient", mock.Anything).Return(h.rollupClient, nil)
	h.finalityPoller.On("LastSnapshot").Return(syncStatus, true)
	h.endpointProv.On("EthClient", mock.Anything).Return(h.ethClient, nil)
	h.ethClient.On("BlockByNumber", mock.Anything, new(big.Int).SetUint64(100)).Return(block, nil)
	h.rollupClient.On("SyncStatus", mock.Anything).Return(syncStatus, nil)
	h.streamer.On("Refresh", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	h.streamer.On("HasNext", mock.Anything).Return(true).Once()
	h.streamer.On("HasNext", mock.Anything).Return(false)
	h.streamer.On("Peek", mock.Anything).Return(batch).Once()
	h.streamer.On("Update", mock.Anything).Return(nil)
	h.streamer.On("Peek", mock.Anything).Return(nil)
	h.streamer.On("GetFallbackHotshotPos").Return(uint64(2))
	h.streamer.On("Next", mock.Anything).Return(batch)

	h.verifier.verifyAndAdvance(ctx)

	require.Empty(t, capturer.errorMessages, "verifyAndAdvance() should not produce error logs on success")

	state := h.store.GetState()
	require.Equal(t, uint64(2), state.FallbackHotshotHeight)
	require.Equal(t, uint64(100), state.L2BlockNumber)
}

func TestStoresEthereumFinalizedBlockWhenAhead(t *testing.T) {
	assertEthereumFinalizedBlockStored := func(t *testing.T, h *testHarness, capturer *logCapturer, expectedFallbackPos uint64) {
		t.Helper()

		require.Contains(t, capturer.errorMessages, "ethereum finalized block is ahead of espresso finalized block")

		state := h.store.GetState()
		require.Equal(t, expectedFallbackPos, state.FallbackHotshotHeight)
		require.Equal(t, uint64(105), state.L2BlockNumber)
	}

	t.Run("verify and advance with no batches", func(t *testing.T) {
		capturer := &logCapturer{}
		h := newTestHarness(t, log.NewLogger(capturer))
		ctx := context.Background()
		syncStatus := &eth.SyncStatus{
			FinalizedL1: eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
			FinalizedL2: eth.L2BlockRef{Number: 105},
			SafeL2: eth.L2BlockRef{
				Number:   5,
				L1Origin: eth.BlockID{Number: 10, Hash: common.Hash{1}},
			},
		}

		h.streamer.On("GetFallbackHotshotPos").Return(uint64(1))
		h.endpointProv.On("EthClient", mock.Anything).Return(h.ethClient, nil)
		h.endpointProv.On("RollupClient", mock.Anything).Return(h.rollupClient, nil)
		h.finalityPoller.On("LastSnapshot").Return(syncStatus, true)
		h.rollupClient.On("SyncStatus", mock.Anything).Return(syncStatus, nil)
		h.streamer.On("Refresh", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		h.streamer.On("HasNext", mock.Anything).Return(false)
		h.streamer.On("Update", mock.Anything).Return(nil)
		h.streamer.On("Peek", mock.Anything).Return(nil)

		h.verifier.verifyAndAdvance(ctx)

		assertEthereumFinalizedBlockStored(t, h, capturer, 1)
		h.streamer.AssertNotCalled(t, "Next", mock.Anything)
	})
}

func TestProxyUsesEthereumFinalizedBlockWhenEspressoStopsAdvancing(t *testing.T) {
	capturer := &logCapturer{}
	h := newTestHarness(t, log.NewLogger(capturer))
	ctx := context.Background()
	syncStatus := &eth.SyncStatus{
		FinalizedL1: eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
		FinalizedL2: eth.L2BlockRef{Number: 105},
		SafeL2: eth.L2BlockRef{
			Number:   5,
			L1Origin: eth.BlockID{Number: 10, Hash: common.Hash{1}},
		},
	}

	h.streamer.On("GetFallbackHotshotPos").Return(uint64(1))
	h.endpointProv.On("EthClient", mock.Anything).Return(h.ethClient, nil)
	h.endpointProv.On("RollupClient", mock.Anything).Return(h.rollupClient, nil)
	h.finalityPoller.On("LastSnapshot").Return(syncStatus, true)
	h.rollupClient.On("SyncStatus", mock.Anything).Return(syncStatus, nil)
	h.streamer.On("Refresh", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	h.streamer.On("HasNext", mock.Anything).Return(false)
	h.streamer.On("Update", mock.Anything).Return(nil)
	h.streamer.On("Peek", mock.Anything).Return(nil)

	var upstreamSeenTags []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var req jsonrpcv2.Request
		require.NoError(t, json.Unmarshal(body, &req))

		params, castOK := req.Params.([]any)
		require.True(t, castOK)
		require.Len(t, params, 2)

		tag, ok := params[0].(string)
		require.True(t, ok)
		upstreamSeenTags = append(upstreamSeenTags, tag)

		resp := jsonrpcv2.Response{
			ID:     req.ID,
			Result: json.RawMessage(`{"number":"` + tag + `"}`),
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer upstream.Close()

	upstreamURL := &url.URL{
		Scheme: "http",
		Host:   upstream.Listener.Addr().String(),
	}
	reverseProxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	interceptor := proxypkg.NewInterceptor(h.store, "finalized", proxypkg.DefaultMaxBatchSize)
	handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), 0, adapters.NewHTTPJSONRPCInterceptor(reverseProxy, interceptor))
	callProxy := func() string {
		reqBody := `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["finalized",false]}`
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var resp jsonrpcv2.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Nil(t, resp.Error)

		require.Nil(t, resp.Error)
		cast, castOK := resp.Result.(map[string]any)
		require.True(t, castOK)
		require.NotNil(t, cast)
		require.Contains(t, cast, "number")

		numberField, ok := cast["number"]
		require.True(t, ok)

		number, castOK := numberField.(string)
		require.True(t, castOK)

		return number
	}

	require.Equal(t, "0x1", callProxy())

	h.verifier.verifyAndAdvance(ctx)

	require.Contains(t, capturer.errorMessages, "ethereum finalized block is ahead of espresso finalized block")
	require.Equal(t, uint64(105), h.store.GetState().L2BlockNumber)
	require.Equal(t, "0x69", callProxy())
	require.Equal(t, []string{"0x1", "0x69"}, upstreamSeenTags)
}

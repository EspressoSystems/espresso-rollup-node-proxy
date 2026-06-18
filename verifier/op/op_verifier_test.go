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
	"testing"
	"time"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/adapters"
	proxyhttp "github.com/EspressoSystems/espresso-rollup-node-proxy/http"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"
	proxypkg "github.com/EspressoSystems/espresso-rollup-node-proxy/proxy"
	sharedVerifier "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier"

	espressoStore "github.com/EspressoSystems/espresso-rollup-node-proxy/store"

	opStreamer "github.com/EspressoSystems/espresso-streamers/op"
	"github.com/EspressoSystems/espresso-streamers/op/derivation"

	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
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

func (m *mockFinalityPoller) LastSnapshot() (opFinalitySnapshot, bool) {
	args := m.Called()
	if args.Get(0) == nil {
		return opFinalitySnapshot{}, false
	}
	return args.Get(0).(opFinalitySnapshot), args.Bool(1)
}
func (m *mockFinalityPoller) Start(_ context.Context) {}
func (m *mockFinalityPoller) Stop()                   {}

var _ sharedVerifier.FinalityPollerInterface[opFinalitySnapshot] = (*mockFinalityPoller)(nil)

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

func (m *mockEthClient) Close() {}

var _ ExecutionClient = (*mockEthClient)(nil)

type testHarness struct {
	verifier       *OPEspressoBatchVerifier
	streamer       *mockStreamer
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
		l2Client:       ethClient,
		logger:         logger,
		finalityPoller: finalityPoller,
	}
	return &testHarness{
		verifier:       verifier,
		streamer:       streamer,
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

	block := createOpBlock(100, eth.BlockID{Number: 5, Hash: common.Hash{0xaa}})

	// Derive the expected EspressoBatch from the block so its BatchHeader matches
	// the full node block hash in VerifyNextBatch.
	batch, err := derivation.BlockToEspressoBatch(&rollup.Config{}, block)
	require.NoError(t, err)

	snapshot := opFinalitySnapshot{
		finalizedL1: eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
	}
	h.finalityPoller.On("LastSnapshot").Return(snapshot, true)
	h.ethClient.On("BlockByNumber", mock.Anything, new(big.Int).SetUint64(100)).Return(block, nil)
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

// TestVerifyRejectsMismatchedBlock ensures that when the full node returns a
// block of the same form as the one Espresso finalized but differing in its body
// (here, the L1 origin in the L1-info deposit), verification fails and the store
// is not advanced.
func TestVerifyRejectsMismatchedBlock(t *testing.T) {
	capturer := &logCapturer{}
	h := newTestHarness(t, log.NewLogger(capturer))
	ctx := context.Background()

	// Espresso finalized a block carrying L1 origin A...
	block := createOpBlock(100, eth.BlockID{Number: 5, Hash: common.Hash{0xaa}})
	batch, err := derivation.BlockToEspressoBatch(&rollup.Config{}, block)
	require.NoError(t, err)

	// ...but the full node returns one differing only in its L1 origin (B). The
	// headers are identical, so the hashes match and only the RLP body comparison
	// catches the difference.
	mismatchedBlock := createOpBlock(100, eth.BlockID{Number: 6, Hash: common.Hash{0xbb}})
	require.Equal(t, block.Hash(), mismatchedBlock.Hash(), "headers are identical, only the body differs")
	require.NotEqual(t, block, mismatchedBlock, "blocks must differ in their L1-info deposit body")

	snapshot := opFinalitySnapshot{
		finalizedL1: eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
	}
	h.finalityPoller.On("LastSnapshot").Return(snapshot, true)
	h.ethClient.On("BlockByNumber", mock.Anything, new(big.Int).SetUint64(100)).Return(mismatchedBlock, nil)
	h.streamer.On("Refresh", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	h.streamer.On("HasNext", mock.Anything).Return(true)
	h.streamer.On("Peek", mock.Anything).Return(batch)

	h.verifier.verifyAndAdvance(ctx)

	require.Contains(t, capturer.errorMessages, "batch verification failed",
		"expected a verification failure to be logged")
	h.streamer.AssertNotCalled(t, "Next", mock.Anything)
	state := h.store.GetState()
	require.Equal(t, uint64(1), state.L2BlockNumber, "store must not advance on a mismatched block")
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
		snapshot := opFinalitySnapshot{
			finalizedL1:       eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
			finalizedL2Number: 105,
		}

		h.streamer.On("GetFallbackHotshotPos").Return(uint64(1))
		h.finalityPoller.On("LastSnapshot").Return(snapshot, true)
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
	snapshot := opFinalitySnapshot{
		finalizedL1:       eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
		finalizedL2Number: 105,
	}

	h.streamer.On("GetFallbackHotshotPos").Return(uint64(1))
	h.finalityPoller.On("LastSnapshot").Return(snapshot, true)
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

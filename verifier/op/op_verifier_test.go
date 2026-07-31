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

func (m *mockStreamer) Start(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockStreamer) Stop() {
	m.Called()
}

func (m *mockStreamer) Peek(ctx context.Context) *derivation.EspressoBatch {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*derivation.EspressoBatch)
}

func (m *mockStreamer) AdvancePosition() {
	m.Called()
}

func (m *mockStreamer) SetBatchPosition(l2Head eth.L2BlockRef) {
	m.Called(l2Head)
}

func (m *mockStreamer) GetFallbackHotshotPos() uint64 {
	args := m.Called()
	return args.Get(0).(uint64)
}

var _ EspressoStreamer = (*mockStreamer)(nil)

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

// TestVerifyNextBatchDoesNotConsumeBatch checks that a peeked batch is left in place
// for the streamer to serve again: the verifier advances only once the full node block
// has been verified against it.
func TestVerifyNextBatchDoesNotConsumeBatch(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()

	block := createOpBlock(100, eth.BlockID{Number: 5, Hash: common.Hash{0xaa}})
	batch, err := derivation.BlockToEspressoBatch(&rollup.Config{}, block)
	require.NoError(t, err)

	h.ethClient.On("BlockByNumber", mock.Anything, new(big.Int).SetUint64(100)).Return(block, nil)
	h.streamer.On("Peek", mock.Anything).Return(batch)

	verified, err := h.verifier.VerifyNextBatch(ctx)
	require.NoError(t, err)
	require.Equal(t, batch, verified)
	h.streamer.AssertNotCalled(t, "AdvancePosition")
}

// TestFallbackHotshotPosIsFloored covers the streamer reporting a fallback HotShot
// height below the one it was seeded with, which the light client does when it has no
// finalized state to report. Persisting a zero there would make the proxy's interceptor
// read the store as uninitialized and stop serving the espresso tag.
func TestFallbackHotshotPosIsFloored(t *testing.T) {
	h := newTestHarness(t, nil)
	h.verifier.originHotshotPos = 7

	h.streamer.On("GetFallbackHotshotPos").Return(uint64(0)).Once()
	require.Equal(t, uint64(7), h.verifier.fallbackHotshotPos(),
		"a reported height below the seed must be floored at the seed")

	// A height above the seed is the streamer making real progress, so take it.
	h.streamer.On("GetFallbackHotshotPos").Return(uint64(42))
	require.Equal(t, uint64(42), h.verifier.fallbackHotshotPos())
}

// TestVerifyNextBatchNoBatch covers the streamer having nothing to serve.
func TestVerifyNextBatchNoBatch(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()

	h.streamer.On("Peek", mock.Anything).Return(nil)

	verified, err := h.verifier.VerifyNextBatch(ctx)
	require.NoError(t, err)
	require.Nil(t, verified)
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
		finalizedEth: eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
	}
	h.finalityPoller.On("LastSnapshot").Return(snapshot, true)
	h.ethClient.On("BlockByNumber", mock.Anything, new(big.Int).SetUint64(100)).Return(block, nil)
	h.streamer.On("Peek", mock.Anything).Return(batch).Once()
	h.streamer.On("Peek", mock.Anything).Return(nil)
	h.streamer.On("GetFallbackHotshotPos").Return(uint64(2))
	h.streamer.On("AdvancePosition").Return()

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
		finalizedEth: eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
	}
	h.finalityPoller.On("LastSnapshot").Return(snapshot, true)
	h.ethClient.On("BlockByNumber", mock.Anything, new(big.Int).SetUint64(100)).Return(mismatchedBlock, nil)
	h.streamer.On("Peek", mock.Anything).Return(batch)

	h.verifier.verifyAndAdvance(ctx)

	require.Contains(t, capturer.errorMessages, "batch verification failed",
		"expected a verification failure to be logged")
	h.streamer.AssertNotCalled(t, "AdvancePosition")
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
			finalizedEth:      eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
			finalizedL2Number: 105,
		}

		h.streamer.On("GetFallbackHotshotPos").Return(uint64(1))
		h.finalityPoller.On("LastSnapshot").Return(snapshot, true)
		h.streamer.On("SetBatchPosition", mock.Anything).Return()
		h.streamer.On("Peek", mock.Anything).Return(nil)

		h.verifier.verifyAndAdvance(ctx)

		assertEthereumFinalizedBlockStored(t, h, capturer, 1)
		h.streamer.AssertNotCalled(t, "AdvancePosition")
		// Fast-forwarding past where the streamer was must re-anchor it on the
		// Ethereum-finalized head, so it serves that block's child next.
		h.streamer.AssertCalled(t, "SetBatchPosition", eth.L2BlockRef{
			Number: 105,
			Hash:   snapshot.finalizedL2Hash,
		})
	})
}

func TestProxyUsesEthereumFinalizedBlockWhenEspressoStopsAdvancing(t *testing.T) {
	capturer := &logCapturer{}
	h := newTestHarness(t, log.NewLogger(capturer))
	ctx := context.Background()
	snapshot := opFinalitySnapshot{
		finalizedEth:      eth.L1BlockRef{Number: 10, Hash: common.Hash{1}},
		finalizedL2Number: 105,
	}

	h.streamer.On("GetFallbackHotshotPos").Return(uint64(1))
	h.finalityPoller.On("LastSnapshot").Return(snapshot, true)
	h.streamer.On("SetBatchPosition", mock.Anything).Return()
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
	interceptor := proxypkg.NewInterceptor(log.Root(), h.store, "finalized", proxypkg.DefaultMaxBatchSize)
	handler := proxyhttp.HTTPRPCMiddlewares(log.Root(), 0, adapters.NewHTTPJSONRPCInterceptor(log.Root(), reverseProxy, interceptor))
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

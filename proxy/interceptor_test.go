package proxy

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/adapters"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"
	espressostore "github.com/EspressoSystems/espresso-rollup-node-proxy/store"

	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, l2BlockNumber uint64) *espressostore.EspressoStore {
	t.Helper()
	fp := filepath.Join(t.TempDir(), "state.json")
	store, err := espressostore.NewEspressoStore(fp, 1)
	require.NoError(t, err)
	updated, err := store.UpdateIfGreater(l2BlockNumber, 1)
	require.True(t, updated)
	require.NoError(t, err)
	return store
}

func TestInterceptor(t *testing.T) {
	const blockNumber uint64 = 100
	t.Run("returns original when no params", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"espresso"}, DefaultMaxBatchSize)

		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","latest"]}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		require.JSONEq(t, input, string(result))
	})

	t.Run("replaces espresso tag in string param", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"espresso"}, DefaultMaxBatchSize)
		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["espresso"],"foo":"bar"}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x64"],"foo":"bar"}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("replaces finalized tag when configured as espresso tag", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"finalized"}, DefaultMaxBatchSize)
		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","finalized"]}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","0x64"]}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("replaces safe and finalized tags when both configured", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"safe", "finalized"}, DefaultMaxBatchSize)

		input := `[{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["safe",false]},{"jsonrpc":"2.0","id":2,"method":"eth_getBlockByNumber","params":["finalized",false]},{"jsonrpc":"2.0","id":3,"method":"eth_getBlockByNumber","params":["latest",false]}]`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `[{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x64",false]},{"jsonrpc":"2.0","id":2,"method":"eth_getBlockByNumber","params":["0x64",false]},{"jsonrpc":"2.0","id":3,"method":"eth_getBlockByNumber","params":["latest",false]}]`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("replaces tag in nested json object param", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"espresso"}, DefaultMaxBatchSize)
		input := `{"jsonrpc":"2.0","id":1,"method":"eth_call","params":{"to":"0xabc","data":"0x123","blockTag":"espresso"}}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"eth_call","params":{"to":"0xabc","data":"0x123","blockTag":"0x64"}}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("replaces tag in array json nested structure", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"finalized"}, DefaultMaxBatchSize)

		input := `{"jsonrpc":"2.0","id":1,"method":"m","params":[{"nested":["finalized"]}]}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"m","params":[{"nested":["0x64"]}]}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("passes through params with only non-string primitives unchanged as they cant contain espresso tag", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"espresso"}, DefaultMaxBatchSize)

		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["latest",true]}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		require.JSONEq(t, input, string(result))
	})

	t.Run("intercepts batch request replacing tags in each element", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"espresso"}, DefaultMaxBatchSize)

		input := `[{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","espresso"]},{"jsonrpc":"2.0","id":2,"method":"eth_getBlockByNumber","params":["espresso",true]}]`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `[{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","0x64"]},{"jsonrpc":"2.0","id":2,"method":"eth_getBlockByNumber","params":["0x64",true]}]`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("passes through batch request without espresso tags unchanged", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"espresso"}, DefaultMaxBatchSize)

		input := `[{"jsonrpc":"2.0","id":1,"method":"eth_chainId"},{"jsonrpc":"2.0","id":2,"method":"eth_getBalance","params":["0xabc","latest"]}]`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		require.JSONEq(t, input, string(result))
	})

	t.Run("InterceptBatchRequests returns nil slice on error", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		const maxBatch = 2
		interceptor := NewInterceptor(nil, store, []string{"espresso"}, maxBatch)

		// Build a batch that exceeds the limit
		requests := make([]jsonrpcv2.Request, maxBatch+1)
		result, err := interceptor.InterceptBatchRequests(requests)
		require.Error(t, err)
		require.Nil(t, result)
	})
}

// FuzzReplaceTagInParams verifies that replaceTagInParams does not panic or
// crash on arbitrary JSON-shaped inputs. The function processes untrusted
// request params and must handle any valid (or structurally unexpected) JSON
// value without panicking or exceeding the stack.
func FuzzReplaceTagInParams(f *testing.F) {
	// Seed corpus: representative params shapes seen in production
	f.Add(`["espresso", false]`)
	f.Add(`{"blockTag": "espresso"}`)
	f.Add(`"espresso"`)
	f.Add(`null`)
	f.Add(`42`)
	f.Add(`[{"nested": ["espresso"]}]`)
	f.Add(`[{"a":{"b":{"c":{"d":"espresso"}}}}]`)
	f.Add(`""`)
	f.Add(`[]`)
	f.Add(`{}`)

	i := &interceptor{espressoTags: []string{"espresso"}}

	f.Fuzz(func(t *testing.T, raw string) {
		var params any
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return // skip structurally invalid JSON
		}

		result, _, err := i.replaceTagInParams(params, 42, 0)
		if err != nil {
			return // error paths are expected and acceptable
		}

		// Verify the result is still marshallable — an un-marshallable result
		// would indicate the function introduced an invalid value.
		if _, err := json.Marshal(result); err != nil {
			t.Fatalf("replaceTagInParams returned un-marshallable result: %v", err)
		}
	})
}

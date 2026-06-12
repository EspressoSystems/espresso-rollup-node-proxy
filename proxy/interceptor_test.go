package proxy

import (
	"path/filepath"
	"testing"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/adapters"
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
		interceptor := NewInterceptor(store, "espresso", DefaultMaxBatchSize)

		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","latest"]}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		require.JSONEq(t, input, string(result))
	})

	t.Run("replaces espresso tag in string param", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso", DefaultMaxBatchSize)
		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["espresso"],"foo":"bar"}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x64"],"foo":"bar"}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("replaces finalized tag when configured as espresso tag", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "finalized", DefaultMaxBatchSize)
		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","finalized"]}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","0x64"]}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("replaces tag in nested json object param", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso", DefaultMaxBatchSize)
		input := `{"jsonrpc":"2.0","id":1,"method":"eth_call","params":{"to":"0xabc","data":"0x123","blockTag":"espresso"}}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"eth_call","params":{"to":"0xabc","data":"0x123","blockTag":"0x64"}}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("replaces tag in array json nested structure", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "finalized", DefaultMaxBatchSize)

		input := `{"jsonrpc":"2.0","id":1,"method":"m","params":[{"nested":["finalized"]}]}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"m","params":[{"nested":["0x64"]}]}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("passes through params with only non-string primitives unchanged as they cant contain espresso tag", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso", DefaultMaxBatchSize)

		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["latest",true]}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		require.JSONEq(t, input, string(result))
	})

	t.Run("intercepts batch request replacing tags in each element", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso", DefaultMaxBatchSize)

		input := `[{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","espresso"]},{"jsonrpc":"2.0","id":2,"method":"eth_getBlockByNumber","params":["espresso",true]}]`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `[{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","0x64"]},{"jsonrpc":"2.0","id":2,"method":"eth_getBlockByNumber","params":["0x64",true]}]`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("passes through batch request without espresso tags unchanged", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso", DefaultMaxBatchSize)

		input := `[{"jsonrpc":"2.0","id":1,"method":"eth_chainId"},{"jsonrpc":"2.0","id":2,"method":"eth_getBalance","params":["0xabc","latest"]}]`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		require.JSONEq(t, input, string(result))
	})
}

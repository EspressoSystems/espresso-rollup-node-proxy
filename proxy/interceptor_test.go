package proxy

import (
	"path/filepath"
	espressoStore "proxy/store"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, l2BlockNumber uint64) *espressoStore.EspressoStore {
	t.Helper()
	fp := filepath.Join(t.TempDir(), "state.json")
	store, err := espressoStore.NewEspressoStore(fp, 1, l2BlockNumber)
	require.NoError(t, err)
	return store
}

func TestInterceptor(t *testing.T) {
	const blockNumber uint64 = 100
	t.Run("returns original when no params", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso")

		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","latest"]}`
		result, err := interceptor.Intercept([]byte(input))
		require.NoError(t, err)
		require.JSONEq(t, input, string(result))
	})

	t.Run("replaces espresso tag in string param", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso")
		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["espresso"]}`
		result, err := interceptor.Intercept([]byte(input))
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x64"]}`
		require.Equal(t, expected, string(result))
	})

	t.Run("replaces finalized tag when configured as espresso tag", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "finalized")
		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","finalized"]}`
		result, err := interceptor.Intercept([]byte(input))
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","0x64"]}`
		require.Equal(t, expected, string(result))
	})

	t.Run("replaces tag in nested json object param", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso")
		input := `{"jsonrpc":"2.0","id":1,"method":"eth_call","params":{"to":"0xabc","data":"0x123","blockTag":"espresso"}}`
		result, err := interceptor.Intercept([]byte(input))
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"eth_call","params":{"to":"0xabc","data":"0x123","blockTag":"0x64"}}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("replaces tag in array json nested structure", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "finalized")

		input := `{"jsonrpc":"2.0","id":1,"method":"m","params":[{"nested":["finalized"]}]}`
		result, err := interceptor.Intercept([]byte(input))
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"m","params":[{"nested":["0x64"]}]}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("passes through params with only non-string primitives unchanged as they cant contain espresso tag", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso")

		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["latest",true]}`
		result, err := interceptor.Intercept([]byte(input))
		require.NoError(t, err)
		require.JSONEq(t, input, string(result))
	})

	t.Run("intercepts batch request replacing tags in each element", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso")

		input := `[{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","espresso"]},{"jsonrpc":"2.0","id":2,"method":"eth_getBlockByNumber","params":["espresso",true]}]`
		result, err := interceptor.Intercept([]byte(input))
		require.NoError(t, err)
		expected := `[{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","0x64"]},{"jsonrpc":"2.0","id":2,"method":"eth_getBlockByNumber","params":["0x64",true]}]`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("passes through batch request without espresso tags unchanged", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(store, "espresso")

		input := `[{"jsonrpc":"2.0","id":1,"method":"eth_chainId"},{"jsonrpc":"2.0","id":2,"method":"eth_getBalance","params":["0xabc","latest"]}]`
		result, err := interceptor.Intercept([]byte(input))
		require.NoError(t, err)
		require.JSONEq(t, input, string(result))
	})

}

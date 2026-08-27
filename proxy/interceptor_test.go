package proxy

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/adapters"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"
	espressostore "github.com/EspressoSystems/espresso-rollup-node-proxy/store"

	"github.com/stretchr/testify/require"
)

// newEmptyTestStore returns a store that holds no Espresso state yet.
func newEmptyTestStore(t *testing.T) *espressostore.EspressoStore {
	t.Helper()
	fp := filepath.Join(t.TempDir(), "state.json")
	store, err := espressostore.NewEspressoStore(fp, 1)
	require.NoError(t, err)
	return store
}

// newTestStore returns a store whose Espresso-finalized L2 block is
// l2BlockNumber.
func newTestStore(t *testing.T, l2BlockNumber uint64) *espressostore.EspressoStore {
	t.Helper()
	store := newEmptyTestStore(t)
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

	f.Add(`["latest", "finalized"]`)
	f.Add(`{"fromBlock": "safe", "toBlock": "latest"}`)

	// Any tag can be configured, so fuzz with a mix of the default tag and
	// standard block tags.
	i := &interceptor{espressoTags: []string{"espresso", "latest", "safe", "finalized"}}

	f.Fuzz(func(t *testing.T, raw string) {
		var params any
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return // skip structurally invalid JSON
		}

		result, changed, err := i.replaceTagInParams(params, "0x2a", 0)
		if err != nil {
			return // error paths are expected and acceptable
		}

		// Verify the result is still marshallable — an un-marshallable result
		// would indicate the function introduced an invalid value.
		if _, err := json.Marshal(result); err != nil {
			t.Fatalf("replaceTagInParams returned un-marshallable result: %v", err)
		}

		// Invariant: after a successful pass no string value equal to a
		// configured tag may remain anywhere in the params.
		if containsTagValue(result, i.espressoTags) {
			t.Fatalf("configured tag survived interception: input %s, result %#v", raw, result)
		}

		// Invariant: changed is reported iff the input contained a tag.
		if had := containsTagValue(params, i.espressoTags); had != changed {
			t.Fatalf("changed=%v but input contained tag=%v: %s", changed, had, raw)
		}
	})
}

// containsTagValue reports whether any string *value* in v (at any depth)
// equals one of tags. Object keys are ignored, mirroring the interceptor.
func containsTagValue(v any, tags []string) bool {
	switch cast := v.(type) {
	case string:
		return slices.Contains(tags, cast)
	case map[string]any:
		for _, val := range cast {
			if containsTagValue(val, tags) {
				return true
			}
		}
	case []any:
		for _, val := range cast {
			if containsTagValue(val, tags) {
				return true
			}
		}
	}
	return false
}

// standardBlockTags are the block tags defined by the Ethereum JSON-RPC
// spec plus the proxy's default tag. Any of them — and any other string —
// can be configured as an intercepted tag.
var standardBlockTags = []string{"earliest", "latest", "pending", "safe", "finalized", "espresso"}

// TestInterceptorAnyTag verifies that interception is not restricted to a
// fixed set of block tags: every standard tag, as well as an arbitrary custom
// string, can be configured and is rewritten by exactly the same rules.
func TestInterceptorAnyTag(t *testing.T) {
	const blockNumber uint64 = 100
	const want = "0x64"

	getBlock := func(id int, tag string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"eth_getBlockByNumber","params":[%q,false]}`, id, tag)
	}

	allTags := slices.Concat(standardBlockTags, []string{"my-custom-tag"})

	for _, tag := range allTags {
		t.Run("rewrites only "+tag+" when configured alone", func(t *testing.T) {
			store := newTestStore(t, blockNumber)
			interceptor := NewInterceptor(nil, store, []string{tag}, DefaultMaxBatchSize)

			for _, sent := range allTags {
				expected := sent
				if sent == tag {
					expected = want
				}
				result, err := adapters.PerformRequestIntercept([]byte(getBlock(1, sent)), interceptor)
				require.NoError(t, err)
				require.JSONEq(t, getBlock(1, expected), string(result), "sent %q with only %q configured", sent, tag)
			}
		})
	}

	t.Run("intercepts every standard tag at once in a batch", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, standardBlockTags, DefaultMaxBatchSize)

		var reqs, expected []string
		for i, tag := range standardBlockTags {
			reqs = append(reqs, getBlock(i, tag))
			expected = append(expected, getBlock(i, want))
		}
		// A hex block number is not a tag and must survive untouched.
		reqs = append(reqs, getBlock(99, "0x1"))
		expected = append(expected, getBlock(99, "0x1"))

		result, err := adapters.PerformRequestIntercept([]byte("["+strings.Join(reqs, ",")+"]"), interceptor)
		require.NoError(t, err)
		require.JSONEq(t, "["+strings.Join(expected, ",")+"]", string(result))
	})

	t.Run("matching is exact and case-sensitive", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"latest"}, DefaultMaxBatchSize)

		for _, notATag := range []string{"Latest", "LATEST", " latest", "latest ", "latest-ish", "xlatest", "late", ""} {
			result, err := adapters.PerformRequestIntercept([]byte(getBlock(1, notATag)), interceptor)
			require.NoError(t, err)
			require.JSONEq(t, getBlock(1, notATag), string(result), "%q must not match configured tag \"latest\"", notATag)
		}
	})

	t.Run("rewrites a configured tag in any param position regardless of method", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"safe"}, DefaultMaxBatchSize)

		// The interceptor is not method-aware: a configured tag is rewritten
		// wherever it appears as a string value, including positions that are
		// not block parameters (here, a log topic).
		input := `{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"fromBlock":"safe","toBlock":"safe","topics":["safe",null,"latest"]}]}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"fromBlock":"0x64","toBlock":"0x64","topics":["0x64",null,"latest"]}]}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("does not rewrite object keys equal to a configured tag", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"finalized"}, DefaultMaxBatchSize)

		input := `{"jsonrpc":"2.0","id":1,"method":"m","params":[{"finalized":"latest"}]}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		require.JSONEq(t, input, string(result))
	})

	t.Run("does not rewrite method, id or extra fields equal to a configured tag", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"latest"}, DefaultMaxBatchSize)

		input := `{"jsonrpc":"2.0","id":"latest","method":"latest","params":["latest"],"extra":"latest"}`
		result, err := adapters.PerformRequestIntercept([]byte(input), interceptor)
		require.NoError(t, err)
		expected := `{"jsonrpc":"2.0","id":"latest","method":"latest","params":["0x64"],"extra":"latest"}`
		require.JSONEq(t, expected, string(result))
	})

	t.Run("duplicate configured tags behave like a single tag", func(t *testing.T) {
		store := newTestStore(t, blockNumber)
		interceptor := NewInterceptor(nil, store, []string{"finalized", "finalized"}, DefaultMaxBatchSize)

		result, err := adapters.PerformRequestIntercept([]byte(getBlock(1, "finalized")), interceptor)
		require.NoError(t, err)
		require.JSONEq(t, getBlock(1, want), string(result))
	})

	t.Run("passes every configured tag through while espresso state is unknown", func(t *testing.T) {
		interceptor := NewInterceptor(nil, newEmptyTestStore(t), standardBlockTags, DefaultMaxBatchSize)

		for _, tag := range standardBlockTags {
			result, err := adapters.PerformRequestIntercept([]byte(getBlock(1, tag)), interceptor)
			require.NoError(t, err)
			require.JSONEq(t, getBlock(1, tag), string(result), "tag %q must be forwarded unchanged without espresso state", tag)
		}
	})
}

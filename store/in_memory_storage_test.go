package store_test

import (
	"testing"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/store"
	"github.com/stretchr/testify/require"
)

func TestInMemoryStorage_Basic(t *testing.T) {
	ctx := t.Context()
	require := require.New(t)
	storage := store.NewInMemoryStorage[int]()

	// Should be not initialized
	require.Equal(store.StoreState[int]{}, storage.Load(ctx))

	// Store some value
	storage.Store(ctx, 42)

	require.Equal(store.StoreState[int]{Status: store.Valid, State: 42}, storage.Load(ctx))
}

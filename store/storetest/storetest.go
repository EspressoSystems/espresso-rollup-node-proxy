// Package storetest provides EspressoStore constructors for tests. It is
// imported by the test files of several packages, which would otherwise each
// carry their own copy.
package storetest

import (
	"path/filepath"
	"testing"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/store"

	"github.com/stretchr/testify/require"
)

// NewEmpty returns a store backed by a fresh temporary file that holds no
// Espresso state yet.
func NewEmpty(t *testing.T) *store.EspressoStore {
	t.Helper()
	fp := filepath.Join(t.TempDir(), "state.json")
	espressoStore, err := store.NewEspressoStore(fp, 1)
	require.NoError(t, err)
	return espressoStore
}

// NewAtBlock returns a store whose Espresso-finalized L2 block is
// l2BlockNumber.
func NewAtBlock(t *testing.T, l2BlockNumber uint64) *store.EspressoStore {
	t.Helper()
	espressoStore := NewEmpty(t)
	updated, err := espressoStore.UpdateIfGreater(l2BlockNumber, 1)
	require.NoError(t, err)
	require.True(t, updated)
	return espressoStore
}

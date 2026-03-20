package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func tempFilePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state.json")
}

func TestEspressoStore(t *testing.T) {
	t.Run("Test initialization with non-existent file", func(t *testing.T) {
		fp := tempFilePath(t)
		store, err := NewEspressoStore(fp, 1, 1)
		require.NoError(t, err)
		require.NotNil(t, store)
		require.Equal(t, uint64(1), store.state.L2BlockNumber)
		require.Equal(t, uint64(1), store.state.FallbackHotshotHeight)
		require.False(t, store.state.UpdatedAt.IsZero())

		_, err = os.Stat(fp)
		require.NoError(t, err)

		// Now create another store and see that the file is loaded correctly
		store2, err := NewEspressoStore(fp, 0, 0)
		require.NoError(t, err)
		require.NotNil(t, store2)
		require.Equal(t, uint64(1), store2.state.L2BlockNumber)
		require.Equal(t, uint64(1), store2.state.FallbackHotshotHeight)
		require.False(t, store2.state.UpdatedAt.IsZero())
	})

	t.Run("Test fails on corrupted file", func(t *testing.T) {
		fp := tempFilePath(t)
		require.NoError(t, os.WriteFile(fp, []byte("not json"), 0644))
		_, err := NewEspressoStore(fp, 10, 20)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to load state from disk")
	})

	t.Run("Test GetState", func(t *testing.T) {
		fp := tempFilePath(t)
		store, err := NewEspressoStore(fp, 1, 1)
		require.NoError(t, err)
		state, err := store.GetState()
		require.NoError(t, err)
		require.Equal(t, uint64(1), state.L2BlockNumber)
		require.Equal(t, uint64(1), state.FallbackHotshotHeight)
		require.False(t, state.UpdatedAt.IsZero())
	})

	t.Run("Test Update", func(t *testing.T) {
		fp := tempFilePath(t)
		store, err := NewEspressoStore(fp, 1, 1)
		require.NoError(t, err)

		err = store.Update(10, 20)
		require.NoError(t, err)

		state, err := store.GetState()
		require.NoError(t, err)
		require.Equal(t, uint64(10), state.L2BlockNumber)
		require.Equal(t, uint64(20), state.FallbackHotshotHeight)
		require.False(t, state.UpdatedAt.IsZero())
	})

}

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/google/renameio"
)

// EspressoState represents the current metadata associated with the proxy
// including the L2BlockNumber which is block number finalized by Espresso,
// FallbackHotshotHeight which is the minimum hotshot height which the proxy should
// start syncing from in case of a shutdown and
// UpdatedAt which is the timestamp of the last update to the state
type EspressoState struct {
	L2BlockNumber         uint64    `json:"l2_block_number"`
	FallbackHotshotHeight uint64    `json:"fallback_hotshot_height"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// EspressoStore is responsible for managing the state of the proxy
// and persisting it to disk.
type EspressoStore struct {
	mu       sync.RWMutex
	filePath string
	state    EspressoState
}

func NewEspressoStore(filePath string, hotshotHeight uint64) (*EspressoStore, error) {
	store := &EspressoStore{filePath: filePath}

	// Check if the file exists, if so load the state from the disk
	if _, err := os.Stat(filePath); err == nil {
		if err := store.loadFromDisk(); err != nil {
			return nil, fmt.Errorf("failed to load state from disk: %w", err)
		}
		return store, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// If the file doesnt exist, initialize the state
	// with the provided hotshot height
	store.state = EspressoState{
		FallbackHotshotHeight: hotshotHeight,
		UpdatedAt:             time.Now(),
	}
	if err := store.writeToDisk(); err != nil {
		return nil, fmt.Errorf("failed to write initial state to disk: %w", err)
	}
	return store, nil
}

// GetBlockNumber returns the current L2 block number stored in the state
func (es *EspressoStore) GetState() EspressoState {
	es.mu.RLock()
	defer es.mu.RUnlock()
	return es.state
}

// Update updates the L2 block number and fallback hotshot height in the state
// and persists the updated state to disk.
// It also updates the UpdatedAt timestamp to the current time.
func (es *EspressoStore) Update(l2BlockNumber uint64, fallbackHotshotHeight uint64) error {
	es.mu.Lock()
	defer es.mu.Unlock()
	originalState := es.state
	es.state.L2BlockNumber = l2BlockNumber
	es.state.FallbackHotshotHeight = fallbackHotshotHeight
	es.state.UpdatedAt = time.Now()

	if err := es.writeToDisk(); err != nil {
		// If writing to disk fails, we revert the in-memory state to the original state
		es.state = originalState
		return fmt.Errorf("failed to write updated state to disk: %w", err)
	}
	return nil
}

func (es *EspressoStore) UpdateIfGreater(l2BlockNumber uint64, fallbackHotshotHeight uint64) (bool, error) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.state.L2BlockNumber >= l2BlockNumber {
		return false, nil
	}
	originalState := es.state
	es.state.L2BlockNumber = l2BlockNumber
	es.state.FallbackHotshotHeight = fallbackHotshotHeight
	es.state.UpdatedAt = time.Now()

	if err := es.writeToDisk(); err != nil {
		es.state = originalState
		return false, fmt.Errorf("failed to write updated state to disk: %w", err)
	}
	return true, nil
}

func (es *EspressoStore) loadFromDisk() error {
	data, err := os.ReadFile(es.filePath)
	if err != nil {
		return fmt.Errorf("failed to read block state file: %w", err)
	}

	var state EspressoState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.FallbackHotshotHeight == 0 || state.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid state file: missing required fields")
	}
	es.state = state
	return nil
}

// writeToDisk grabs a snapshot of the current state and writes it to the disk
// aotmically, by first writing to a temporary file, then renaming the file.
//
// NOTE: This is only guaranteed to be atomic if the file system rename
// operation is guaranteed to be atomic.
// Should be the case on linux utilizing ext4/XFS.
func (es *EspressoStore) writeToDisk() error {
	// Grab the local state (locked)
	state := es.GetState()

	pendingFile, err := renameio.TempFile("", es.filePath)
	if err != nil {
		return fmt.Errorf("failed to open file to write to: %w", err)
	}

	// Create a JSON encoder, wrapping the io.Writer
	encoder := json.NewEncoder(pendingFile)
	if encodeErr := encoder.Encode(state); encodeErr != nil {
		// Attempt to ensure that we close the file
		if err := pendingFile.Close(); err != nil {
			return fmt.Errorf("failed to write to file: %w, failed to close file: %w", err, err)
		}
		return fmt.Errorf("failed to write to file: %w", encodeErr)
	}

	if err := pendingFile.CloseAtomicallyReplace(); err != nil {
		return fmt.Errorf("failed to atomically replace file: %w", err)
	}

	return nil
}

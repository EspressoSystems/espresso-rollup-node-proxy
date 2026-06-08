package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
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
	if err := store.writeToDisk(store.state); err != nil {
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

func (es *EspressoStore) UpdateIfGreater(l2BlockNumber uint64, fallbackHotshotHeight uint64) (bool, error) {
	state := es.GetState()
	if state.L2BlockNumber >= l2BlockNumber {
		log.Warn("L2 block number should only ever increase", "current", state.L2BlockNumber, "new", l2BlockNumber)
		return false, nil
	}

	newState := EspressoState{
		L2BlockNumber:         l2BlockNumber,
		FallbackHotshotHeight: fallbackHotshotHeight,
		UpdatedAt:             time.Now(),
	}

	if err := es.writeToDisk(newState); err != nil {
		// dont update state if we fail to write to disk
		return false, fmt.Errorf("failed to write updated state to disk: %w", err)
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	es.state = newState
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
func (es *EspressoStore) writeToDisk(newState EspressoState) error {

	pendingFile, err := renameio.TempFile("", es.filePath)
	if err != nil {
		return fmt.Errorf("failed to open file to write to: %w", err)
	}

	// Create a JSON encoder, wrapping the io.Writer
	encoder := json.NewEncoder(pendingFile)
	if encodeErr := encoder.Encode(newState); encodeErr != nil {
		// Attempt to ensure that we close the file
		if err := pendingFile.Cleanup(); err != nil {
			return fmt.Errorf("failed to write to file: %w, failed to close file: %w", encodeErr, err)
		}
		return fmt.Errorf("failed to write to file: %w", encodeErr)
	}

	if err := pendingFile.CloseAtomicallyReplace(); err != nil {
		return fmt.Errorf("failed to atomically replace file: %w", err)
	}

	return nil
}

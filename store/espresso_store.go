package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"
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

func NewEspressoStore(filePath string, hotshotHeight uint64, finalizedL2BlockNumber uint64) (*EspressoStore, error) {
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
	// with the provided hotshot height and finalized L2 block number
	store.state = EspressoState{
		L2BlockNumber:         finalizedL2BlockNumber,
		FallbackHotshotHeight: hotshotHeight,
		UpdatedAt:             time.Now(),
	}
	if err := store.writeToDisk(); err != nil {
		return nil, fmt.Errorf("failed to write initial state to disk: %w", err)
	}
	return store, nil
}

// GetBlockNumber returns the current L2 block number stored in the state
func (es *EspressoStore) GetBlockNumber() uint64 {
	es.mu.RLock()
	defer es.mu.RUnlock()
	return es.state.L2BlockNumber
}

// GetState returns the full current state
func (es *EspressoStore) GetState() (EspressoState, error) {
	es.mu.RLock()
	defer es.mu.RUnlock()
	return es.state, nil
}

// Update updates the L2 block number and fallback hotshot height in the state
// and persists the updated state to disk.
// It also updates the UpdatedAt timestamp to the current time.
func (es *EspressoStore) Update(l2BlockNumber uint64, fallbackHotshotHeight uint64) error {
	es.mu.Lock()
	defer es.mu.Unlock()

	es.state.L2BlockNumber = l2BlockNumber
	es.state.FallbackHotshotHeight = fallbackHotshotHeight
	es.state.UpdatedAt = time.Now()

	return es.writeToDisk()
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
	if state.FallbackHotshotHeight == 0 || state.L2BlockNumber == 0 || state.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid state file: missing required fields")
	}
	es.state = state
	return nil
}

func (es *EspressoStore) writeToDisk() error {
	data, err := json.Marshal(es.state)
	if err != nil {
		return fmt.Errorf("failed to marshal block state: %w", err)
	}
	tmp := es.filePath + ".tmp"

	// Since WriteFile requires multiple system calls to complete,
	// a failure mid-operation can leave the file in a partially written state.
	// as a reason we write a tmp file first and then rename it to the actual file which is an atomic operation in most operating systems.
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if renameErr := os.Rename(tmp, es.filePath); renameErr != nil {
		if removeErr := os.Remove(tmp); removeErr != nil {
			return fmt.Errorf("failed to rename temp file: %w (and failed to remove temp file: %v)", renameErr, removeErr)
		}
		return fmt.Errorf("failed to rename temp file: %w", renameErr)
	}
	return nil
}

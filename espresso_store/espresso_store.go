package espressostore

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type EspressoState struct {
	BlockNumber   uint64    `json:"blockNumber"`
	HotshotHeight uint64    `json:"hotshotHeight"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type EspressoStore struct {
	mu       sync.RWMutex
	filePath string
	state    *EspressoState
}

func NewEspressoStore(filePath string) (*EspressoStore, error) {
	es := &EspressoStore{
		filePath: filePath,
	}

	if _, err := os.Stat(filePath); err == nil {
		if err := es.loadFromDisk(); err != nil {
			return nil, fmt.Errorf("failed to load block state from disk: %w", err)
		}
	}
	return es, nil
}

func (es *EspressoStore) Update(blockNumber, hotshotHeight uint64) error {
	es.mu.Lock()
	defer es.mu.Unlock()

	es.state = &EspressoState{
		BlockNumber:   blockNumber,
		HotshotHeight: hotshotHeight,
		UpdatedAt:     time.Now(),
	}

	return es.writeToDisk()
}

func (es *EspressoStore) loadFromDisk() error {
	data, err := os.ReadFile(es.filePath)
	if err != nil {
		return fmt.Errorf("failed to read block state file: %w", err)
	}
	return json.Unmarshal(data, &es.state)
}

func (es *EspressoStore) GetBlockNumber() uint64 {
	es.mu.RLock()
	defer es.mu.RUnlock()
	if es.state == nil {
		return 0
	}
	return es.state.BlockNumber
}

// TODO: think about the edge condition when for some reason
// the espresso tag doesnt get written it may cause the tag to go backwards,
// is that acceptable?
func (es *EspressoStore) writeToDisk() error {
	data, err := json.Marshal(es.state)
	if err != nil {
		return fmt.Errorf("failed to marshal block state: %w", err)
	}
	// TODO: check these file permissions
	return os.WriteFile(es.filePath, data, 0644)
}

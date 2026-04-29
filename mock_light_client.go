package main

import (
	"context"
	"math/big"

	opStreamer "github.com/EspressoSystems/espresso-streamers/op"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
)

const finalizedBlocks = 500

// mockLightClient implements opStreamer.LightClientCallerInterface for dev/test
// environments where the real light client contract is not available.
// It tracks the live Espresso head and returns a height lagged by finalizedBlocks.
type mockLightClient struct {
	client opStreamer.EspressoClient
	last   uint64
}

var _ opStreamer.LightClientCallerInterface = (*mockLightClient)(nil)

func newMockLightClient(client opStreamer.EspressoClient) *mockLightClient {
	return &mockLightClient{client: client}
}

func (m *mockLightClient) FinalizedState(_ *bind.CallOpts) (opStreamer.FinalizedState, error) {
	current, err := m.client.FetchLatestBlockHeight(context.Background())
	result := m.last
	if err == nil {
		m.last = 0
		if current > finalizedBlocks {
			m.last = current - finalizedBlocks
		}
	}
	return opStreamer.FinalizedState{
		BlockHeight:   result,
		ViewNum:       0,
		BlockCommRoot: big.NewInt(0),
	}, nil
}

package verifier

import (
	"context"
	"math/big"

	op "github.com/EspressoSystems/espresso-streamers/op"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// AdaptL1BlockRefClient is a wrapper around ethclient.Client that implements the op.L1Client interface
type AdaptL1BlockRefClient struct {
	L1Client *ethclient.Client
}

var _ op.L1Client = new(AdaptL1BlockRefClient)

// NewAdaptL1BlockRefClient creates a new L1BlockRefClient
func NewAdaptL1BlockRefClient(L1Client *ethclient.Client) *AdaptL1BlockRefClient {
	return &AdaptL1BlockRefClient{
		L1Client: L1Client,
	}
}

// HeaderHashByNumber implements the op.L1Client interface
func (c *AdaptL1BlockRefClient) HeaderHashByNumber(ctx context.Context, number *big.Int) (common.Hash, error) {
	expectedL1BlockRef, err := c.L1Client.HeaderByNumber(ctx, number)
	if err != nil {
		return common.Hash{}, err
	}

	return expectedL1BlockRef.Hash(), nil
}

// CodeAt implements bind.ContractCaller
func (c *AdaptL1BlockRefClient) CodeAt(ctx context.Context, contract common.Address, blockNumber *big.Int) ([]byte, error) {
	return c.L1Client.CodeAt(ctx, contract, blockNumber)
}

// CallContract implements bind.ContractCaller
func (c *AdaptL1BlockRefClient) CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	return c.L1Client.CallContract(ctx, call, blockNumber)
}

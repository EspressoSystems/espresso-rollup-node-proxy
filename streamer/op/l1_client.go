package op

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type adaptL1BlockRefClient struct {
	client *ethclient.Client
}

func NewAdaptL1BlockRefClient(client *ethclient.Client) L1Client {
	return &adaptL1BlockRefClient{client: client}
}

func (c *adaptL1BlockRefClient) HeaderHashByNumber(ctx context.Context, number *big.Int) (common.Hash, error) {
	header, err := c.client.HeaderByNumber(ctx, number)
	if err != nil {
		return common.Hash{}, err
	}
	return header.Hash(), nil
}

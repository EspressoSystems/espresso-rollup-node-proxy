package streamer

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

const (
	decafURL   = "https://query.decaf.testnet.espresso.network"
	startBlock = uint64(7_298_000)
	endBlock   = uint64(7_298_001)
)

type ethL1Client struct {
	client *ethclient.Client
}

func (c *ethL1Client) HeaderHashByNumber(ctx context.Context, number *big.Int) (common.Hash, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	header, err := c.client.HeaderByNumber(ctx, number)
	if err != nil {
		return common.Hash{}, err
	}
	return header.Hash(), nil
}

// stubIntegLightClient implements LightClientCallerInterface.
type stubLightClient struct{}

func (s *stubLightClient) FinalizedState(_ *bind.CallOpts) (FinalizedState, error) {
	return FinalizedState{BlockHeight: endBlock, BlockCommRoot: big.NewInt(0)}, nil
}

func TestOpStreamerIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelDebug, true)))

	const namespace = uint64(22262320)
	batchPosterAddr := common.HexToAddress("0x9f64044c334893d890c06E9cA5Fa48B957665E96")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := espressoClient.NewClient(decafURL)

	ethClient, err := ethclient.DialContext(ctx, "https://theserversroom.com/sepolia/54cmzzhcj1o/")
	require.NoError(t, err)
	defer ethClient.Close()
	l1 := &ethL1Client{client: ethClient}

	streamer := NewEspressoStreamer(
		namespace,
		l1,
		l1,
		client,
		&stubLightClient{},
		log.Root(),
		CreateEspressoBatchUnmarshaler(batchPosterAddr),
		time.Second,
		startBlock-1,
		4_578_818,
	)

	streamer.FinalizedL1 = eth.L1BlockRef{Number: 999_999_999}

	log.Info("streaming espresso blocks", "start", startBlock, "end", endBlock, "namespace", namespace, "url", decafURL)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel2()
	for streamer.hotShotPos < endBlock {
		err := streamer.Update(ctx2)
		require.NoError(t, err)
		for streamer.HasNext(ctx2) {
			result := streamer.Next(ctx2)
			if result != nil {
				b := *result
				log.Info("received next batch from streamer", "batch number", b.Number(), "hot shot block", streamer.hotShotPos)
			}
		}
	}

	log.Info("streamer", "batch pos", streamer.BatchPos)
	require.Equal(t, uint64(4_580_869), streamer.BatchPos)
}

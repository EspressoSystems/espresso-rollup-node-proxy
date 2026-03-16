package nitro

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	espressoClient "github.com/EspressoSystems/espresso-network/sdks/go/client"
	espressoTypes "github.com/EspressoSystems/espresso-network/sdks/go/types"
	espressoCommon "github.com/EspressoSystems/espresso-network/sdks/go/types/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

func TestEspressoStreamer(t *testing.T) {
	t.Run("Peek should not change the current position", func(t *testing.T) {
		ctx := context.Background()
		mockEspressoClient := new(mockEspressoClient)

		streamer := NewEspressoStreamer(1, 3, mockEspressoClient, nil, 1*time.Second)

		streamer.Reset(1, 3)

		before := streamer.currentMessagePos
		r := streamer.Peek(ctx)
		assert.Nil(t, r)
		assert.Equal(t, before, streamer.currentMessagePos)

		streamer.messageWithMetadataAndPos = []*MessageWithMetadataAndPos{
			{
				MessageWithMeta: MessageWithMetadata{},
				Pos:             1,
				HotshotHeight:   3,
			},
			{
				MessageWithMeta: MessageWithMetadata{},
				Pos:             2,
				HotshotHeight:   4,
			},
		}

		r = streamer.Peek(ctx)
		assert.Equal(t, streamer.messageWithMetadataAndPos[0], r)
		assert.Equal(t, before, streamer.currentMessagePos)
		assert.Equal(t, len(streamer.messageWithMetadataAndPos), 2)
	})
	t.Run("Next should consume a message if it is in buffer", func(t *testing.T) {
		ctx := context.Background()
		mockEspressoClient := new(mockEspressoClient)

		streamer := NewEspressoStreamer(1, 3, mockEspressoClient, nil, 1*time.Second)

		streamer.Reset(1, 3)

		// Empty buffer. Should not change anything
		initialPos := streamer.currentMessagePos
		r := streamer.Next(ctx)
		assert.Nil(t, r)
		assert.Equal(t, initialPos, streamer.currentMessagePos)

		streamer.messageWithMetadataAndPos = []*MessageWithMetadataAndPos{
			{
				MessageWithMeta: MessageWithMetadata{},
				Pos:             1,
				HotshotHeight:   3,
			},
			{
				MessageWithMeta: MessageWithMetadata{},
				Pos:             2,
				HotshotHeight:   4,
			},
		}

		r = streamer.Next(ctx)
		assert.Equal(t, streamer.messageWithMetadataAndPos[0], r)
		assert.Equal(t, initialPos+1, streamer.currentMessagePos)
		// Buffer should still have 2 messages.
		assert.Equal(t, len(streamer.messageWithMetadataAndPos), 2)

		// Second message
		// Peek would cleanup the outdated messages as well
		peekMessage := streamer.Peek(ctx)
		assert.NotNil(t, peekMessage)
		assert.Equal(t, initialPos+1, streamer.currentMessagePos)
		assert.Equal(t, len(streamer.messageWithMetadataAndPos), 1)

		newMessage := streamer.Next(ctx)
		assert.Equal(t, peekMessage, newMessage)
		assert.Equal(t, initialPos+2, streamer.currentMessagePos)

		// Empty message should not alter the current position
		third := streamer.Next(ctx)
		assert.Nil(t, third)
		assert.Equal(t, initialPos+2, streamer.currentMessagePos)
	})
	t.Run("Streamer should not skip any hotshot blocks", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mockEspressoClient := new(mockEspressoClient)

		namespace := uint64(1)

		mockEspressoClient.On("FetchLatestBlockHeight", ctx).Return(uint64(4), nil).Once()
		mockEspressoClient.On("FetchNamespaceTransactionsInRange", ctx, uint64(3), uint64(4), namespace).Return([]espressoTypes.NamespaceTransactionsRangeData{}, nil).Once()
		mockEspressoClient.On("FetchLatestBlockHeight", ctx).Return(uint64(5), nil).Once()
		mockEspressoClient.On("FetchNamespaceTransactionsInRange", ctx, uint64(4), uint64(5), namespace).Return([]espressoTypes.NamespaceTransactionsRangeData{}, nil).Once()
		mockEspressoClient.On("FetchLatestBlockHeight", ctx).Return(uint64(6), nil).Once()
		mockEspressoClient.On("FetchNamespaceTransactionsInRange", ctx, uint64(5), uint64(6), namespace).Return([]espressoTypes.NamespaceTransactionsRangeData{}, nil).Once()
		mockEspressoClient.On("FetchLatestBlockHeight", ctx).Return(uint64(7), nil).Once()
		mockEspressoClient.On("FetchNamespaceTransactionsInRange", ctx, uint64(6), uint64(7), namespace).Return([]espressoTypes.NamespaceTransactionsRangeData{}, errors.New("test error")).Once()

		streamer := NewEspressoStreamer(namespace, 3, mockEspressoClient, nil, 1*time.Second)

		testParseFn := func(tx espressoTypes.Bytes) ([]*MessageWithMetadataAndPos, error) {
			return nil, nil
		}

		err := streamer.QueueMessagesFromHotshot(ctx, testParseFn)
		require.NoError(t, err)
		require.Equal(t, streamer.nextHotshotBlockNum, uint64(4))

		err = streamer.QueueMessagesFromHotshot(ctx, testParseFn)
		require.NoError(t, err)
		require.Equal(t, streamer.nextHotshotBlockNum, uint64(5))

		err = streamer.QueueMessagesFromHotshot(ctx, testParseFn)
		require.NoError(t, err)
		require.Equal(t, streamer.nextHotshotBlockNum, uint64(6))

		err = streamer.QueueMessagesFromHotshot(ctx, testParseFn)
		require.Error(t, err)
		require.Equal(t, streamer.nextHotshotBlockNum, uint64(6))

	})
	t.Run("Streamer should query hotshot after being reset", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mockEspressoClient := new(mockEspressoClient)

		namespace := uint64(1)
		mockEspressoClient.On("FetchLatestBlockHeight", ctx).Return(uint64(4), nil).Once()
		mockEspressoClient.On("FetchNamespaceTransactionsInRange", ctx, uint64(3), uint64(4), namespace).Return([]espressoTypes.NamespaceTransactionsRangeData{
			{
				Transactions: []espressoTypes.Transaction{
					{
						Namespace: 1,
						Payload:   espressoTypes.Bytes{0x05, 0x06, 0x07, 0x08},
					},
				},
			},
		}, nil).Once()

		mockEspressoClient.On("FetchLatestBlockHeight", ctx).Return(uint64(5), nil).Once()
		mockEspressoClient.On("FetchNamespaceTransactionsInRange", ctx, uint64(4), uint64(5), namespace).Return([]espressoTypes.NamespaceTransactionsRangeData{
			{
				Transactions: []espressoTypes.Transaction{
					{
						Namespace: 1,
						Payload:   espressoTypes.Bytes{0x05, 0x06, 0x07, 0x08},
					},
				},
			},
		}, nil).Once()

		streamer := NewEspressoStreamer(namespace, 3, mockEspressoClient, nil, 1*time.Second)

		testParseFn := func(pos uint64, hotshotheight uint64) func(tx espressoTypes.Bytes) ([]*MessageWithMetadataAndPos, error) {

			return func(tx espressoTypes.Bytes) ([]*MessageWithMetadataAndPos, error) {
				return []*MessageWithMetadataAndPos{
					{
						MessageWithMeta: MessageWithMetadata{
							Message: &L1IncomingMessage{},
						},
						Pos:           pos,
						HotshotHeight: hotshotheight,
					},
				}, nil
			}
		}

		err := streamer.QueueMessagesFromHotshot(ctx, testParseFn(3, 3))
		require.NoError(t, err)

		err = streamer.QueueMessagesFromHotshot(ctx, testParseFn(4, 4))
		require.NoError(t, err)

		require.Equal(t, 2, len(streamer.messageWithMetadataAndPos))

		streamer.Reset(0, 3)

		require.Equal(t, 0, len(streamer.messageWithMetadataAndPos))

		// Add new mocks for the next fetch after reset
		mockEspressoClient.On("FetchLatestBlockHeight", ctx).Return(uint64(4), nil).Once()
		mockEspressoClient.On("FetchNamespaceTransactionsInRange", ctx, uint64(3), uint64(4), namespace).Return([]espressoTypes.NamespaceTransactionsRangeData{
			{
				Transactions: []espressoTypes.Transaction{
					{
						Namespace: 1,
						Payload:   espressoTypes.Bytes{0x05, 0x06, 0x07, 0x08},
					},
				},
			},
		}, nil).Once()

		err = streamer.QueueMessagesFromHotshot(ctx, testParseFn(3, 3))
		require.NoError(t, err)

		require.Equal(t, len(streamer.messageWithMetadataAndPos), 1)
	})

	t.Run("rpc error should retry", func(t *testing.T) {
		ctx := context.Background()
		mockEspressoClient := new(mockEspressoClient)
		namespace := uint64(1)
		blockNum := uint64(3)

		tx1, tx2, tx3 := espressoTypes.Bytes{0x01}, espressoTypes.Bytes{0x02}, espressoTypes.Bytes{0x03}
		mockEspressoClient.On("FetchLatestBlockHeight", ctx).Return(blockNum+1, nil).Once()
		mockEspressoClient.On("FetchNamespaceTransactionsInRange", ctx, blockNum, blockNum+1, namespace).Return([]espressoTypes.NamespaceTransactionsRangeData{
			{
				Transactions: []espressoTypes.Transaction{
					{
						Namespace: namespace,
						Payload:   tx1,
					},
					{
						Namespace: namespace,
						Payload:   tx2,
					},
					{
						Namespace: namespace,
						Payload:   tx3,
					},
				},
			},
		}, nil).Once()

		parseAttemptCount := 0
		parseFn := func(tx espressoTypes.Bytes) ([]*MessageWithMetadataAndPos, error) {
			if assert.ObjectsAreEqual(tx, tx2) {
				parseAttemptCount++
				return nil, rpc.ErrNoResult
			}
			return []*MessageWithMetadataAndPos{{
				MessageWithMeta: MessageWithMetadata{},
				Pos:             uint64(tx[0]),
				HotshotHeight:   blockNum,
			}}, nil
		}

		messages, _, err := fetchNextHotshotBlock(ctx, mockEspressoClient, blockNum, parseFn, namespace)
		require.NoError(t, err)

		require.Equal(t, 2, len(messages), "Expected to process two messages")
		if len(messages) == 2 && len(tx1) > 0 && len(tx3) > 0 {
			assert.Equal(t, uint64(tx1[0]), messages[0].Pos)
			assert.Equal(t, uint64(tx3[0]), messages[1].Pos)
		}

		require.Equal(t, 1, parseAttemptCount, "Expected the failing transaction to be attempted only once")

		mockEspressoClient.AssertExpectations(t)
	})

	t.Run("empty transaction should return ErrPayloadHadNoMessages", func(t *testing.T) {
		mockEspressoClient := new(mockEspressoClient)
		streamer := NewEspressoStreamer(1, 1, mockEspressoClient, nil, time.Millisecond)

		msgFetcher := func(MessageIndex) ([]byte, error) {
			return []byte{}, nil
		}
		test := []MessageIndex{1, 2}
		payload, _ := BuildRawHotShotPayload(test, msgFetcher, 100000)

		signerFunc := func([]byte) ([]byte, error) {
			return []byte{1}, nil
		}
		signedPayload, _ := SignHotShotPayload(payload, signerFunc)
		_, err := streamer.parseEspressoTransaction(signedPayload)
		require.ErrorIs(t, err, ErrPayloadHadNoMessages)
	})

	t.Run("valid signer address should pass batch poster verification", func(t *testing.T) {
		mockEspressoClient := new(mockEspressoClient)

		privateKey, err := crypto.GenerateKey()
		require.NoError(t, err)

		hash := crypto.Keccak256Hash([]byte("test-user-data"))
		hashArr := [32]byte(hash)
		signature, err := crypto.Sign(hash.Bytes(), privateKey)
		require.NoError(t, err)

		validAddr := crypto.PubkeyToAddress(privateKey.PublicKey)

		streamerWithValidSigner := NewEspressoStreamer(1, 1, mockEspressoClient, []common.Address{validAddr}, time.Millisecond)
		err = streamerWithValidSigner.verifyBatchPosterSignature(signature, hashArr)
		require.NoError(t, err)

		streamerWithNoValidSigners := NewEspressoStreamer(1, 1, mockEspressoClient, []common.Address{}, time.Millisecond)
		err = streamerWithNoValidSigners.verifyBatchPosterSignature(signature, hashArr)
		require.Error(t, err)
		require.Contains(t, err.Error(), "address not valid")
	})

	t.Run("parseEspressoTransaction with valid signer should return messages", func(t *testing.T) {
		mockEspressoClient := new(mockEspressoClient)

		privateKey, err := crypto.GenerateKey()
		require.NoError(t, err)

		validAddr := crypto.PubkeyToAddress(privateKey.PublicKey)
		streamer := NewEspressoStreamer(1, 5, mockEspressoClient, []common.Address{validAddr}, time.Millisecond)

		msg := MessageWithMetadata{
			Message: &L1IncomingMessage{
				Header: &L1IncomingMessageHeader{
					Kind:        0,
					Poster:      common.Address{},
					BlockNumber: 1,
					Timestamp:   1000,
					RequestId:   nil,
					L1BaseFee:   common.Big0,
				},
				L2msg: []byte{0x01},
			},
		}
		encodedMsg, err := rlp.EncodeToBytes(msg)
		require.NoError(t, err)

		msgFetcher := func(idx MessageIndex) ([]byte, error) {
			return encodedMsg, nil
		}

		payload, _ := BuildRawHotShotPayload([]MessageIndex{5, 6}, msgFetcher, 100000)

		signer := func(data []byte) ([]byte, error) {
			hash := crypto.Keccak256(data)
			return crypto.Sign(hash, privateKey)
		}

		signedPayload, err := SignHotShotPayload(payload, signer)
		require.NoError(t, err)

		result, err := streamer.parseEspressoTransaction(signedPayload)
		require.NoError(t, err)
		require.Equal(t, 2, len(result))
		require.Equal(t, uint64(5), result[0].Pos)
		require.Equal(t, uint64(6), result[1].Pos)
	})

	t.Run("GetCurrentEarliestHotShotBlockNumber returns correct value", func(t *testing.T) {
		mockEspressoClient := new(mockEspressoClient)
		streamer := NewEspressoStreamer(1, 10, mockEspressoClient, nil, time.Millisecond)

		require.Equal(t, uint64(10), streamer.GetCurrentEarliestHotShotBlockNumber())

		streamer.messageWithMetadataAndPos = []*MessageWithMetadataAndPos{
			{HotshotHeight: 7},
			{HotshotHeight: 9},
		}
		require.Equal(t, uint64(7), streamer.GetCurrentEarliestHotShotBlockNumber())
	})

	t.Run("Start and StopAndWait should manage goroutine lifecycle", func(t *testing.T) {
		mockEspressoClient := new(mockEspressoClient)

		privateKey, err := crypto.GenerateKey()
		require.NoError(t, err)
		validAddr := crypto.PubkeyToAddress(privateKey.PublicKey)

		msg := MessageWithMetadata{
			Message: &L1IncomingMessage{
				Header: &L1IncomingMessageHeader{
					Kind:        0,
					Poster:      common.Address{},
					BlockNumber: 1,
					Timestamp:   1000,
					RequestId:   nil,
					L1BaseFee:   common.Big0,
				},
				L2msg: []byte{0x01},
			},
		}
		encodedMsg, err := rlp.EncodeToBytes(msg)
		require.NoError(t, err)

		msgFetcher := func(idx MessageIndex) ([]byte, error) {
			return encodedMsg, nil
		}
		payload, _ := BuildRawHotShotPayload([]MessageIndex{1}, msgFetcher, 100000)

		signer := func(data []byte) ([]byte, error) {
			hash := crypto.Keccak256(data)
			return crypto.Sign(hash, privateKey)
		}
		signedPayload, err := SignHotShotPayload(payload, signer)
		require.NoError(t, err)

		namespace := uint64(1)

		mockEspressoClient.On("FetchLatestBlockHeight", mock.Anything).Return(uint64(2), nil).Once()
		mockEspressoClient.On("FetchNamespaceTransactionsInRange", mock.Anything, uint64(1), uint64(2), namespace).Return([]espressoTypes.NamespaceTransactionsRangeData{
			{
				Transactions: []espressoTypes.Transaction{
					{Namespace: namespace, Payload: signedPayload},
				},
			},
		}, nil).Once()

		mockEspressoClient.On("FetchLatestBlockHeight", mock.Anything).Return(uint64(2), nil)
		mockEspressoClient.On("FetchNamespaceTransactionsInRange", mock.Anything, uint64(2), uint64(2), namespace).Return([]espressoTypes.NamespaceTransactionsRangeData{}, nil).Maybe()

		streamer := NewEspressoStreamer(namespace, 1, mockEspressoClient, []common.Address{validAddr}, 50*time.Millisecond)

		err = streamer.Start(context.Background())
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			return streamer.Peek(context.Background()) != nil
		}, 2*time.Second, 10*time.Millisecond)

		result := streamer.Next(context.Background())
		require.NotNil(t, result)
		require.Equal(t, uint64(1), result.Pos)

		done := make(chan struct{})
		go func() {
			streamer.StopAndWait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("StopAndWait did not return within timeout")
		}
	})
}

// This serves to assert that we should be expecting a specific error during the test, and if the error does not match, fail the test.
func ExpectErr(t *testing.T, err error, expectedError error) {
	t.Helper()
	if !errors.Is(err, expectedError) {
		t.Fatal(err, expectedError)
	}
}

type mockEspressoClient struct {
	mock.Mock
}

// StreamTransactions implements client.EspressoClient.
func (m *mockEspressoClient) StreamTransactions(ctx context.Context, height uint64) (espressoClient.Stream[espressoTypes.TransactionQueryData], error) {
	panic("unimplemented")
}

// StreamTransactionsInNamespace implements client.EspressoClient.
func (m *mockEspressoClient) StreamTransactionsInNamespace(ctx context.Context, height uint64, namespace uint64) (espressoClient.Stream[espressoTypes.TransactionQueryData], error) {
	panic("unimplemented")
}

func (m *mockEspressoClient) FetchLatestBlockHeight(ctx context.Context) (uint64, error) {
	args := m.Called(ctx)
	//nolint:errcheck
	return args.Get(0).(uint64), args.Error(1)
}

func (m *mockEspressoClient) FetchExplorerTransactionByHash(ctx context.Context, hash *espressoTypes.TaggedBase64) (espressoTypes.ExplorerTransactionQueryData, error) {
	args := m.Called(ctx, hash)
	//nolint:errcheck
	return args.Get(0).(espressoTypes.ExplorerTransactionQueryData), args.Error(1)
}

// FetchNamespaceTransactionsInRange implements client.EspressoClient.
func (m *mockEspressoClient) FetchNamespaceTransactionsInRange(ctx context.Context, fromHeight uint64, toHeight uint64, namespace uint64) ([]espressoTypes.NamespaceTransactionsRangeData, error) {
	args := m.Called(ctx, fromHeight, toHeight, namespace)
	//nolint:errcheck
	return args.Get(0).([]espressoTypes.NamespaceTransactionsRangeData), args.Error(1)
}

func (m *mockEspressoClient) FetchTransactionsInBlock(ctx context.Context, blockHeight uint64, namespace uint64) (espressoClient.TransactionsInBlock, error) {
	args := m.Called(ctx, blockHeight, namespace)
	//nolint:errcheck
	return args.Get(0).(espressoClient.TransactionsInBlock), args.Error(1)
}

func (m *mockEspressoClient) FetchHeaderByHeight(ctx context.Context, blockHeight uint64) (espressoTypes.HeaderImpl, error) {
	header := espressoTypes.Header0_3{Height: blockHeight, L1Finalized: &espressoTypes.L1BlockInfo{Number: 1}}
	return espressoTypes.HeaderImpl{Header: &header}, nil
}

func (m *mockEspressoClient) FetchHeadersByRange(ctx context.Context, from uint64, until uint64) ([]espressoTypes.HeaderImpl, error) {
	panic("not implemented")
}

func (m *mockEspressoClient) FetchRawHeaderByHeight(ctx context.Context, height uint64) (json.RawMessage, error) {
	panic("not implemented")
}

func (m *mockEspressoClient) FetchTransactionByHash(ctx context.Context, hash *espressoTypes.TaggedBase64) (espressoTypes.TransactionQueryData, error) {
	panic("not implemented")
}

func (m *mockEspressoClient) FetchVidCommonByHeight(ctx context.Context, blockHeight uint64) (espressoTypes.VidCommon, error) {
	panic("not implemented")
}

func (m *mockEspressoClient) SubmitTransaction(ctx context.Context, tx espressoCommon.Transaction) (*espressoCommon.TaggedBase64, error) {
	panic("not implemented")
}

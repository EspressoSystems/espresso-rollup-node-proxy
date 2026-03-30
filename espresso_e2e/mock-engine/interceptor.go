package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

var (
	zeroHash = json.RawMessage(`"0x0000000000000000000000000000000000000000000000000000000000000000"`)
)

const (
	// JSON RPC methods
	forkUpdated      = "engine_forkchoiceUpdatedV3"
	engineGetPayload = "engine_getPayloadV4"
	engineNewPayload = "engine_newPayloadV4"
)

type jsonRpcRequest struct {
	Jsonrpc string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

type payloadIDResponse struct {
	PayloadID *string `json:"payloadId"`
}

type executionPayload struct {
	ExecutionPayload map[string]json.RawMessage `json:"executionPayload"`
}

type Interceptor struct {
	mu                   sync.RWMutex
	allowMaliciousBlock  bool
	maliciousBlockHashes map[string]string
	jwtSecret            []byte
	upstreamAddress      string
	maliciousBlockNum    uint64
}

func (i *Interceptor) getJwt() (string, error) {
	now := time.Now()

	claims := jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(60 * time.Second).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString(i.jwtSecret)
}

func (i *Interceptor) callUpstream(method string, params any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	req, err := http.NewRequest("POST", i.upstreamAddress, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	token, err := i.getJwt()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

func (i *Interceptor) handleMaliciousBlock(
	blockNum uint64,
	rpcRequest jsonRpcRequest,
	rawPayload map[string]json.RawMessage) ([]byte, error) {

	var blockHash string
	json.Unmarshal(rawPayload["blockHash"], &blockHash)
	log.Printf("intercepted engine_newPayloadV4 for block %d (hash %s), building replacement", blockNum, blockHash)

	// Build a replacement block on the same parent with only the L1 deposit tx
	replacementPayload, err := i.buildMaliciousBlock(rawPayload)
	if err != nil {
		log.Printf("failed to build replacement block: %v, forwarding original", err)
		return nil, err
	}

	var maliciousBlockHash string
	json.Unmarshal(replacementPayload["blockHash"], &maliciousBlockHash)
	log.Printf("replacement block built (hash %s), submitting", maliciousBlockHash)

	// Submit the replacement block to geth
	modifiedParams := []any{replacementPayload}
	for i := 1; i < len(rpcRequest.Params); i++ {
		modifiedParams = append(modifiedParams, rpcRequest.Params[i])
	}
	result, err := i.callUpstream(engineNewPayload, modifiedParams)
	if err != nil {
		log.Printf("failed to submit replacement block: %v, forwarding original", err)
		return nil, err
	}

	// Store hash mapping so subsequent forkchoiceUpdated calls use the replacement
	i.mu.Lock()
	i.maliciousBlockHashes[blockHash] = maliciousBlockHash
	i.mu.Unlock()
	log.Printf("hash mapping registered %s -> %s", blockHash, maliciousBlockHash)

	// Return the submit result to op-node as if the original payload was accepted
	newBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      rpcRequest.ID,
		"result":  result,
	})
	return newBody, nil
}

func (i *Interceptor) buildMaliciousBlock(requestPayload map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	// Extract L1 attributes deposit (always transactions[0] in OP)
	var txs []json.RawMessage
	if err := json.Unmarshal(requestPayload["transactions"], &txs); err != nil || len(txs) == 0 {
		return nil, fmt.Errorf("failed to get transactions from payload")
	}

	// Keep the payload similar, just really duplicating the L1 deposit transaction here
	newPayload := map[string]json.RawMessage{
		"timestamp":             requestPayload["timestamp"],
		"prevRandao":            requestPayload["prevRandao"],
		"suggestedFeeRecipient": requestPayload["feeRecipient"],
		"withdrawals":           requestPayload["withdrawals"],
		"parentBeaconBlockRoot": zeroHash,
		"transactions":          json.RawMessage(fmt.Sprintf("[%s,%s]", string(txs[0]), string(txs[0]))),
		"noTxPool":              json.RawMessage("true"),
		"gasLimit":              requestPayload["gasLimit"],
		"eip1559Params":         json.RawMessage(`"0x000000fa00000006"`),
		"minBaseFee":            json.RawMessage("0"),
	}

	newState := map[string]json.RawMessage{
		"headBlockHash":      requestPayload["parentHash"],
		"safeBlockHash":      zeroHash,
		"finalizedBlockHash": zeroHash,
	}

	// Send the modified block and payload to upstream
	result, err := i.callUpstream(forkUpdated, []any{newState, newPayload})
	if err != nil {
		return nil, fmt.Errorf("forkchoiceUpdatedV3 failed: %v", err)
	}

	var payloadId payloadIDResponse
	if err := json.Unmarshal(result, &payloadId); err != nil || payloadId.PayloadID == nil {
		return nil, fmt.Errorf("no payloadId returned: %s", result)
	}

	// Give geth time to build
	time.Sleep(500 * time.Millisecond)

	// Retrieve the payload from upstream now
	enginePayload, err := i.callUpstream(engineGetPayload, []any{*payloadId.PayloadID})
	if err != nil {
		return nil, fmt.Errorf("getPayloadV4 failed: %v", err)
	}

	var payload executionPayload
	if err := json.Unmarshal(enginePayload, &payload); err != nil || payload.ExecutionPayload == nil {
		return nil, fmt.Errorf("failed to parse getPayloadV4 response: %s", enginePayload)
	}

	return payload.ExecutionPayload, nil
}

func (i *Interceptor) handleNewPayloadRequest(rpcRequest jsonRpcRequest) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rpcRequest.Params[0], &payload); err != nil {
		return nil, err
	}

	var blockHex string
	if err := json.Unmarshal(payload["blockNumber"], &blockHex); err != nil {
		return nil, err
	}
	blockNum, err := strconv.ParseUint(strings.TrimPrefix(blockHex, "0x"), 16, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse block number from hex: %v", err)
	}

	// Only allow one bad request to go through for a malicious block
	log.Printf("mock engine at block number: %d", blockNum)
	i.mu.Lock()
	if (i.maliciousBlockNum > 0 && blockNum != i.maliciousBlockNum) || !i.allowMaliciousBlock {
		// Nothing to do
		i.mu.Unlock()
		return nil, nil
	}
	i.allowMaliciousBlock = false
	i.mu.Unlock()

	return i.handleMaliciousBlock(blockNum, rpcRequest, payload)
}

func (i *Interceptor) handleForkchoiceUpdatedRequest(rpcRequest jsonRpcRequest) ([]byte, error) {
	var forkUpdatedResponse map[string]json.RawMessage
	if err := json.Unmarshal(rpcRequest.Params[0], &forkUpdatedResponse); err != nil {
		return nil, err
	}

	i.mu.RLock()
	var headHash string
	json.Unmarshal(forkUpdatedResponse["headBlockHash"], &headHash)
	maliciousHash, ok := i.maliciousBlockHashes[headHash]
	i.mu.RUnlock()

	if !ok {
		// Nothing to do
		return nil, nil
	}

	log.Printf("replacing forkchoiceUpdated head %s -> %s", headHash, maliciousHash)
	forkUpdatedResponse["headBlockHash"] = json.RawMessage(`"` + maliciousHash + `"`)

	response, err := json.Marshal(forkUpdatedResponse)
	if err != nil {
		log.Printf("failed to marshal fork updated response, err: %v", err)
		return nil, err
	}
	rpcRequest.Params[0] = response
	newBody, err := json.Marshal(rpcRequest)
	if err != nil {
		log.Printf("failed to marshal request, err: %v", err)
		return nil, err
	}
	return newBody, nil
}

func (i *Interceptor) Intercept(body []byte) ([]byte, []byte, error) {
	var rpcRequest jsonRpcRequest
	if err := json.Unmarshal(body, &rpcRequest); err != nil {
		return body, nil, nil
	}

	if len(rpcRequest.Params) == 0 {
		return body, nil, nil
	}

	switch rpcRequest.Method {
	case engineNewPayload:
		newBody, err := i.handleNewPayloadRequest(rpcRequest)
		if err != nil {
			log.Printf("error handling new payload request: %v", err)
			return body, nil, nil
		}
		if newBody == nil {
			return body, nil, nil
		}
		return nil, newBody, nil

	case forkUpdated:
		newBody, err := i.handleForkchoiceUpdatedRequest(rpcRequest)
		if err != nil {
			log.Printf("error handling fork updated request: %v", err)
			return body, nil, nil
		}
		if newBody == nil {
			return body, nil, nil
		}
		return newBody, nil, nil
	}

	return body, nil, nil
}

package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
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
	forkUpdated = "engine_forkchoiceUpdatedV3"
	getPayload  = "engine_getPayloadV4"
	newPayload  = "engine_newPayloadV4"
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
	transport            http.RoundTripper
	allowMaliciousBlock  bool
	mu                   sync.RWMutex
	maliciousBlockHashes map[string]string
	jwtSecret            []byte
	upstreamAddress      string
	maliciousBlockNum    uint64
}

func (t *Interceptor) getJwt() (string, error) {
	now := time.Now()

	claims := jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(60 * time.Second).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString(t.jwtSecret)
}

func (t *Interceptor) callUpstream(method string, params interface{}) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	req, err := http.NewRequest("POST", t.upstreamAddress, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	token, err := t.getJwt()
	if err != nil {
		return json.RawMessage{}, err
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

func (i *Interceptor) RoundTrip(httpRequest *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(httpRequest.Body)
	if err != nil {
		log.Printf("failed to get body: %v", err)
		return i.transport.RoundTrip(httpRequest)
	}
	httpRequest.Body = io.NopCloser(bytes.NewReader(body))

	var rpcReq jsonRpcRequest
	if err := json.Unmarshal(body, &rpcReq); err != nil {
		log.Printf("failed to unmarshall body: %v", err)
		return i.transport.RoundTrip(httpRequest)
	}

	switch rpcReq.Method {
	case newPayload:
		return i.handleNewPayload(httpRequest, rpcReq)
	case forkUpdated:
		return i.handleForkchoiceUpdated(httpRequest, rpcReq)
	}

	return i.transport.RoundTrip(httpRequest)
}

func (i *Interceptor) handleMaliciousBlock(
	blockNum uint64,
	httpRequest *http.Request,
	parsedRequest jsonRpcRequest,
	rawPayload map[string]json.RawMessage) (*http.Response, error) {

	i.allowMaliciousBlock = false

	var blockHash string
	json.Unmarshal(rawPayload["blockHash"], &blockHash)
	log.Printf("intercepted engine_newPayloadV4 for block %d (hash %s), building replacement", blockNum, blockHash)

	// Build a replacement block on the same parent with only the L1 deposit tx
	replacementPayload, err := i.buildMaliciousBlock(rawPayload)
	if err != nil {
		log.Printf("failed to build replacement block: %v, forwarding original", err)
		return i.forwardNewPayload(httpRequest, parsedRequest)
	}

	var maliciousBlockHash string
	json.Unmarshal(replacementPayload["blockHash"], &maliciousBlockHash)
	log.Printf("replacement block built (hash %s), submitting", maliciousBlockHash)

	// Submit the replacement block to geth
	modifiedParams := []interface{}{replacementPayload}
	for i := 1; i < len(parsedRequest.Params); i++ {
		modifiedParams = append(modifiedParams, parsedRequest.Params[i])
	}
	result, err := i.callUpstream(newPayload, modifiedParams)
	if err != nil {
		log.Printf(" failed to submit replacement block: %v, forwarding original", err)
		return i.forwardNewPayload(httpRequest, parsedRequest)
	}

	// Store hash mapping so subsequent forkchoiceUpdated calls use the replacement
	i.mu.Lock()
	i.maliciousBlockHashes[blockHash] = maliciousBlockHash
	i.mu.Unlock()
	log.Printf("hash mapping registered %s -> %s", blockHash, maliciousBlockHash)

	// Return the submit result to op-node as if the original payload was accepted
	return httpResponse(parsedRequest.ID, result), nil
}

func (i *Interceptor) handleNewPayload(httpRequest *http.Request, parsedRequest jsonRpcRequest) (*http.Response, error) {
	if len(parsedRequest.Params) == 0 {
		return i.transport.RoundTrip(httpRequest)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(parsedRequest.Params[0], &payload); err != nil {
		return i.transport.RoundTrip(httpRequest)
	}

	blockNum, ok := parseHexUint64(payload["blockNumber"])
	if !ok {
		return i.transport.RoundTrip(httpRequest)
	}

	// Only allow one bad request to go through for a malicious block
	log.Printf("mock engine at block number: %d", blockNum)
	if (i.maliciousBlockNum > 0 && blockNum != i.maliciousBlockNum) || !i.allowMaliciousBlock {
		return i.transport.RoundTrip(httpRequest)
	}
	i.allowMaliciousBlock = false

	return i.handleMaliciousBlock(blockNum, httpRequest, parsedRequest, payload)

}

func (i *Interceptor) handleForkchoiceUpdated(httpRequest *http.Request, parsedRequest jsonRpcRequest) (*http.Response, error) {
	if len(parsedRequest.Params) == 0 {
		return i.transport.RoundTrip(httpRequest)
	}

	var forkUpdatedResponse map[string]json.RawMessage
	if err := json.Unmarshal(parsedRequest.Params[0], &forkUpdatedResponse); err != nil {
		return i.transport.RoundTrip(httpRequest)
	}

	i.mu.Lock()
	var headHash string
	json.Unmarshal(forkUpdatedResponse["headBlockHash"], &headHash)
	maliciousHash, ok := i.maliciousBlockHashes[headHash]
	i.mu.Unlock()

	if !ok {
		return i.transport.RoundTrip(httpRequest)
	}

	log.Printf("replacing forkchoiceUpdated head %s -> %s", headHash, maliciousHash)
	forkUpdatedResponse["headBlockHash"] = json.RawMessage(`"` + maliciousHash + `"`)

	response, err := json.Marshal(forkUpdatedResponse)
	if err != nil {
		log.Printf("failed to marshal fork updated response, err: %v", err)
		return i.transport.RoundTrip(httpRequest)
	}
	parsedRequest.Params[0] = response
	newBody, err := json.Marshal(parsedRequest)
	if err != nil {
		log.Printf("failed to marshal request, err: %v", err)
		return i.transport.RoundTrip(httpRequest)
	}
	httpRequest.Body = io.NopCloser(bytes.NewReader(newBody))
	httpRequest.ContentLength = int64(len(newBody))
	return i.transport.RoundTrip(httpRequest)
}

func (i *Interceptor) buildMaliciousBlock(requestPayload map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	// Extract L1 attributes deposit (always transactions[0] in OP)
	var txs []json.RawMessage
	if err := json.Unmarshal(requestPayload["transactions"], &txs); err != nil || len(txs) == 0 {
		return nil, fmt.Errorf("failed to get transactions from payload")
	}

	// Keep the payload similar, just really duplicating the transactions here
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
	result, err := i.callUpstream(forkUpdated, []interface{}{newState, newPayload})
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
	enginePayload, err := i.callUpstream(getPayload, []interface{}{*payloadId.PayloadID})
	if err != nil {
		return nil, fmt.Errorf("getPayloadV4 failed: %v", err)
	}

	var payload executionPayload
	if err := json.Unmarshal(enginePayload, &payload); err != nil || payload.ExecutionPayload == nil {
		return nil, fmt.Errorf("failed to parse getPayloadV4 response: %s", enginePayload)
	}

	return payload.ExecutionPayload, nil
}

func (t *Interceptor) forwardNewPayload(req *http.Request, rpcRequest jsonRpcRequest) (*http.Response, error) {
	body, err := json.Marshal(rpcRequest)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	return t.transport.RoundTrip(req)
}

func httpResponse(id json.RawMessage, result json.RawMessage) *http.Response {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	return &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Content-Length": []string{strconv.Itoa(len(body))},
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func parseHexUint64(raw json.RawMessage) (uint64, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	return n, err == nil
}

func main() {
	upstream := flag.String("upstream", "", "upstream engine URL (required)")
	addr := flag.String("addr", ":8080", "listen address")
	jwtFile := flag.String("jwt-secret", "", "path to JWT secret file (hex-encoded)")
	flag.Parse()

	if *upstream == "" {
		log.Fatal("--upstream is required")
	}
	if *jwtFile == "" {
		log.Fatal("--jwt-secret is required")
	}

	raw, err := os.ReadFile(*jwtFile)
	if err != nil {
		log.Fatalf("failed to read JWT secret: %v", err)
	}
	jwtSecret, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(string(raw), "0x")))
	if err != nil {
		log.Fatalf("failed to decode JWT secret: %v", err)
	}

	target, err := url.Parse(*upstream)
	if err != nil {
		log.Fatalf("invalid upstream URL: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	interceptor := &Interceptor{
		transport:            http.DefaultTransport,
		maliciousBlockHashes: make(map[string]string),
		allowMaliciousBlock:  false,
		jwtSecret:            jwtSecret,
		upstreamAddress:      *upstream,
		maliciousBlockNum:    0,
	}
	proxy.Transport = interceptor
	proxy.Director = func(r *http.Request) {
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		r.Host = target.Host
	}

	// Use mux to handle our custom endpoint `create-malicious-block`
	mux := http.NewServeMux()

	// Custom endpoint we dont want to forward these to upstream, handle request here
	mux.HandleFunc("/create-malicious-block", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		type Request struct {
			BlockNumber uint64 `json:"blockNumber"`
		}

		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BlockNumber <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "Invalid JSON or missing/invalid blockNumber",
			})
			return
		}

		interceptor.allowMaliciousBlock = true
		interceptor.maliciousBlockNum = req.BlockNumber

		log.Println("setting malicious block number", "num", req.BlockNumber)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message":     "Malicious block configured",
			"blockNumber": req.BlockNumber,
		})
	})

	// All other paths go to upstream
	mux.Handle("/", proxy)

	log.Printf(" %s -> %s", *addr, *upstream)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	engineURL     = flag.String("engine", "http://localhost:8551", "l1-geth engine API URL (JWT authenticated)")
	ethURL        = flag.String("eth", "http://localhost:8545", "l1-geth eth RPC URL")
	jwtFile       = flag.String("jwt", "/config/jwt.txt", "path to JWT secret hex file")
	blockTimeDur  = flag.Duration("block-time", 2*time.Second, "target L1 block time")
	finalizedDist = flag.Uint64("finalized-distance", 3, "how many blocks behind head to finalize")
	safeDist      = flag.Uint64("safe-distance", 2, "how many blocks behind head to mark as safe")
	listenAddr    = flag.String("addr", ":8555", "listen address for fork HTTP API")
)

type blockTag uint8

const (
	Latest blockTag = iota
	Safe
	Finalized
)

type blockInfo struct {
	Hash   string
	Number uint64
	Time   uint64
}

type rpcBlock struct {
	// Some rpc calls are slightly different json field names
	Hash      string `json:"hash,omitempty"`
	BlockHash string `json:"blockHash,omitempty"`

	Number      string `json:"number,omitempty"`
	BlockNumber string `json:"blockNumber,omitempty"`

	Timestamp string `json:"timestamp"`
}

type forkchoiceState struct {
	HeadBlockHash      string `json:"headBlockHash"`
	SafeBlockHash      string `json:"safeBlockHash"`
	FinalizedBlockHash string `json:"finalizedBlockHash"`
}

type payloadAttributes struct {
	Timestamp             string `json:"timestamp"`
	PrevRandao            string `json:"prevRandao"`
	SuggestedFeeRecipient string `json:"suggestedFeeRecipient"`
	Withdrawals           []any  `json:"withdrawals"`
	ParentBeaconBlockRoot string `json:"parentBeaconBlockRoot"`
}

type forkchoiceUpdatedResponse struct {
	PayloadStatus struct {
		Status          string `json:"status"`
		LatestValidHash string `json:"latestValidHash,omitempty"`
		ValidationError string `json:"validationError,omitempty"`
	} `json:"payloadStatus"`

	PayloadID *string `json:"payloadId,omitempty"`
}

type payloadResponse struct {
	ExecutionPayload  json.RawMessage   `json:"executionPayload"`
	ExecutionRequests []json.RawMessage `json:"executionRequests,omitempty"`
}

type jsonRpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *jsonRpcError   `json:"error,omitempty"`
}

type jsonRpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type FakeBeacon struct {
	mu                sync.Mutex
	engineURL         string
	ethURL            string
	secret            []byte
	blockTime         time.Duration
	finalizedDistance uint64
	safeDistance      uint64

	history   []blockInfo
	safe      blockInfo
	finalized blockInfo
}

func makeJWT(secret []byte) (string, error) {
	now := time.Now()

	claims := jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(60 * time.Second).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString(secret)
}

func callRPC(url string, secret []byte, method string, params []interface{}, result interface{}) error {
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal body: %w", err)
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create POST request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != nil {
		token, err := makeJWT(secret)
		if err != nil {
			return fmt.Errorf("failed to create jwt token from secret: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var rpcResp jsonRpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if result != nil && rpcResp.Result != nil {
		return json.Unmarshal(rpcResp.Result, result)
	}
	return nil
}

func (fb *FakeBeacon) getBlockByTag(tag blockTag) blockInfo {
	len := uint64(len(fb.history))
	latest := len - 1
	switch tag {
	case Latest:
		return fb.history[latest]
	case Safe:
		if len <= fb.safeDistance {
			return fb.history[0]
		}
		return fb.history[latest-fb.safeDistance]
	case Finalized:
		if len <= fb.finalizedDistance {
			return fb.history[0]
		}
		return fb.history[latest-fb.finalizedDistance]
	default:
		log.Println("unknown tag: %w using default tag of latest", tag)
		return fb.history[latest]
	}
}

func (fb *FakeBeacon) bootstrap() error {
	var block rpcBlock
	if err := callRPC(fb.ethURL, nil, "eth_getBlockByNumber", []interface{}{"latest", false}, &block); err != nil {
		return err
	}
	blockNumber, err := strconv.ParseUint(strings.TrimPrefix(block.Number, "0x"), 16, 64)
	if err != nil {
		return fmt.Errorf("failed to convert block number hex: %s to uint64", block.Number)
	}
	blockTimestamp, err := strconv.ParseUint(strings.TrimPrefix(block.Timestamp, "0x"), 16, 64)
	if err != nil {
		return fmt.Errorf("failed to convert block timestamp hex: %s to uint64", block.Timestamp)
	}
	fb.history = append(fb.history, blockInfo{
		Hash:   block.Hash,
		Number: blockNumber,
		Time:   blockTimestamp,
	})
	log.Printf("bootstrapped from block %d hash=%s", fb.history[0].Number, fb.history[0].Hash[:10])
	return nil
}

func (fb *FakeBeacon) updateTags() (latest, safe, finalized blockInfo) {
	latest = fb.getBlockByTag(Latest)

	safe = fb.getBlockByTag(Safe)
	if safe.Number >= fb.safe.Number {
		fb.safe = safe
	}

	finalized = fb.getBlockByTag(Finalized)
	if finalized.Number >= fb.finalized.Number {
		fb.finalized = finalized
	}

	return latest, fb.safe, fb.finalized
}

func (fb *FakeBeacon) initiateBlockBuilding(latest, safe, finalized blockInfo) (string, error) {
	fcs := forkchoiceState{
		HeadBlockHash:      latest.Hash,
		SafeBlockHash:      safe.Hash,
		FinalizedBlockHash: finalized.Hash,
	}
	attrs := payloadAttributes{
		Timestamp:             fmt.Sprintf("0x%x", time.Now().Unix()),
		PrevRandao:            "0x0000000000000000000000000000000000000000000000000000000000000000",
		SuggestedFeeRecipient: "0x0000000000000000000000000000000000000000",
		Withdrawals:           []any{},
		ParentBeaconBlockRoot: "0x0000000000000000000000000000000000000000000000000000000000000000",
	}
	var resp forkchoiceUpdatedResponse
	if err := callRPC(fb.engineURL, fb.secret, "engine_forkchoiceUpdatedV3",
		[]interface{}{fcs, attrs}, &resp); err != nil {
		return "", fmt.Errorf("forkchoiceUpdated: %w", err)
	}
	if resp.PayloadID == nil {
		return "", fmt.Errorf("no payloadId, status=%s", resp.PayloadStatus.Status)
	}
	return *resp.PayloadID, nil
}

func (fb *FakeBeacon) retrievePayload(payloadID string) (payloadResponse, error) {
	var resp payloadResponse
	if err := callRPC(fb.engineURL, fb.secret, "engine_getPayloadV4",
		[]interface{}{payloadID}, &resp); err != nil {
		return payloadResponse{}, fmt.Errorf("getPayload err: %w", err)
	}
	return resp, nil
}

func (fb *FakeBeacon) submitPayload(payload payloadResponse) (rpcBlock, error) {
	reqs := payload.ExecutionRequests
	if reqs == nil {
		reqs = []json.RawMessage{}
	}
	var status struct{ Status string }
	if err := callRPC(fb.engineURL, fb.secret, "engine_newPayloadV4",
		[]interface{}{
			json.RawMessage(payload.ExecutionPayload),
			[]interface{}{},
			"0x0000000000000000000000000000000000000000000000000000000000000000",
			reqs,
		}, &status); err != nil {
		return rpcBlock{}, fmt.Errorf("newPayload: %w", err)
	}
	if status.Status != "VALID" && status.Status != "ACCEPTED" {
		return rpcBlock{}, fmt.Errorf("newPayload status=%s", status.Status)
	}
	var block rpcBlock
	if err := json.Unmarshal(payload.ExecutionPayload, &block); err != nil {
		return rpcBlock{}, fmt.Errorf("failed to unmarshal execution payload: %w", err)
	}
	return block, nil
}

func (fb *FakeBeacon) setCanonicalHead(newBlock rpcBlock) error {
	state := forkchoiceState{
		HeadBlockHash:      newBlock.BlockHash,
		SafeBlockHash:      fb.safe.Hash,
		FinalizedBlockHash: fb.finalized.Hash,
	}
	if err := callRPC(fb.engineURL, fb.secret, "engine_forkchoiceUpdatedV3",
		[]interface{}{state, nil}, nil); err != nil {
		return fmt.Errorf("forkchoiceUpdated canonical: %w", err)
	}
	return nil
}

func parseBlockInfo(block rpcBlock) (blockInfo, error) {
	number, err := strconv.ParseUint(strings.TrimPrefix(block.BlockNumber, "0x"), 16, 64)
	if err != nil {
		return blockInfo{}, fmt.Errorf("failed to convert block number hex: %s to uint64", block.BlockNumber)
	}
	timestamp, err := strconv.ParseUint(strings.TrimPrefix(block.Timestamp, "0x"), 16, 64)
	if err != nil {
		return blockInfo{}, fmt.Errorf("failed to convert block timestamp hex: %s to uint64", block.Timestamp)
	}
	return blockInfo{Hash: block.BlockHash, Number: number, Time: timestamp}, nil
}

func (fb *FakeBeacon) recordBlock(block blockInfo) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.history = append(fb.history, block)
	// cap entries to 500
	if len(fb.history) > 500 {
		fb.history = fb.history[len(fb.history)-500:]
	}
}

func (fb *FakeBeacon) advance() error {
	fb.mu.Lock()
	if len(fb.history) == 0 {
		fb.mu.Unlock()
		// Genesis block
		return fb.bootstrap()
	}
	// Update and retrieve latest, safe and finalized blocks
	latest, safe, finalized := fb.updateTags()
	fb.mu.Unlock()

	// Send payload attributes to geth engine to start building a new block.
	payloadID, err := fb.initiateBlockBuilding(latest, safe, finalized)
	if err != nil {
		return err
	}

	// wait for block time to elapse before sealing
	time.Sleep(fb.blockTime)

	// Retrieve built payload from geth engine
	payload, err := fb.retrievePayload(payloadID)
	if err != nil {
		return err
	}

	// Send payload to geth engine and validate the geth engine accepted the payload
	newBlock, err := fb.submitPayload(payload)
	if err != nil {
		return err
	}

	// Let geth engine know we accepted the new block
	if err := fb.setCanonicalHead(newBlock); err != nil {
		return err
	}

	// Extract block information for our own knowledge
	block, err := parseBlockInfo(newBlock)
	if err != nil {
		return err
	}

	fb.recordBlock(block)
	log.Printf("block %d hash=%s", block.Number, block.Hash[:10])
	return nil
}

func (fb *FakeBeacon) fork(blockNumber uint64) error {
	fb.mu.Lock()
	// first reset the beacon to the block number
	var target blockInfo
	if blockNumber >= uint64(len(fb.history)) {
		fb.mu.Unlock()
		return fmt.Errorf("block %d not found in history", blockNumber)
	}

	target = fb.history[blockNumber]
	fb.history = fb.history[:blockNumber+1]
	fb.mu.Unlock()

	// call geth and tell it to fork back to the block number
	if err := callRPC(fb.ethURL, nil, "debug_setHead",
		[]interface{}{fmt.Sprintf("0x%x", target.Number)}, nil); err != nil {
		return fmt.Errorf("debug_setHead: %w", err)
	}
	log.Printf("forked to block %d hash=%s", target.Number, target.Hash[:10])
	return nil
}

func main() {
	flag.Parse()

	secretHex, err := os.ReadFile(*jwtFile)
	if err != nil {
		log.Fatalf("read jwt secret: %v", err)
	}
	secret, err := hex.DecodeString(strings.TrimSpace(string(secretHex)))
	if err != nil {
		log.Fatalf("decode jwt secret: %v", err)
	}

	fb := &FakeBeacon{
		engineURL:         *engineURL,
		ethURL:            *ethURL,
		secret:            secret,
		blockTime:         *blockTimeDur,
		finalizedDistance: *finalizedDist,
		safeDistance:      *safeDist,
	}

	http.HandleFunc("/fork", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			BlockNumber uint64 `json:"blockNum"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("fork requested to number %d", req.BlockNumber)
		if err := fb.fork(req.BlockNumber); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/eth/v1/node/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]string{"version": "fake-beacon/v1.0"},
		})
	})

	go func() {
		log.Printf("fake-beacon API listening on %s", *listenAddr)
		log.Fatal(http.ListenAndServe(*listenAddr, nil))
	}()

	log.Printf("fake-beacon starting: engine=%s eth=%s blockTime=%s", *engineURL, *ethURL, *blockTimeDur)
	for {
		if err := fb.advance(); err != nil {
			log.Printf("step error: %v", err)
			time.Sleep(time.Second)
		}
	}
}

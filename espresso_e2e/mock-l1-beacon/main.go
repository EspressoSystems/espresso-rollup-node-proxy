package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
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

// makeJWT creates a HS256 JWT for the engine API.
func makeJWT(secret []byte) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iat":%d}`, time.Now().Unix())))
	msg := h + "." + p
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	return msg + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
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
		req.Header.Set("Authorization", "Bearer "+makeJWT(secret))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.Error != nil {
		return fmt.Errorf("rpc error %d: %s", out.Error.Code, out.Error.Message)
	}
	if result != nil && out.Result != nil {
		return json.Unmarshal(out.Result, result)
	}
	return nil
}

func fakeBeaconRoot(timestamp uint64) string {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], timestamp)
	h := sha256.Sum256(b[:])
	return "0x" + hex.EncodeToString(h[:])
}

func parseHex(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	return n
}

type blockInfo struct {
	Hash   string
	Number uint64
	Time   uint64
}

type FakeBeacon struct {
	mu            sync.Mutex
	engineURL     string
	ethURL        string
	secret        []byte
	blockTime     time.Duration
	finalizedDist uint64
	safeDist      uint64

	history []blockInfo
}

func (fb *FakeBeacon) head() blockInfo      { return fb.history[len(fb.history)-1] }
func (fb *FakeBeacon) safe() blockInfo      { return fb.atDepth(fb.safeDist) }
func (fb *FakeBeacon) finalized() blockInfo { return fb.atDepth(fb.finalizedDist) }
func (fb *FakeBeacon) atDepth(d uint64) blockInfo {
	if uint64(len(fb.history)) <= d {
		return fb.history[0]
	}
	return fb.history[uint64(len(fb.history))-1-d]
}

func (fb *FakeBeacon) bootstrap() error {
	var blk struct {
		Hash      string `json:"hash"`
		Number    string `json:"number"`
		Timestamp string `json:"timestamp"`
	}
	if err := callRPC(fb.ethURL, nil, "eth_getBlockByNumber", []interface{}{"latest", false}, &blk); err != nil {
		return err
	}
	fb.history = append(fb.history, blockInfo{
		Hash:   blk.Hash,
		Number: parseHex(blk.Number),
		Time:   parseHex(blk.Timestamp),
	})
	log.Printf("bootstrapped from block %d hash=%s", fb.history[0].Number, fb.history[0].Hash[:10])
	return nil
}

func (fb *FakeBeacon) advance() error {
	fb.mu.Lock()
	if len(fb.history) == 0 {
		fb.mu.Unlock()
		return fb.bootstrap()
	}

	head := fb.head()
	safe := fb.safe()
	fin := fb.finalized()
	fb.mu.Unlock()

	beaconRoot := fakeBeaconRoot(head.Time)

	fcState := map[string]string{
		"headBlockHash":      head.Hash,
		"safeBlockHash":      safe.Hash,
		"finalizedBlockHash": fin.Hash,
	}
	payloadAttrs := map[string]interface{}{
		"timestamp":             fmt.Sprintf("0x%x", time.Now().Unix()),
		"prevRandao":            "0x0000000000000000000000000000000000000000000000000000000000000000",
		"suggestedFeeRecipient": "0x0000000000000000000000000000000000000000",
		"withdrawals":           []interface{}{},
		"parentBeaconBlockRoot": beaconRoot,
	}
	var fcResp struct {
		PayloadStatus struct{ Status string } `json:"payloadStatus"`
		PayloadID     *string                 `json:"payloadId"`
	}
	if err := callRPC(fb.engineURL, fb.secret, "engine_forkchoiceUpdatedV3",
		[]interface{}{fcState, payloadAttrs}, &fcResp); err != nil {
		return fmt.Errorf("forkchoiceUpdated: %w", err)
	}
	if fcResp.PayloadID == nil {
		return fmt.Errorf("no payloadId, status=%s", fcResp.PayloadStatus.Status)
	}

	// wait for block time to elapse
	time.Sleep(fb.blockTime)

	var payloadResp struct {
		ExecutionPayload  json.RawMessage   `json:"executionPayload"`
		ExecutionRequests []json.RawMessage `json:"executionRequests"`
	}
	if err := callRPC(fb.engineURL, fb.secret, "engine_getPayloadV4",
		[]interface{}{*fcResp.PayloadID}, &payloadResp); err != nil {
		return fmt.Errorf("error calling rpc: %w", err)
	}

	var newPayloadStatus struct{ Status string }
	reqs := make([]json.RawMessage, 0)
	if payloadResp.ExecutionRequests != nil {
		reqs = payloadResp.ExecutionRequests
	}
	if err := callRPC(fb.engineURL, fb.secret, "engine_newPayloadV4",
		[]interface{}{json.RawMessage(payloadResp.ExecutionPayload), []interface{}{}, beaconRoot, reqs},
		&newPayloadStatus); err != nil {
		return fmt.Errorf("newPayload: %w", err)
	}
	if newPayloadStatus.Status != "VALID" && newPayloadStatus.Status != "ACCEPTED" {
		return fmt.Errorf("newPayload status=%s", newPayloadStatus.Status)
	}

	var newBlock struct {
		BlockHash   string `json:"blockHash"`
		BlockNumber string `json:"blockNumber"`
		Timestamp   string `json:"timestamp"`
	}
	if err := json.Unmarshal(payloadResp.ExecutionPayload, &newBlock); err != nil {
		return fmt.Errorf("failed to unmarshal execution payload: %w", err)
	}

	// make canonical
	canonicalFC := map[string]string{
		"headBlockHash":      newBlock.BlockHash,
		"safeBlockHash":      safe.Hash,
		"finalizedBlockHash": fin.Hash,
	}
	if err := callRPC(fb.engineURL, fb.secret, "engine_forkchoiceUpdatedV3",
		[]interface{}{canonicalFC, nil}, nil); err != nil {
		return fmt.Errorf("forkchoiceUpdated canonical: %w", err)
	}

	block := blockInfo{
		Hash:   newBlock.BlockHash,
		Number: parseHex(newBlock.BlockNumber),
		Time:   parseHex(newBlock.Timestamp),
	}
	fb.mu.Lock()
	fb.history = append(fb.history, block)
	if len(fb.history) > 2000 {
		fb.history = fb.history[len(fb.history)-2000:]
	}
	fb.mu.Unlock()

	log.Printf("block %d hash=%s", block.Number, block.Hash[:10])
	return nil
}

func (fb *FakeBeacon) fork(blockNumber uint64) error {
	fb.mu.Lock()
	var target blockInfo
	if blockNumber >= uint64(len(fb.history)) {
		fb.mu.Unlock()
		return fmt.Errorf("block %d not found in history", blockNumber)
	}

	target = fb.history[blockNumber]
	fb.history = fb.history[:blockNumber+1]
	fb.mu.Unlock()

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
		engineURL:     *engineURL,
		ethURL:        *ethURL,
		secret:        secret,
		blockTime:     *blockTimeDur,
		finalizedDist: *finalizedDist,
		safeDist:      *safeDist,
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
		json.NewEncoder(w).Encode(map[string]interface{}{
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

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/golang/snappy"

	libp2p "github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsub_pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

// meshTracer logs GRAFT and PRUNE events so we can see exactly why messages stop flowing.
type meshTracer struct{}

func (t *meshTracer) AddPeer(p peer.ID, proto protocol.ID) {
	log.Printf("[ADD_PEER] peer=%s proto=%s", p, proto)
}
func (t *meshTracer) RemovePeer(p peer.ID)          { log.Printf("[REMOVE_PEER] peer=%s", p) }
func (t *meshTracer) Join(topic string)             {}
func (t *meshTracer) Leave(topic string)            {}
func (t *meshTracer) Graft(p peer.ID, topic string) { log.Printf("[GRAFT] peer=%s topic=%s", p, topic) }
func (t *meshTracer) Prune(p peer.ID, topic string) { log.Printf("[PRUNE] peer=%s topic=%s", p, topic) }
func (t *meshTracer) ValidateMessage(msg *pubsub.Message) {
	log.Printf("[VALIDATE] topic=%s", msg.GetTopic())
}
func (t *meshTracer) DeliverMessage(msg *pubsub.Message) {
	log.Printf("[DELIVER] topic=%s from=%s", msg.GetTopic(), msg.ReceivedFrom)
}
func (t *meshTracer) RejectMessage(msg *pubsub.Message, reason string) {
	log.Printf("[REJECT] topic=%s reason=%s", msg.GetTopic(), reason)
}
func (t *meshTracer) DuplicateMessage(msg *pubsub.Message) {
	log.Printf("[DUPLICATE] topic=%s", msg.GetTopic())
}
func (t *meshTracer) ThrottlePeer(p peer.ID) { log.Printf("[THROTTLE] peer=%s", p) }
func (t *meshTracer) RecvRPC(rpc *pubsub.RPC) {
	for _, sub := range rpc.Subscriptions {
		log.Printf("[RECV_SUB] topic=%s subscribe=%v", sub.GetTopicid(), sub.GetSubscribe())
	}
	if c := rpc.Control; c != nil {
		for _, g := range c.Graft {
			log.Printf("[RECV_GRAFT] topic=%s", g.GetTopicID())
		}
		for _, p := range c.Prune {
			log.Printf("[RECV_PRUNE] topic=%s", p.GetTopicID())
		}
	}
}
func (t *meshTracer) SendRPC(rpc *pubsub.RPC, p peer.ID) {
	if len(rpc.Publish) > 0 {
		log.Printf("[SEND_MSG] to=%s count=%d", p, len(rpc.Publish))
	}
	if c := rpc.Control; c != nil {
		for _, g := range c.Graft {
			log.Printf("[SEND_GRAFT] to=%s topic=%s", p, g.GetTopicID())
		}
		for _, pr := range c.Prune {
			log.Printf("[SEND_PRUNE] to=%s topic=%s", p, pr.GetTopicID())
		}
	}
}
func (t *meshTracer) DropRPC(rpc *pubsub.RPC, p peer.ID) { log.Printf("[DROP_RPC] peer=%s", p) }
func (t *meshTracer) UndeliverableMessage(msg *pubsub.Message) {
	log.Printf("[UNDELIVERABLE] topic=%s", msg.GetTopic())
}

// registerReqRespForwarding handles the op-stack payload_by_number sync protocol.
// Wire format: client sends 8-byte little-endian block number, server responds
// with 1-byte result code followed by snappy-compressed SSZ payload data.
// The proxy just forwards the raw bytes between the fullnode and sequencer streams.
func registerReqRespForwarding(ctx context.Context, serverHost, clientHost host.Host, seqID peer.ID, chainID string) {
	protoID := protocol.ID(fmt.Sprintf("/opstack/req/payload_by_number/%s/0", chainID))
	serverHost.SetStreamHandler(protoID, func(inStream network.Stream) {
		defer inStream.Close()

		// Read the 8-byte block number from the fullnode.
		var req [8]byte
		if _, err := io.ReadFull(inStream, req[:]); err != nil {
			log.Printf("[REQ_RESP] read request: %v", err)
			inStream.Reset()
			return
		}

		// Open a stream to the sequencer for the same protocol.
		outStream, err := clientHost.NewStream(ctx, seqID, protoID)
		if err != nil {
			log.Printf("[REQ_RESP] open sequencer stream: %v", err)
			inStream.Reset()
			return
		}
		defer outStream.Close()

		// Forward the request and pipe the response back.
		if _, err := outStream.Write(req[:]); err != nil {
			log.Printf("[REQ_RESP] write request: %v", err)
			return
		}
		outStream.CloseWrite()

		n, err := io.Copy(inStream, outStream)
		if err != nil {
			log.Printf("[REQ_RESP] copy response: %v", err)
		}
		log.Printf("[REQ_RESP] forwarded block response %d bytes", n)
	})
	log.Printf("registered req/resp forwarding on %s", protoID)
}

func fetchSequencerAddrInfo(rpcURL, p2pAddr string) (*peer.AddrInfo, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "opp2p_self",
		"params":  []any{},
		"id":      1,
	})
	resp, err := http.Post(rpcURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Result struct {
			PeerID string `json:"peerID"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Result.PeerID == "" {
		return nil, fmt.Errorf("empty peerID in response")
	}

	fullMA, err := ma.NewMultiaddr(fmt.Sprintf("%s/p2p/%s", p2pAddr, result.Result.PeerID))
	if err != nil {
		return nil, err
	}
	return peer.AddrInfoFromP2pAddr(fullMA)
}

func decodeBlock(data []byte, version eth.BlockVersion) (*eth.ExecutionPayload, error) {
	if len(data) < 65 {
		return nil, fmt.Errorf("message too short (%d bytes)", len(data))
	}
	payloadBytes := data[65:] // first 65 bytes are secp256k1 signature

	if version.HasParentBeaconBlockRoot() {
		var envelope eth.ExecutionPayloadEnvelope
		if err := envelope.UnmarshalSSZ(version, uint32(len(payloadBytes)), bytes.NewReader(payloadBytes)); err != nil {
			return nil, fmt.Errorf("unmarshal envelope: %w", err)
		}
		return envelope.ExecutionPayload, nil
	}

	var payload eth.ExecutionPayload
	if err := payload.UnmarshalSSZ(version, uint32(len(payloadBytes)), bytes.NewReader(payloadBytes)); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}
	return &payload, nil
}

func waitForBlockNumber(ctx context.Context, rpcURL string) error {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return fmt.Errorf("failed to dial RPC: %w", err)
	}
	defer client.Close()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		block, err := client.BlockByNumber(ctx, nil) // nil = latest block
		if err != nil {
			log.Printf("failed to get block number: %v", err)
		} else if block.NumberU64() > 0 {
			log.Printf("block number %d detected, proceeding", block.NumberU64())
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// continue retrying
		}
	}
}

func decodeEnvelope(data []byte) (*eth.ExecutionPayloadEnvelope, error) {
	if len(data) < 65 {
		return nil, fmt.Errorf("message too short (%d bytes)", len(data))
	}
	payloadBytes := data[65:] // first 65 bytes are secp256k1 signature
	var envelope eth.ExecutionPayloadEnvelope
	if err := envelope.UnmarshalSSZ(eth.BlockV4, uint32(len(payloadBytes)), bytes.NewReader(payloadBytes)); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return &envelope, nil
}

// signPayload signs the SSZ payload bytes using the sequencer private key.
// Signing domain is SigningDomainBlocksV1 (all zeros), matching op-node.
// Message = keccak256(domain[32] || chainID[32] || keccak256(sszPayload))
func signPayload(sszPayload []byte, privKey *ecdsa.PrivateKey, chainID *big.Int) ([65]byte, error) {
	var domain [32]byte // SigningDomainBlocksV1 = all zeros

	var chainIDBytes [32]byte
	chainID.FillBytes(chainIDBytes[:])

	payloadHash := gethcrypto.Keccak256(sszPayload)

	var msgInput [96]byte
	copy(msgInput[:32], domain[:])
	copy(msgInput[32:64], chainIDBytes[:])
	copy(msgInput[64:], payloadHash)
	signingHash := gethcrypto.Keccak256(msgInput[:])

	sig, err := gethcrypto.Sign(signingHash, privKey)
	if err != nil {
		return [65]byte{}, err
	}
	var out [65]byte
	copy(out[:], sig)
	return out, nil
}

// encodeEnvelope marshals the envelope back to gossipsub wire format:
// 65-byte signature + SSZ bytes, snappy-compressed.
func encodeEnvelope(envelope *eth.ExecutionPayloadEnvelope, privKey *ecdsa.PrivateKey, chainID *big.Int) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := envelope.MarshalSSZ(&buf); err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	sszBytes := buf.Bytes()

	sig, err := signPayload(sszBytes, privKey, chainID)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}

	raw := make([]byte, 65+len(sszBytes))
	copy(raw[:65], sig[:])
	copy(raw[65:], sszBytes)
	return snappy.Encode(nil, raw), nil
}

var zeroHash = json.RawMessage(`"0x0000000000000000000000000000000000000000000000000000000000000000"`)

type engineClient struct {
	url    string
	secret []byte
}

// jwt produces a fresh HS256 JWT for the engine API (valid 60s).
func (c *engineClient) jwt() (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	now := time.Now().Unix()
	claims := fmt.Sprintf(`{"iat":%d,"exp":%d}`, now, now+60)
	payload := base64.RawURLEncoding.EncodeToString([]byte(claims))
	msg := header + "." + payload
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(msg))
	return msg + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (c *engineClient) call(method string, params any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	req, err := http.NewRequest("POST", c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	token, err := c.jwt()
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

// buildBlock asks the engine to build a new block with tx[0] duplicated.
// parentOverride, if non-nil, replaces the headBlockHash in the forkchoice call
// so the new block chains onto a previously-built malicious block.
func (c *engineClient) buildBlock(envelope *eth.ExecutionPayloadEnvelope, parentOverride *common.Hash) (*eth.ExecutionPayloadEnvelope, error) {
	payload := envelope.ExecutionPayload
	payloadJSON, _ := json.Marshal(payload)
	var raw map[string]json.RawMessage
	json.Unmarshal(payloadJSON, &raw)

	var txs []json.RawMessage
	if err := json.Unmarshal(raw["transactions"], &txs); err != nil || len(txs) == 0 {
		return nil, fmt.Errorf("no transactions in payload")
	}
	dupTxs, _ := json.Marshal([]json.RawMessage{txs[0], txs[0]})

	// Use the actual parentBeaconBlockRoot from the envelope so the engine
	// can validate the L1 deposit context correctly.
	parentBeaconRootJSON := zeroHash
	if envelope.ParentBeaconBlockRoot != nil {
		parentBeaconRootJSON, _ = json.Marshal(envelope.ParentBeaconBlockRoot)
	}

	// Extract eip1559Params from extraData (Holocene format: 0x01 || 4-byte denom || 4-byte elasticity).
	eip1559ParamsJSON := json.RawMessage(`"0x000000fa00000006"`)
	if len(payload.ExtraData) >= 9 && payload.ExtraData[0] == 0x01 {
		eip1559ParamsJSON, _ = json.Marshal("0x" + hex.EncodeToString(payload.ExtraData[1:9]))
	}

	headBlockHash := raw["parentHash"]
	if parentOverride != nil {
		headBlockHash, _ = json.Marshal(parentOverride)
	}

	fcState := map[string]json.RawMessage{
		"headBlockHash":      headBlockHash,
		"safeBlockHash":      zeroHash,
		"finalizedBlockHash": zeroHash,
	}
	attrs := map[string]json.RawMessage{
		"timestamp":             raw["timestamp"],
		"prevRandao":            raw["prevRandao"],
		"suggestedFeeRecipient": raw["feeRecipient"],
		"withdrawals":           raw["withdrawals"],
		"parentBeaconBlockRoot": parentBeaconRootJSON,
		"transactions":          dupTxs,
		"noTxPool":              json.RawMessage("true"),
		"gasLimit":              raw["gasLimit"],
		"eip1559Params":         eip1559ParamsJSON,
		"minBaseFee":            json.RawMessage("0"),
	}

	result, err := c.call("engine_forkchoiceUpdatedV3", []any{fcState, attrs})
	if err != nil {
		return nil, fmt.Errorf("forkchoiceUpdated: %w", err)
	}
	var fcuResp struct {
		PayloadID *string `json:"payloadId"`
	}
	if err := json.Unmarshal(result, &fcuResp); err != nil || fcuResp.PayloadID == nil {
		return nil, fmt.Errorf("no payloadId in response: %s", result)
	}

	time.Sleep(500 * time.Millisecond)

	getResult, err := c.call("engine_getPayloadV4", []any{*fcuResp.PayloadID})
	if err != nil {
		return nil, fmt.Errorf("getPayload: %w", err)
	}
	var getResp struct {
		ExecutionPayload json.RawMessage `json:"executionPayload"`
		ParentBeaconRoot *common.Hash    `json:"parentBeaconBlockRoot"`
	}
	if err := json.Unmarshal(getResult, &getResp); err != nil {
		return nil, fmt.Errorf("parse getPayload response: %w", err)
	}
	var ep eth.ExecutionPayload
	if err := json.Unmarshal(getResp.ExecutionPayload, &ep); err != nil {
		return nil, fmt.Errorf("parse execution payload: %w", err)
	}
	return &eth.ExecutionPayloadEnvelope{
		ExecutionPayload:      &ep,
		ParentBeaconBlockRoot: getResp.ParentBeaconRoot,
	}, nil
}

// modify builds a replacement block with tx[0] duplicated using the engine,
// then re-encodes and signs it for gossipsub.
// parentOverride, if non-nil, chains the new block onto a malicious parent.
func modify(data []byte, privKey *ecdsa.PrivateKey, chainID *big.Int, engine *engineClient, parentOverride *common.Hash) ([]byte, error) {
	envelope, err := decodeEnvelope(data)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	newEnvelope, err := engine.buildBlock(envelope, parentOverride)
	if err != nil {
		return nil, fmt.Errorf("build block: %w", err)
	}
	log.Printf("built replacement block hash %s", newEnvelope.ExecutionPayload.BlockHash)

	return encodeEnvelope(newEnvelope, privKey, chainID)
}

func main() {
	seqRPC := flag.String("sequencer-rpc", "", "sequencer op-node RPC URL to discover peer ID (e.g. http://op-node-sequencer:9545)")
	seqP2PAddr := flag.String("sequencer", "", "sequencer p2p address without peer ID (e.g. /dns4/op-node-sequencer/tcp/9003)")
	listenAddr := flag.String("listen", "/ip4/0.0.0.0/tcp/9005", "listen multiaddr")
	topic := flag.String("topic", "", "gossipsub topic to proxy (e.g. /optimism/<chainID>/0/blocks)")
	signerKeyHex := flag.String("signer-key", "", "sequencer p2p signing private key (hex)")
	engineRPC := flag.String("engine-rpc", "", "engine API URL for building replacement blocks (e.g. http://op-geth-fullnode:8551)")
	engineJWT := flag.String("engine-jwt", "", "path to JWT secret file for engine API")
	flag.Parse()

	if *seqRPC == "" || *seqP2PAddr == "" || *topic == "" || *signerKeyHex == "" || *engineRPC == "" || *engineJWT == "" {
		log.Fatal("--sequencer-rpc, --sequencer, --topic, --signer-key, --engine-rpc, and --engine-jwt are required")
	}

	signerKey, err := gethcrypto.HexToECDSA(strings.TrimPrefix(*signerKeyHex, "0x"))
	if err != nil {
		log.Fatalf("invalid --signer-key: %v", err)
	}

	jwtRaw, err := os.ReadFile(*engineJWT)
	if err != nil {
		log.Fatalf("read --engine-jwt: %v", err)
	}
	jwtSecret, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(string(jwtRaw), "0x")))
	if err != nil {
		log.Fatalf("decode jwt secret: %v", err)
	}
	engine := &engineClient{url: *engineRPC, secret: jwtSecret}

	// Extract chain ID from topic "/optimism/<chainID>/<version>/blocks"
	topicParts := strings.Split(*topic, "/")
	if len(topicParts) < 3 {
		log.Fatalf("invalid topic format: %s", *topic)
	}
	chainID, ok := new(big.Int).SetString(topicParts[2], 10)
	if !ok {
		log.Fatalf("invalid chain ID in topic: %s", topicParts[2])
	}

	ctx := context.Background()

	// Server host starts immediately so the peer-connector can wire up the fullnode
	// before the sequencer starts producing blocks.
	serverHost, err := libp2p.New(
		libp2p.ListenAddrStrings(*listenAddr),
	)
	if err != nil {
		log.Fatalf("failed to create server host: %v", err)
	}
	log.Printf("proxy peer ID: %s", serverHost.ID())

	// op-node sends messages with no From/Seqno (WithNoAuthor), so the default
	// message ID (From+Seqno) is always empty — every block looks like a duplicate.
	// Use a content hash instead so each block has a unique ID.
	msgIDFn := func(msg *pubsub_pb.Message) string {
		h := sha256.Sum256(msg.Data)
		return string(h[:])
	}

	// Match op-node's gossipsub config.
	gsOpts := []pubsub.Option{
		pubsub.WithNoAuthor(),
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign),
		pubsub.WithStrictSignatureVerification(false),
		pubsub.WithFloodPublish(true),
		pubsub.WithMessageIdFn(msgIDFn),
		pubsub.WithRawTracer(&meshTracer{}),
	}

	serverPS, err := pubsub.NewGossipSub(ctx, serverHost, gsOpts...)
	if err != nil {
		log.Fatalf("failed to create server gossipsub: %v", err)
	}

	basePrefix := (*topic)[:len(*topic)-len("/0/blocks")]
	variants := []string{
		basePrefix + "/0/blocks",
		basePrefix + "/1/blocks",
		basePrefix + "/2/blocks",
		basePrefix + "/3/blocks",
	}

	// Join server-side topics immediately so the fullnode can subscribe before the sequencer starts.
	serverTopics := make(map[string]*pubsub.Topic, len(variants))
	for _, name := range variants {
		st, err := serverPS.Join(name)
		if err != nil {
			log.Fatalf("failed to join server topic %s: %v", name, err)
		}
		serverTopics[name] = st
		log.Printf("joined server topic %s", name)
	}

	// Expose peer ID via HTTP immediately so the peer-connector can run before the sequencer starts.
	go func() {
		peerID := serverHost.ID().String()
		http.HandleFunc("/peer-id", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, peerID)
		})
		log.Printf("peer ID HTTP endpoint on :8080")
		if err := http.ListenAndServe(":8080", nil); err != nil {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	log.Printf("listening on %s, waiting for sequencer...", *listenAddr)

	// Periodically log server-side topic peers.
	go func() {
		for range time.Tick(5 * time.Second) {
			for name, st := range serverTopics {
				if sp := len(st.ListPeers()); sp > 0 {
					log.Printf("topic %s: server peers=%d", name, sp)
				}
			}
		}
	}()

	// Connect to the sequencer in the background. Once connected, forward all
	// gossipsub messages from the sequencer to subscribed fullnode peers.
	go func() {
		var seqAddrInfo *peer.AddrInfo
		for {
			var err error
			seqAddrInfo, err = fetchSequencerAddrInfo(*seqRPC, *seqP2PAddr)
			if err == nil {
				break
			}
			log.Printf("waiting for sequencer RPC (%s): %v", *seqRPC, err)
			time.Sleep(2 * time.Second)
		}
		log.Printf("discovered sequencer peer ID: %s", seqAddrInfo.ID)

		clientHost, err := libp2p.New(
			libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		)
		if err != nil {
			log.Fatalf("failed to create client host: %v", err)
		}

		clientPS, err := pubsub.NewGossipSub(ctx, clientHost, gsOpts...)
		if err != nil {
			log.Fatalf("failed to create client gossipsub: %v", err)
		}

		if err := clientHost.Connect(ctx, *seqAddrInfo); err != nil {
			log.Fatalf("failed to connect to sequencer: %v", err)
		}
		log.Printf("connected to sequencer %s", seqAddrInfo.ID)

		registerReqRespForwarding(ctx, serverHost, clientHost, seqAddrInfo.ID, chainID.String())

		type incomingMsg struct {
			msg       *pubsub.Message
			topicName string
		}
		msgCh := make(chan incomingMsg, 50)

		for _, name := range variants {
			ct, err := clientPS.Join(name)
			if err != nil {
				log.Fatalf("failed to join client topic %s: %v", name, err)
			}
			sub, err := ct.Subscribe()
			if err != nil {
				log.Fatalf("failed to subscribe to %s: %v", name, err)
			}
			log.Printf("subscribed to %s", name)

			name := name
			go func() {
				for {
					msg, err := sub.Next(ctx)
					if err != nil {
						log.Printf("subscription error on %s: %v", name, err)
						return
					}
					msgCh <- incomingMsg{msg, name}
				}
			}()
		}

		log.Println("waiting for block to go up")
		// err = waitForBlockNumber(context.Background(), "http://op-geth-fullnode:8546")
		// if err != nil {
		// 	panic(err)
		// }

		// maliciousChain maps original block hash -> malicious block hash for
		// blocks we have replaced, so subsequent blocks can chain onto them.
		maliciousChain := make(map[common.Hash]common.Hash)
		const maxChainLen = 2 // how many blocks to chain after the initial malicious block

		log.Println("starting")
		for m := range msgCh {
			data, err := snappy.Decode(nil, m.msg.Data)
			if err != nil {
				log.Printf("snappy decode failed topic=%s from=%s err=%v", m.topicName, m.msg.ReceivedFrom, err)
				continue
			}
			d, err := decodeBlock(data, eth.BlockV4)
			if err != nil {
				log.Printf("decode block failed: %v", err)
				continue
			}

			log.Printf("message on topic %s from %s block %d", m.topicName, m.msg.From, d.BlockNumber)
			outData := m.msg.Data

			// Decide whether to build a malicious replacement for this block.
			var parentOverride *common.Hash
			if d.BlockNumber == 25 {
				// Initial trigger: replace this specific block.
			} else if maliciousParent, ok := maliciousChain[d.ParentHash]; ok && len(maliciousChain) <= maxChainLen {
				// Parent is in our malicious chain — build on top of the malicious parent.
				parentOverride = &maliciousParent
			} else {
				// Nothing to do for this block.
				if err := serverTopics[m.topicName].Publish(ctx, outData); err != nil {
					log.Printf("publish error: %v", err)
				}
				continue
			}

			log.Printf("building malicious replacement for block %d (chain len %d)", d.BlockNumber, len(maliciousChain))
			outData, err = modify(data, signerKey, chainID, engine, parentOverride)
			if err != nil {
				log.Printf("modify error: %v, forwarding original", err)
				outData = m.msg.Data
			} else {
				// Decode the replacement to record its hash for chaining.
				replacedData, decErr := snappy.Decode(nil, outData)
				if decErr == nil {
					if replacedBlock, decErr := decodeBlock(replacedData, eth.BlockV4); decErr == nil {
						maliciousChain[d.BlockHash] = replacedBlock.BlockHash
						log.Printf("chained %s -> %s (chain len %d)", d.BlockHash, replacedBlock.BlockHash, len(maliciousChain))
					}
				}
			}

			if err := serverTopics[m.topicName].Publish(ctx, outData); err != nil {
				log.Printf("publish error: %v", err)
			}
		}
	}()

	select {} // block until killed
}

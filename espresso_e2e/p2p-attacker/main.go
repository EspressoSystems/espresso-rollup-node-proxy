package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"

	gethcrypto "github.com/ethereum/go-ethereum/crypto"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	// Where to bind this services p2p listener address to
	p2pListenerAddress = "/ip4/0.0.0.0/tcp/9005"

	// Sequencer address to ask for their peer id
	sequencerRpcAddress = "http://op-node-sequencer:9545"

	// Sequencer p2p address, to connect to after discovering their peer id
	sequencerP2PAddress = "/dns4/op-node-sequencer/tcp/9003"

	// Full node engine rpc used sending malicious block payload to
	opFullNodeEngineRpc = "http://op-reth-fullnode:8552"

	// Jwt token path
	jwtPath = "/config/jwt.txt"

	// L2 chain id for rollup
	l2ChainId = 22_266_222

	// Sequencer private key, used for signing modified gossiped messages
	sequencerPrivateKey = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
)

func fetchSequencerPeerAddressInfo() (*peer.AddrInfo, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "opp2p_self",
		"params":  []any{},
		"id":      1,
	})
	resp, err := http.Post(sequencerRpcAddress, "application/json", bytes.NewReader(body))
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

	multiAddress, err := ma.NewMultiaddr(fmt.Sprintf("%s/p2p/%s", sequencerP2PAddress, result.Result.PeerID))
	if err != nil {
		return nil, err
	}
	return peer.AddrInfoFromP2pAddr(multiAddress)
}

func main() {
	chainId := big.NewInt(l2ChainId)

	signerKey, err := gethcrypto.HexToECDSA(strings.TrimPrefix(sequencerPrivateKey, "0x"))
	if err != nil {
		log.Fatalf("invalid signer-key: %v", err)
	}

	jwtBytes, err := os.ReadFile(jwtPath)
	if err != nil {
		log.Fatalf("invalid jwt path: %v", err)
	}
	jwtSecret, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(string(jwtBytes), "0x")))
	if err != nil {
		log.Fatalf("error decoding jwt secret: %v", err)
	}

	seqAddrInfo, err := fetchSequencerPeerAddressInfo()
	if err != nil {
		log.Fatalf("failed to fetch sequencer address info: %v", err)
	}
	log.Printf("discovered sequencer peer ID: %s", seqAddrInfo.ID)

	p2p := NewP2P(opFullNodeEngineRpc, jwtSecret, signerKey, p2pListenerAddress, chainId, seqAddrInfo)

	http.HandleFunc("/peer-id", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, p2p.libp2pServer.ID().String())
	})

	http.HandleFunc("/stop-fork", func(w http.ResponseWriter, r *http.Request) {
		p2p.StopFork()
		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/create-fork-at-block", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		type Request struct {
			BlockNumber uint64 `json:"blockNumber"`
		}

		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BlockNumber == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid or missing blockNumber"})
			return
		}

		log.Printf("create-fork-at-block requested for block %d", req.BlockNumber)
		p2p.SetForkBlock(req.BlockNumber)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"blockNumber": req.BlockNumber})
	})

	go func() {
		log.Printf("HTTP endpoints on :8080")
		if err := http.ListenAndServe(":8080", nil); err != nil {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	p2p.run()
}

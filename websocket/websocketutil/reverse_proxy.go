package websocketutil

import (
	"net/http"
	"net/url"

	"proxy/websocket"

	"github.com/ethereum/go-ethereum/log"
)

// ReverseProxy is a utility for creating a reverse proxy for WebSocket
// connections.
type ReverseProxy struct {
	// URL is the upstream URL to forward all requests to
	URL *url.URL

	// Dialer is the dialer to utilize for the upstream connection.
	Dialer websocket.Dialer

	// Upgrader is the upgrader to utilize for the incoming connection.
	Upgrader websocket.Upgrader

	// Logger represents the logger to use for logging responses
	Logger log.Logger
}

// NewReverseProxy creates a new ReverseProxy with the provided URL, dialer,
// and upgrader.
//
// NOTE: Every incoming Websocket connection will automatically spawn an
// additional goroutine per connection to automatically bridge upstream
// messages to the downstream.
func NewReverseProxy(url *url.URL, dialer websocket.Dialer, upgrader websocket.Upgrader) *ReverseProxy {
	return &ReverseProxy{
		URL:      url,
		Dialer:   dialer,
		Upgrader: upgrader,
	}
}

// getLogger returns the logger to use for logging responses.  If no logger is
// specified the Root logger
func (r *ReverseProxy) getLogger() log.Logger {
	if r.Logger != nil {
		return r.Logger
	}

	return log.Root()
}

// Compile-time interface adherence assertion.
var _ websocket.Upgrader = (*ReverseProxy)(nil)

// Upgrade implements [websocket.Upgrader].
//
// This method will upgrade the incoming HTTP Request into a websocket
// connection, if successful, it will also establish a connection to the
// upstream URL, and automatically bridge messages from the upstream to the
// downstream.
//
// The resulting connection needs to be continuuously read in order to
// bridge the messages from the downstream to the upstream.
//
// Any messages written to the resulting [websocket.Conn] will only
// be written to the downstream.
func (r *ReverseProxy) Upgrade(w http.ResponseWriter, req *http.Request, options ...websocket.UpgradeOption) (websocket.Conn, error) {
	ctx := req.Context()
	downstream, err := r.Upgrader.Upgrade(w, req, options...)
	if err != nil {
		return downstream, err
	}

	var subProtocols []string
	if subProtocol := downstream.SubProtocol(); subProtocol != "" {
		subProtocols = []string{subProtocol}
	}

	upstream, _, err := r.Dialer.Dial(
		ctx,
		r.URL.String(),
		websocket.SetDialerHeaders(
			websocket.CloneRequestHeadersForProxy(req.Header),
		),
		websocket.SetDialerSubProtocols(subProtocols),
	)
	if err != nil {
		r.getLogger().Warn("failed to dial upstream websocket server", "error", err)
		if err := downstream.Close(websocket.StatusInternalServerError, "failed to dial upstream"); err != nil {
			// Oh dear... we failed to close the downstream connection after failing
			// to dial the upstream. Not much to do about it other than log it.
			r.getLogger().Warn("failed to close downstream connection after upstream dial failure", "error", err)
		}

		return nil, err
	}

	conn := &components{
		Reader:            Tee(upstream, downstream),
		Writer:            downstream,
		Closer:            MultiCloser{upstream, downstream},
		ErrorChecker:      MultiErrorChecker{upstream, downstream},
		SubProtoRetriever: downstream,
	}

	// Spawn a goroutine to continuously read from the upstream and forward to
	// the downstream.
	go func() {
		bridge := &components{
			Reader:       Tee(downstream, upstream),
			ErrorChecker: MultiErrorChecker{upstream, downstream},
		}

		defer func() {
			_ = upstream.Close(websocket.StatusNormalClosure, "closing")

			// For the sake of consistency we'll also close the downstream here as
			// well, though it's almost surely the case that this would already
			// be occurring.
			_ = downstream.Close(websocket.StatusNormalClosure, "closing")
		}()

		err := ReadAllMessages(ctx, bridge)
		if err != nil {
			r.getLogger().Info("error encountered bridging websocket connections", "error", err)
		}
	}()

	return conn, nil
}

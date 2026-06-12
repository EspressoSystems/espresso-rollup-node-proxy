package http

import (
	"net/http"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket/websocketutil"

	"github.com/ethereum/go-ethereum/log"
)

// websocketUpgrader is a simple HTTP handler that upgrades incoming HTTP
// requests into WebSocket connections, and consumes all messages until
// the connection errors or closes, without explicitly handling the
// messages.
type websocketUpgrader struct {
	logger   log.Logger
	upgrader websocket.Upgrader
	options  []websocket.UpgradeOption
}

// ServeHTTP implements [http.Handler].
//
// This implementation performs a WebSocket Upgrader, and repeatidely
// reads all messages until an error is encountered, or the connection
// is closed.
//
// NOTE: This does not directly handle any of the messages being read.  But
// the act of reading them could trigger other middlewares to process the
// messages as needed.
func (u *websocketUpgrader) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	conn, err := u.upgrader.Upgrade(w, r, u.options...)
	if err != nil {
		u.logger.Debug("failed to upgrade connection to websocket", "error", err)
		return
	}

	// Ensure the connection is shutdown when we're done.
	defer func() {
		if err := conn.Close(websocket.StatusNormalClosure, "closing"); err != nil {
			if _, ok := conn.IsCloseError(err); ok {
				// Already closed
				return
			}

			u.logger.Warn("failed to close websocket connection", "error", err)
		}
	}()

	// We want to read messages until the user is done.
	if err := websocketutil.ReadAllMessages(ctx, conn); err != nil {
		u.logger.Warn("reading all messages from websocket failed", "error", err)
	}
}

// WebSocketUpgrader is a helper function for creating a new
// [websocket.Upgrader] that will automatically attempt to upgrade any
// incoming request into websocket connection, and consume every message by
// reading until the connection errors, or closes.
func WebSocketUpgrader(logger log.Logger, upgrader websocket.Upgrader, options ...websocket.UpgradeOption) http.Handler {
	return &websocketUpgrader{
		logger:   logger,
		upgrader: upgrader,
		options:  options,
	}
}

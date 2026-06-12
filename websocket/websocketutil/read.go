package websocketutil

import (
	"context"
	"fmt"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket"
)

// ReadErrorChecker is an interface that combines the [websocket.Reader] and
// [websocket.ErrorChecker] interfaces, which are both required for the
// [ReadAllMessages] function.
type ReadErrorChecker interface {
	websocket.Reader
	websocket.ErrorChecker
}

// ReadAllMessages will continuously read messages from the provided
// connection until the connection is closed.
//
// The messages themselves are not handled or inspected directly in
// any way.
//
// But they would allow for middlewares of the [websocket.Conn] to
// potentiall process with whatever side-effects they have.
func ReadAllMessages(ctx context.Context, conn ReadErrorChecker) error {
	for {
		select {
		default:
		case <-ctx.Done():
			// If our context is cancelled, we should exit this loop and stop
			// reading messages
			return fmt.Errorf("context reported done: %w", ctx.Err())
		}

		// We haven't been cancelled, sick, let's continue.

		_, _, err := conn.Read(ctx)
		if _, ok := conn.IsCloseError(err); ok {
			// Our connection is closed, let's exit the loop
			return nil
		}

		if err != nil {
			return fmt.Errorf("read request on websocket failed with error: %w", err)
		}
	}
}

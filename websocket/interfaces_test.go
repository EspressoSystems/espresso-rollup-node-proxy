package websocket_test

import (
	"context"
	"fmt"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/websocket/websockettest"
)

// Example_echoServer demonstrates how to use the websocket package to
// create a simple echo server that echoes back any messages it receives from
// clients.
func Example_echoServer() {
	ctx := context.Background()
	server := websockettest.NewServer(
		websocket.GorillaUpgrader(),
		websockettest.WebSocketHandlerFunc(func(conn websocket.Conn, err error) {
			if err != nil {
				panic(err)
			}

			for {
				typ, msg, err := conn.Read(ctx)
				if err != nil {
					// Close the connection
					_ = conn.Close(websocket.StatusNormalClosure, "goodbye")
					return
				}

				fmt.Printf("echoing message of type %d: %s\n", typ, string(msg))
				if err := conn.Write(ctx, typ, msg); err != nil {

					// Close the connection
					_ = conn.Close(websocket.StatusNormalClosure, "goodbye")
					return
				}
			}
		}),
	)
	defer server.Close()

	dialer := websocket.GorillaDialer()

	conn, _, err := dialer.Dial(ctx, server.WSURL().String())
	if err != nil {
		panic(err)
	}

	_ = conn.Write(ctx, websocket.MessageTypeText, []byte("Hello!"))
	typ, msg, err := conn.Read(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("received message of type %d: %s\n", typ, string(msg))

	// Output: echoing message of type 1: Hello!
	// received message of type 1: Hello!
}

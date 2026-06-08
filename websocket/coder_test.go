package websocket_test

import (
	"testing"

	"proxy/websocket"
	"proxy/websocket/websockettest"
)

// TestCoderBasicSuite runs the basic suite of tests on the Adapters
// for the WebSocket implementations.
func TestCoderBasicSuite(t *testing.T) {
	suite := websockettest.NewBasicSuite(websocket.CoderUpgrader(), websocket.CoderDialer())
	newServer := websockettest.TestServerCreator
	t.Run("ServerClose", suite.CreateBasicServerCloseTest(newServer, websocket.StatusGoingAway, "goodbye"))
	t.Run("ClientClose", suite.CreateBasicClientCloseTest(newServer, websocket.StatusNormalClosure, "goodbye"))
	t.Run("ClientWrite", suite.CreateBasicWriteMessageTest(newServer, websocket.MessageTypeText, []byte("hello there")))
	t.Run("ClientRead", suite.CreateBasicReadMessageTest(newServer, websocket.MessageTypeText, []byte("hello there")))
}

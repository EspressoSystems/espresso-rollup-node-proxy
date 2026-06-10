package logutil

import (
	"github.com/ethereum/go-ethereum/log"
)

// DiscardLogger is a [log.Logger] that automatically discards all log
// messages.
//
// This is most useful for testing purposes.
var DiscardLogger log.Logger = log.NewLogger(log.DiscardHandler())

package websocketutil

import "proxy/websocket"

// MultiErrorChecker is a utility type that implements [websocket.ErrorChecker]
// by delegating to each [websocket.ErrorChecker] in the slice.
type MultiErrorChecker []websocket.ErrorChecker

// Compile time type assertion to ensure that MultiErrorChecker implements
// [websocket.ErrorChecker].
var _ websocket.ErrorChecker = (MultiErrorChecker)(nil)

// IsCloseError implements [websocket.ErrorChecker] by calling IsCloseError on
// each of the underlying [websocket.ErrorChecker]s, and returning the first
// one [websocket.CloseError] encountered when valid.
//
// If no [websocket.CloseError] is encountered from any of the underlying
// [websocket.ErrorChecker]s, this will return false, and an empty
// [websocket.CloseError].
func (m MultiErrorChecker) IsCloseError(err error) (websocket.CloseError, bool) {
	for _, c := range m {
		if closeError, ok := c.IsCloseError(err); ok {
			return closeError, true
		}
	}

	return websocket.CloseError{}, false
}

package websocketutil

import (
	"errors"

	"proxy/websocket"
)

// MultiCloser is a utility type that implements [websocket.Closer] by
// delegating to each [websocket.Closer] in the slice.
type MultiCloser []websocket.Closer

// Compile time type assertion to ensure that MultiCloser implements
// [websocket.Closer].
var _ websocket.Closer = (MultiCloser)(nil)

// Close implements [websocket.Closer] by calling Close on all of the
// underlying [websocket.Closer]s, returning all errors encountered
// via [errors.Join], so they can be inspected.
func (m MultiCloser) Close(status websocket.Status, reason string) error {
	errs := make([]error, 0, len(m))
	for _, c := range m {
		errs = append(errs, c.Close(status, reason))
	}
	return errors.Join(errs...)
}

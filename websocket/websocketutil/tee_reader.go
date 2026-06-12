package websocketutil

import (
	"context"
	"errors"

	"proxy/websocket"
)

// ReadCloser is a utility interface that combines [websocket.Reader],
// [websocket.Closer], and [websocket.ErrorChecker], which are all required
// for a TeeReader.
type ReadCloser interface {
	websocket.Reader
	websocket.Closer
	websocket.ErrorChecker
}

// WriteCloser is a utility interface that combines [websocket.Writer],
// [websocket.Closer], and [websocket.ErrorChecker], which are all required
// for a TeeReader.
type WriteCloser interface {
	websocket.Writer
	websocket.Closer
	websocket.ErrorChecker
}

// teeReader is a utility struct that implements
// [websocket.Reader] by reading from a [ReadCloser] and writing the read
// message to a [WriteCloser].
type teeReader struct {
	r ReadCloser
	w WriteCloser
}

// Tee creates a new [websocket.Reader] that reads from the provided
// provided [ReadCloser], and writes the read message to the provided
// [WriteCloser].
//
// Any [webocket.CloseError] encountered from either the [ReadCloser] or the
// [WriteCloser] will be forwarded to the other one as well via the
// the [websocket.Closer.Close] method.
//
// Both [websocket.CloseError] and any error returned from
// [websocket.Closer.Close] should be inspectable via [errors.Is], or
// [errors.As].
func Tee(reader ReadCloser, writer WriteCloser) websocket.Reader {
	return &teeReader{
		r: reader,
		w: writer,
	}
}

// Compile time type assertion to ensure that readToWriterForwarder implements
// [websocket.Reader]
var _ websocket.Reader = (*teeReader)(nil)

// Read implements [websocket.Reader].
//
// Any call to Read will read from the provided [ReadCloser], and forward the
// read to the [WriteCloser]. If a [websocket.CloseError] is encountered from
// either the [ReadCloser] or the [WriteCloser], the status and reason will
// be forwarded to the other one as well via the [websocket.Closer.Close]
// method, both error will be rectievable via [errors.Is], or [errors.As].
func (r *teeReader) Read(ctx context.Context) (messageType websocket.MessageType, message []byte, err error) {
	messageType, message, err = r.r.Read(ctx)
	if closeError, ok := r.r.IsCloseError(err); ok {
		return messageType, message, errors.Join(err, r.w.Close(closeError.Status, closeError.Reason))
	}

	if err != nil {
		return messageType, message, err
	}

	err = r.w.Write(ctx, messageType, message)

	if closeError, ok := r.w.IsCloseError(err); ok {
		return messageType, message, errors.Join(err, r.r.Close(closeError.Status, closeError.Reason))
	}

	return messageType, message, err
}

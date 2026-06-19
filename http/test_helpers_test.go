package http_test

// This file contains shared test utilities (types and sentinel errors) used
// across multiple test files in this package. It contains no test functions.

import "errors"

// closeError is a test utility type that implements [io.ReadCloser] and
// returns an error when [Close] is invoked.
type closeError struct{}

// Read implements [io.Reader]
func (closeError) Read(p []byte) (n int, err error) {
	return len(p), nil
}

// ErrCloseFailed is returned by closeError.Close to simulate a body-close failure.
var ErrCloseFailed = errors.New("close failed")

// Close implements [io.Closer]
func (closeError) Close() error {
	return ErrCloseFailed
}

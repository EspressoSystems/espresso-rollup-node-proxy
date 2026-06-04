package adapters

import "io"

// readCloser is a simple struct that implements [io.ReadCloser] by combining
// an [io.Reader] and an [io.Closer].
type readCloser struct {
	io.Reader
	io.Closer
}

// ReadCloser creates a new [io.ReadCloser] by combining the given [io.Reader]
// and [io.Closer].
func ReadCloser(reader io.Reader, closer io.Closer) io.ReadCloser {
	return readCloser{
		Reader: reader,
		Closer: closer,
	}
}

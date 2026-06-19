package http_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	proxyhttp "github.com/EspressoSystems/espresso-rollup-node-proxy/http"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/log/logutil"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// closeRecorder is a utility type that implements [io.ReadCloser] and
// records when the [Close] method is invoked.
type closeRecorder struct {
	closed bool
}

// Read implements [io.Reader]
func (closeRecorder) Read(p []byte) (n int, err error) {
	return len(p), nil
}

// Close implements [io.Closer]
func (r *closeRecorder) Close() error {
	r.closed = true
	return nil
}

// TestBodyAutoCloserClosesAsNormal tests that the AutoBodyCloserMiddleware
// correctly closes the request body when the handler returns normally.
func TestBodyAutoCloserClosesAsNormal(t *testing.T) {
	require := require.New(t)
	resp := httptest.NewRecorder()
	captureLogger := logutil.NewCaptureLogger(nil)
	closer := &closeRecorder{}
	handler := proxyhttp.AutoBodyCloserMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Do nothing
		}),
		log.NewLogger(captureLogger),
	)

	handler.ServeHTTP(resp, &http.Request{
		Header: http.Header{},
		Body:   closer,
	})

	require.True(closer.closed, "expected body to be closed after request is processed")
	require.Empty(captureLogger.Entries, "expected no log entries since there should be no error when closing the body")
}

// TestBodyAutoCloserClosesWhenHandlerPanics tests that the
// AutoBodyCloserMiddleware correctly closes the request body when the
// handler panics, ensuring that the middleware always closes the body.
func TestBodyAutoCloserClosesWhenHandlerPanics(t *testing.T) {
	require := require.New(t)
	resp := httptest.NewRecorder()
	captureLogger := logutil.NewCaptureLogger(nil)
	closer := &closeRecorder{}
	handler := proxyhttp.AutoBodyCloserMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("oh no")
		}),
		log.NewLogger(captureLogger),
	)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			// Add a Recover here to prevent the panic from crashing the test, since we're only
			_ = recover()
		}()

		handler.ServeHTTP(resp, &http.Request{
			Header: http.Header{},
			Body:   closer,
		})
	}()

	wg.Wait()

	require.True(closer.closed, "expected body to be closed after request is processed")
	require.Empty(captureLogger.Entries, "expected no log entries since there should be no error when closing the body")
}

// TestBodyAutoCloserLogsCloseErrors tests that the AutoBodyCloserMiddleware
// emits a log if the Close call on the request's Body returns an error.
func TestBodyAutoCloserLogsCloseErrors(t *testing.T) {
	require := require.New(t)
	resp := httptest.NewRecorder()
	captureLogger := logutil.NewCaptureLogger(nil)
	closer := &closeError{}
	handler := proxyhttp.AutoBodyCloserMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Do nothing
		}),
		log.NewLogger(captureLogger),
	)

	handler.ServeHTTP(resp, &http.Request{
		Header: http.Header{},
		Body:   closer,
	})

	// We want to ensure that a log even did happen, we don't particularly
	// care what its contents are.
	require.NotEmpty(captureLogger.Entries, "expected log entries since there should be no error when closing the body")
}

// TestBodyAutoCloserWithNilBody tests that the AutoBodyCloserMiddleware
// correctly does not attempt to invoke [io.Closer.Close] on a nil Body
// in [http.Request].
//
// The way this test achieves the assertion is by testing if the program
// panics.  If the Body on the [http.Request] had its Close method invoked
// it would be a nil exception, and would cause a panic.
//
// So the test is to verify that a call to recover returns nil.
func TestBodyAutoCloserWithNilBody(t *testing.T) {
	require := require.New(t)
	defer func() {
		require.Nil(recover(), "no call should be being performed on a missing body")
	}()

	resp := httptest.NewRecorder()
	captureLogger := logutil.NewCaptureLogger(nil)
	handler := proxyhttp.AutoBodyCloserMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "OK", http.StatusOK)
		}),
		log.NewLogger(captureLogger),
	)

	handler.ServeHTTP(resp, &http.Request{
		Header: http.Header{},
	})

	// We want to ensure that a log even did happen, we don't particularly
	// care what its contents are.
	require.Empty(captureLogger.Entries, "expected log entries since there should be no error when closing the body")
	result := resp.Result()
	require.Equal(http.StatusOK, result.StatusCode)
}

package http_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	proxyhttp "github.com/EspressoSystems/espresso-rollup-node-proxy/http"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/log/logutil"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// TestRequestLoggerRecordsALog tests that the RequestLoggingMiddleware
// correctly records a log entry without any issue.
func TestRequestLoggerRecordsALog(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	captureLogger := logutil.NewCaptureLogger(nil)
	handler := proxyhttp.RequestLoggingMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "ok", http.StatusOK)
		}),
		log.NewLogger(captureLogger),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method:     http.MethodPost,
		RemoteAddr: "127.0.0.1:12345",
		URL: &url.URL{
			Path: "/test",
		},
	})

	require.Len(captureLogger.Entries, 1, "expected a single log entry")
}

// TestRequestLoggerDoesNotRecordOnPanic tests that
// [proxyhttp.RequestLoggingMiddleware] won't actually record a log when
// the handler panics.
//
// This test verifies that a panic prvents the logging action from
// occuring directly.
//
// NOTE: This matches the current behavior of the handler, should we decide
// that this is not the desired behavior, then we can adjust the logging
// action to be performed in a defer call instead.
func TestRequestLoggerDoesNotRecordOnPanic(t *testing.T) {
	require := require.New(t)
	captureLogger := logutil.NewCaptureLogger(nil)
	defer func() {
		_ = recover()
	}()
	defer func() {
		require.Len(captureLogger.Entries, 0, "expected no entries from a panic")
	}()
	recorder := httptest.NewRecorder()
	handler := proxyhttp.RequestLoggingMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("oh no!")
		}),
		log.NewLogger(captureLogger),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method:     http.MethodPost,
		RemoteAddr: "127.0.0.1:12345",
		URL: &url.URL{
			Path: "/test",
		},
	})
}

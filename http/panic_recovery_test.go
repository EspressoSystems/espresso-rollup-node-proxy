package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	proxyhttp "github.com/EspressoSystems/espresso-rollup-node-proxy/http"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/log/logutil"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// TestRecoveryNoPanic tests that the RecoveryMiddleware correctly allows
// for non panicing requests to continue without interference.
//
// This is the expected mode of ooperation.  If nothing panics, there's
// nothing to effectively do.
func TestRecoveryNoPanic(t *testing.T) {
	require := require.New(t)

	captureLogger := logutil.NewCaptureLogger(nil)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.RecoveryMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "OK", http.StatusOK)
		}),
		log.NewLogger(captureLogger),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodPost,
	})

	result := recorder.Result()
	require.Equal(http.StatusOK, result.StatusCode)
	require.Len(captureLogger.Entries, 0, "expected no log entries since there should be no panic")
}

// TestRecoveryPanic tests that when the underlying handler panics, that
// the [proxyhttp.RecoveryMiddleware] correctly recovers from the panic.
//
// The way this is fulfilled is by utilizing a handler that just panics.
// We want to see that the response is an internal server error, and that
// there is a log entry as a result of the panic.
func TestRecoveryPanic(t *testing.T) {
	require := require.New(t)

	captureLogger := logutil.NewCaptureLogger(nil)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.RecoveryMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("oh no")
		}),
		log.NewLogger(captureLogger),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodPost,
	})

	result := recorder.Result()
	require.Equal(http.StatusInternalServerError, result.StatusCode)
	require.Len(captureLogger.Entries, 1, "expected no log entries since there should be no panic")
}

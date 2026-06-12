package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	proxyhttp "proxy/http"

	"github.com/stretchr/testify/require"
)

// TestMethodIsMethodMatches is a test that verifies the behavior of the
// [proxyhttp.MethodIsMiddleware] when the [http.Request] method matches
// the expected restriction.
//
// This test verifies that the Middleware successfully passes the request
// to the handler when the Method matches the filter.
func TestMethodIsMethodMatches(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.MethodIsMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "OK", http.StatusOK)
		}),
		http.MethodPost,
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodPost,
	})

	result := recorder.Result()
	require.Equal(http.StatusOK, result.StatusCode)
}

// TestMethodIsMethodDoesNotMatch is a test that verifies the behavior of the
// [proxyhttp.MethodIsMiddleware] when the [http.Request] method does not
// match the expected restriction.
//
// This test verifies that the [http.Response] is popualted with the
// status code indicating that the method is not allowed  should the HTTP
// Method not match the expectation.
func TestMethodIsMethodDoesNotMatch(t *testing.T) {
	require := require.New(t)

	methods := []string{
		http.MethodGet,
		http.MethodPut,
		http.MethodDelete,
		http.MethodPatch,
		http.MethodOptions,
		http.MethodHead,
		"Custom",
	}

	for _, method := range methods {
		recorder := httptest.NewRecorder()
		handler := proxyhttp.MethodIsMiddleware(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "OK", http.StatusOK)
			}),
			http.MethodPost,
		)

		handler.ServeHTTP(recorder, &http.Request{
			Method: method,
		})

		result := recorder.Result()
		require.Equal(http.StatusMethodNotAllowed, result.StatusCode)
	}
}

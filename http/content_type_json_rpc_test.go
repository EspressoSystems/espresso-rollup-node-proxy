package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	proxyhttp "github.com/EspressoSystems/espresso-rollup-node-proxy/http"

	"github.com/stretchr/testify/require"
)

// TestContentTypeJSONRPCValidContentTypes is a test that
// ensures the correct behavior of allowing requests with acceptable
// Content-Type header formats through.
func TestContentTypeJSONRPCValidContentTypes(t *testing.T) {
	require := require.New(t)

	acceptedContentTypes := []string{
		"application/json",
		"application/json; charset=utf-8",
		"application/json; charset=UTF-8",

		// Older JSON RPC 1.0 formats that may be utilized
		"application/json-rpc",
		"application/json-rpc; charset=utf-8",
		"application/json-rpc; charset=UTF-8",
		"application/jsonrequest",
		"application/jsonrequest; charset=utf-8",
		"application/jsonrequest; charset=UTF-8",
	}

	for _, contentType := range acceptedContentTypes {
		recorder := httptest.NewRecorder()
		handler := proxyhttp.ContentTypeIsJSONRPCMiddleware(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "OK", http.StatusOK)
			}),
		)

		handler.ServeHTTP(recorder, &http.Request{
			Header: http.Header{
				"Content-Type": []string{contentType},
			},
		})

		result := recorder.Result()
		require.Equal(http.StatusOK, result.StatusCode)
	}
}

// TestContentTypeJSONRPCNoContentType is a test that ensures the correct
// behavior of rejecting a request when its Content-Type is an invalid
// MIME type.
//
// This test ensures that invalid Content-Types return the status
// code of unsupported media type when provided something that
// does not conform to MIME type standards.
func TestContentTypeJSONRPCInvalidContentType(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.ContentTypeIsJSONRPCMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "OK", http.StatusOK)
		}),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Header: http.Header{
			"Content-Type": []string{"invalid mime type"},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusUnsupportedMediaType, result.StatusCode)
}

// TestContentTypeJSONRPCUnsupportedContentType is a test that ensures the
// correct behavior of rejecting a request when its Content-Type is not
// supported.
//
// This test ensures that unsupported Content-Types return the status
// code of unsupported media type when provided something like text/plain.
func TestContentTypeJSONRPCUnsupportedContentType(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.ContentTypeIsJSONRPCMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "OK", http.StatusOK)
		}),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Header: http.Header{
			"Content-Type": []string{"text/plain"},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusUnsupportedMediaType, result.StatusCode)
}

// TestContentTypeJSONRPCNoContentType is a test that ensures the correct
// behavior of rjecting a request when the Content-Type header is not
// present.
//
// This test ensures that a request lacking a 'Content-Type' header will
// return the appropriate unsupported media type status code, since the
// middleware is unable to validate the content type of the request,
// and thus must reject it.
func TestContentTypeJSONRPCNoContentType(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.ContentTypeIsJSONRPCMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "OK", http.StatusOK)
		}),
	)

	handler.ServeHTTP(recorder, &http.Request{})

	result := recorder.Result()
	require.Equal(http.StatusUnsupportedMediaType, result.StatusCode)
}

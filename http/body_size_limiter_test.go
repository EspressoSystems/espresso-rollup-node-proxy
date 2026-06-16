package http_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	proxyhttp "github.com/EspressoSystems/espresso-rollup-node-proxy/http"
	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"

	"github.com/stretchr/testify/require"
)

// TestBodySizeLimiterAllowsContentLengthUnderLimit tests that the
// middleware does not prevent the request from going through if the
// ther circumstances allow for it to continue.
//
// This test demonstrates a hypothetical scenario where a request
// is made against a JSON-RPC 2,0 protocol endpoint where the size
// of the body of the request is less than the limit.
func TestBodySizeLimiterAllowsContentLengthUnderLimit(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.RequestBodySizeLimiterMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			enc := json.NewEncoder(w)
			require.NoError(enc.Encode(jsonrpcv2.Response{
				Result: "ok",
			}))
		}),
		10,
	)

	handler.ServeHTTP(recorder, &http.Request{})

	resp := recorder.Result()
	require.Equal(http.StatusOK, resp.StatusCode, "expected status code to be ok, since this is a JSON-RPC error")

	dec := json.NewDecoder(resp.Body)
	var result jsonrpcv2.Response
	require.NoError(dec.Decode(&result))
	require.Nil(result.Error, "expected error to be set in the response")
}

// TestBodySizeLimiterDetectsContentLengthHeaderBeingOverLimit tests that the
// Content-Length header is inspected to ensure that the length of the
// content is not anticipated to exceed the specified limit.
//
// When the Content-Length header is set to a valid number, and that
// number exceeds the specified limit, we should should receive
// an HTTP transport response error indicating that the request entity
// is too large.
func TestBodySizeLimiterDetectsContentLengthHeaderBeingOverLimit(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.RequestBodySizeLimiterMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Do nothing
		}),
		10,
	)

	handler.ServeHTTP(recorder, &http.Request{
		Header: http.Header{
			"Content-Length": []string{"100"},
		},
	})

	resp := recorder.Result()
	require.Equal(http.StatusRequestEntityTooLarge, resp.StatusCode, "expected to be entity too large")
}

// TestBodySizeLimiterDetectsContentLengthHeaderIsInvalid tests that the
// Content-Length header value should be valid.
//
// If the Content-Length header is not set to a numeric value, we
// should received a Transport error response indicating that the
// length is required.
func TestBodySizeLimiterDetectsContentLengthHeaderIsInvalid(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.RequestBodySizeLimiterMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Do nothing
		}),
		10,
	)

	handler.ServeHTTP(recorder, &http.Request{
		Header: http.Header{
			"Content-Length": []string{"invalid"},
		},
	})

	resp := recorder.Result()
	require.Equal(http.StatusLengthRequired, resp.StatusCode, "expected status code to be unsupported media type")
}

// TestBodySizeLimiterDetectsRequestContentLengthBeingOverLimit tests that the
// ContentLength property on the [http.Request] is inspected and ensures that
// the ContentLength does not exceed or limit.
//
// If the ContentLength property on [http.Request] is populated to a value
// over the max size limit, we should recieve an HTTP Transport error
// indicating that the request entity is too large.
func TestBodySizeLimiterDetectsRequestContentLengthBeingOverLimit(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.RequestBodySizeLimiterMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Do nothing
		}),
		10,
	)

	handler.ServeHTTP(recorder, &http.Request{
		ContentLength: 100,
	})

	resp := recorder.Result()
	require.Equal(http.StatusRequestEntityTooLarge, resp.StatusCode, "expected to be entity too large")
}

// TestBodySizeLimiterErrorWhenBodyIsOverLimit is a test that verifies a
// specific edge-case.
//
// If the Request doesn't hjave the `Content-Length` specified, and it doesn't
// have the `ContentLength` length populated in the [http.Request], then
// the erorr will be encountered in the [http.Handler] itself when
// attempting to read the contents of the body of the request.
//
// This means tha the error returned will likely be encountered outside
// of the HTTP Transport.  This error will be encountered, potentially,
// in the protocol or service serving the request.
//
// This test simulates a JSON-RPC 2.0 protocol on top of the HTTP Transport
// to demonstrate where the error is expected to be encountered.
// The response in this case may not be seen in the form of the HTTP
// transport, but rather may be potrayed in the Protocol layer's language
// itself.
func TestBodySizeLimiterErrorWhenBodyIsOverLimit(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.RequestBodySizeLimiterMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request jsonrpcv2.Request
			dec := json.NewDecoder(r.Body)
			enc := json.NewEncoder(w)
			err := dec.Decode(&request)

			var maxBytesError *http.MaxBytesError
			require.ErrorAs(err, &maxBytesError, "expected error to be a MaxBytesError since the body should exceed the maximum size")

			require.NoError(enc.Encode(jsonrpcv2.Response{
				Error: &jsonrpcv2.Error{
					Code:    jsonrpcv2.CodeParseError,
					Message: err.Error(),
				},
			}))
		}),
		10,
	)

	rawBody := [100]byte{}
	for i := range len(rawBody) {
		// Make it appear as valid JSON parsing characters
		rawBody[i] = ' '
	}
	handler.ServeHTTP(recorder, &http.Request{
		Body: io.NopCloser(bytes.NewBuffer(rawBody[:])),
	})

	resp := recorder.Result()
	require.Equal(http.StatusOK, resp.StatusCode, "expected status code to be ok, since this is a JSON-RPC error")

	dec := json.NewDecoder(resp.Body)
	var result jsonrpcv2.Response
	require.NoError(dec.Decode(&result))
	require.NotNil(result.Error, "expected error to be set in the response")
}

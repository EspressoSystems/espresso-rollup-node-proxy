package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	proxyhttp "github.com/EspressoSystems/espresso-rollup-node-proxy/http"

	"github.com/stretchr/testify/require"
)

// NotInvoked is a simple http.Handler that fails the test if it is invoked.
type NotInvoked require.Assertions

// ServeHTTP implements [http.Handler]
func (n *NotInvoked) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*require.Assertions)(n).Fail("This Handle should not be invoked")
}

// GenericResponse is a simple http.Handler that just returns "OK" with a 200
// status code.
type GenericResponse int

const (
	OK GenericResponse = iota
)

// ServeHTTP implements [http.Handler]
func (r GenericResponse) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "OK", http.StatusOK)
}

// The tests within this file are concerned with the correct and appropriate
// application of the CORS middleware based on the definitions of CORS
// handling logc defined by the w3 group's specification.
//
// Please see https://www.w3.org/TR/2020/SPSD-cors-20200602/ for full
// context and reference.
//

// TestCORSMiddlewareSimpleRequestNoOrigin tests that the CORS Middleware
// doesn't add any headers for simple requests that don't match the
// CORS requirements.
//
// The criteria for this is that the handler should be
// invoked as expected if no "Origin" header is specified in the request.
func TestCORSMiddlewareSimpleRequestNoOrigin(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(OK)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodGet,
	})

	result := recorder.Result()
	require.Equal(http.StatusOK, result.StatusCode)
	// Expected CORS Header Values
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowOrigin)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewareSimpleRequestNoOptions tests that the CORS Middleware
// **DOES** add a Allow Origin header of wildcard when no allow list has
// been specified.
//
// The criteria for this is that the handler should be invoked as expected
// if a Request has the Origin header specified, but no restrictions on
// the allowlist for origins.
func TestCORSMiddlewareSimpleRequestNoOptions(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(OK)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodGet,
		Header: http.Header{
			proxyhttp.HeaderOrigin: []string{"https://example.com/"},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusOK, result.StatusCode)
	// Expected CORS Header Values
	require.Equal([]string{"*"}, result.Header[proxyhttp.HeaderAccessControlAllowOrigin])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewareSimpleRequestAllowCredentials tests that the CORS
// Middleware changes the Allow Origin header when configured to specify
// that credentials are allowed.
//
// The accepctance critiera for this test are such that the Allow Credentials
// head should be present and set to "true", and that the Allow Origin
// header should be set to the Origin header passed if it is not
// restricted by the configured allow list of origins.
func TestCORSMiddlewareSimpleRequestAllowCredentials(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		OK,
		proxyhttp.WithCORSAllowCredentials(true),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodGet,
		Header: http.Header{
			proxyhttp.HeaderOrigin: []string{"https://example.com/"},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusOK, result.StatusCode)
	// Expected CORS Header Values
	require.Equal([]string{"https://example.com/"}, result.Header[proxyhttp.HeaderAccessControlAllowOrigin])
	require.Equal([]string{"true"}, result.Header[proxyhttp.HeaderAccessControlAllowCredentials])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewareSimpleRequestAllowdOrigins tests that the CORS
// Middleware changes the Allow Origin header when configured to specify
// the specific Origin passed in if the origin is allowed explicitly
// by configuration.
//
// Acceptance criteria are that the Allow Origin header should be present
// and equal to the passed Origin header, if the Origin is allowed.
func TestCORSMiddlewareSimpleRequestAllowdOrigins(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		OK,
		proxyhttp.WithCORSAllowedOrigins([]string{"https://example.com/", "https://foo.bar/"}),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodGet,
		Header: http.Header{
			proxyhttp.HeaderOrigin: []string{"https://example.com/"},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusOK, result.StatusCode)
	// Expected CORS Header Values
	require.Equal([]string{"https://example.com/"}, result.Header[proxyhttp.HeaderAccessControlAllowOrigin])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewarePreflightNoOriginNotHandled tests that the CORS
// Middleware behaves as expected when provided with a Pre-Flight
// request with no Origin header.
//
// The Acceptance criteria are that the handler should **NOT** be invoked
// and that no CORS headers are set on the response when the request is
// deemed to be invalid.
func TestCORSMiddlewarePreflightNoOriginNotHandled(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		(*NotInvoked)(require),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodOptions,
	})

	result := recorder.Result()
	require.Equal(http.StatusNoContent, result.StatusCode)
	// Expected CORS Header Values
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowOrigin)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewarePreflightNoRequestMethodNotHandled tests that the CORS
// Middleware behaves as expected when provided with a Pre-Flight request
// with an Origin Header, but no Requested Method.
//
// The Acceptance criteria are that the handler should **NOT** be invoked
// and that no CORS headers are set on the response when the request is
// deemed to be invalid.
func TestCORSMiddlewarePreflightNoRequestMethodNotHandled(t *testing.T) {
	require := require.New(t)
	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		(*NotInvoked)(require),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodOptions,
		Header: http.Header{
			proxyhttp.HeaderOrigin: []string{"https://example.com/"},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusNoContent, result.StatusCode)
	// Expected CORS Header Values
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowOrigin)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewarePreflightInvalidMethodNotHandled tests that the CORS
// Middleware behaves as expected when provided with a Pre-Flight request with
// invalid HTTP Methods.
//
// The Acceptance criteria are that the handler should **NOT** be invoked
// and that no CORS headers are set on the response when the request is
// deemed to be invalid.
func TestCORSMiddlewarePreflightInvalidMethodNotHandled(t *testing.T) {
	require := require.New(t)

	invalidAllowMethodsRequestValues := []string{
		"",
		"<",
		"[",
		"\u0001",
		"\u0100",
		"GET,POST",
		"=",
		"?",
	}

	for _, invalidMethod := range invalidAllowMethodsRequestValues {

		recorder := httptest.NewRecorder()
		handler := proxyhttp.NewCORSMiddleware(
			(*NotInvoked)(require),
		)

		handler.ServeHTTP(recorder, &http.Request{
			Method: http.MethodOptions,
			Header: http.Header{
				proxyhttp.HeaderOrigin:                     []string{"https://example.com/"},
				proxyhttp.HeaderAccessControlRequestMethod: []string{invalidMethod},
			},
		})

		result := recorder.Result()
		require.Equal(http.StatusNoContent, result.StatusCode)
		// Expected CORS Header Values
		require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowOrigin)
		require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
		require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
		require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
	}
}

// TestCORSMiddlewarePreflightSimpleMethod tests that the CORS Middleware
// behaves as expected when provided with a Pre-Flight request with a simple
// HTTP Method request, in this case a "GET".
//
// The acceptance criteria for this test are that the Allow Origin should
// be populated to thw wildcard value (since no restrictions have been
// configured), and no other CORS headers should be set.
func TestCORSMiddlewarePreflightSimpleMethod(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		(*NotInvoked)(require),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodOptions,
		Header: http.Header{
			proxyhttp.HeaderOrigin:                     []string{"https://example.com/"},
			proxyhttp.HeaderAccessControlRequestMethod: []string{http.MethodGet},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusNoContent, result.StatusCode)
	// ExpectedkCORS Header Values
	require.Equal([]string{"*"}, result.Header[proxyhttp.HeaderAccessControlAllowOrigin])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewarePreflightCustomMethod tests that the CORS Middleware
// behaves as expected when provided with a Pre-Flight request with a simple
// custom HTTP Method request that is a valid request.
//
// The acceptance criteria for this test are that the Allow Origin should
// be populated with the wildcard value, and that the Allow methods
// should include the custom method when the method list is not restricted.
func TestCORSMiddlewarePreflightCustomMethod(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		(*NotInvoked)(require),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodOptions,
		Header: http.Header{
			proxyhttp.HeaderOrigin:                     []string{"https://example.com/"},
			proxyhttp.HeaderAccessControlRequestMethod: []string{"sample"},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusNoContent, result.StatusCode)
	// ExpectedkCORS Header Values
	require.Equal([]string{"*"}, result.Header[proxyhttp.HeaderAccessControlAllowOrigin])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.Equal([]string{"sample"}, result.Header[proxyhttp.HeaderAccessControlAllowMethods])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewarePreflightRequestMethodConnectNoOptions tests that the
// CORS Middleware behaves as expected when provided with a Pre-Flight
// request with a non-simple method, with no method restrictions configured.
//
// The acceptance criteria for this test are that the Allow Origin should
// be set to the wildcard, and that the Allow Method should specify the
// requested method.
func TestCORSMiddlewarePreflightRequestMethodConnectNoOptions(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		(*NotInvoked)(require),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodOptions,
		Header: http.Header{
			proxyhttp.HeaderOrigin:                     []string{"https://example.com/"},
			proxyhttp.HeaderAccessControlRequestMethod: []string{http.MethodConnect},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusNoContent, result.StatusCode)
	// ExpectedkCORS Header Values
	require.Equal([]string{"*"}, result.Header[proxyhttp.HeaderAccessControlAllowOrigin])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.Equal([]string{http.MethodConnect}, result.Header[proxyhttp.HeaderAccessControlAllowMethods])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewarePreflightRequestMethodConnectNotAllowed tests that the
// CORS Middleware behaves as expected when provided with a Pre-Flight request
// that is not explicitly allowed.
//
// The acceptance criteria for this test are that no CORS headers should
// **NOT** be set as this request is now in the configured allowed list
// of methods.
func TestCORSMiddlewarePreflightRequestMethodConnectNotAllowed(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		(*NotInvoked)(require),
		proxyhttp.WithCORSAllowedMethods([]string{
			http.MethodPatch,
		}),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodOptions,
		Header: http.Header{
			proxyhttp.HeaderOrigin:                     []string{"https://example.com/"},
			proxyhttp.HeaderAccessControlRequestMethod: []string{http.MethodConnect},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusNoContent, result.StatusCode)
	// ExpectedkCORS Header Values
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowOrigin)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewarePreflightRequestMethodConnectAllowed tests that the CORS
// Middleware behaves as expected when provided with a Pre-Flight request that
// is requesting a method that is in the allowed list.
//
// The acceptance criteria for this test are that the Allow Origin should be
// a wildcard, and that the allow methods should list all of the configured
// allowed methods.
func TestCORSMiddlewarePreflightRequestMethodConnectAllowed(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		(*NotInvoked)(require),
		proxyhttp.WithCORSAllowedMethods([]string{
			http.MethodGet, http.MethodPost, http.MethodHead, http.MethodConnect,
		}),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodOptions,
		Header: http.Header{
			proxyhttp.HeaderOrigin:                     []string{"https://example.com/"},
			proxyhttp.HeaderAccessControlRequestMethod: []string{http.MethodConnect},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusNoContent, result.StatusCode)
	// ExpectedkCORS Header Values
	require.Equal([]string{"*"}, result.Header[proxyhttp.HeaderAccessControlAllowOrigin])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.Equal([]string{"GET,POST,HEAD,CONNECT"}, result.Header[proxyhttp.HeaderAccessControlAllowMethods])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewarePreflightRequestHeaderNoOptions tests that the CORS
// Middleware behaves as expected when provided with a Pre-Flight request
// with a custom Header being requested, but no  restriction on the allowed
// headers having been configured.
//
// The acceptance criteria for this test are that the Allow Origin should be
// be a wildcard, and that the Allow Header should contain the Canonicalized
// version of the submitted header (which is expectred to be lowercase)
func TestCORSMiddlewarePreflightRequestHeaderNoOptions(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		(*NotInvoked)(require),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodOptions,
		Header: http.Header{
			proxyhttp.HeaderOrigin:                      []string{"https://example.com/"},
			proxyhttp.HeaderAccessControlRequestMethod:  []string{http.MethodGet},
			proxyhttp.HeaderAccessControlRequestHeaders: []string{"x-custom-header"},
		},
	})

	result := recorder.Result()
	require.Equal(http.StatusNoContent, result.StatusCode)
	// ExpectedkCORS Header Values
	require.Equal([]string{"*"}, result.Header[proxyhttp.HeaderAccessControlAllowOrigin])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.Equal([]string{"X-Custom-Header"}, result.Header[proxyhttp.HeaderAccessControlAllowHeaders])
}

// TestCORSMiddlewarePreflightRequestHeaderNotAllowed tests that the CORS
// Middleware behaves as expected when provided with a Pre-Flight request with
// a custom Header being requested, but the header is not in explicitly
// configured allowed list.
//
// The acceptance criteria for this test are that no CORS headers should be
// set as the request is considered invalid and not allowed.
func TestCORSMiddlewarePreflightRequestHeaderNotAllowed(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		(*NotInvoked)(require),
		proxyhttp.WithCORSAllowedHeaders([]string{"x-something-else"}),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodOptions,
		Header: http.Header{
			proxyhttp.HeaderOrigin:                      []string{"https://example.com/"},
			proxyhttp.HeaderAccessControlRequestMethod:  []string{http.MethodGet},
			proxyhttp.HeaderAccessControlRequestHeaders: []string{"x-custom-header"},
		},
	},
	)

	result := recorder.Result()
	require.Equal(http.StatusNoContent, result.StatusCode)
	// ExpectedkCORS Header Values
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowOrigin)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowHeaders)
}

// TestCORSMiddlewarePreflightRequestHeaderAllowed tests that the CORS
// Middleware behaves as expected when provided with a Pre-Flight request
// with a requested header that is in the explicitly configured allowed
// header list.
//
// The acceptance criteria for this test are that the Allow Origin should be
// a wild card, and that the Allow Headres should be the canoncialized
// list of headers being requested that are within the expplicitly configured
// allowed list of headers.
//
// NOTE: This does **NOT** respond with every explicitly allowed header.
func TestCORSMiddlewarePreflightRequestHeaderAllowed(t *testing.T) {
	require := require.New(t)

	recorder := httptest.NewRecorder()
	handler := proxyhttp.NewCORSMiddleware(
		(*NotInvoked)(require),
		proxyhttp.WithCORSAllowedHeaders([]string{"x-custom-header", "x-something-else"}),
	)

	handler.ServeHTTP(recorder, &http.Request{
		Method: http.MethodOptions,
		Header: http.Header{
			proxyhttp.HeaderOrigin:                      []string{"https://example.com/"},
			proxyhttp.HeaderAccessControlRequestMethod:  []string{http.MethodGet},
			proxyhttp.HeaderAccessControlRequestHeaders: []string{"x-custom-header"},
		},
	},
	)

	result := recorder.Result()
	require.Equal(http.StatusNoContent, result.StatusCode)
	// ExpectedkCORS Header Values
	require.Equal([]string{"*"}, result.Header[proxyhttp.HeaderAccessControlAllowOrigin])
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowCredentials)
	require.NotContains(result.Header, proxyhttp.HeaderAccessControlAllowMethods)
	require.Equal([]string{"X-Custom-Header"}, result.Header[proxyhttp.HeaderAccessControlAllowHeaders])
}

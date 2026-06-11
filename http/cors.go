package http

import (
	"net/http"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// httpCORSMiddleware is a middleware that handles CORS preflight requests
// and sets appropriate CORS headers in the response.
type httpCORSMiddleware struct {
	handler http.Handler

	allowedOrigins   []string
	allowedMethods   []string
	allowedHeaders   []string
	allowCredentials bool
}

// corsResponseHeaders is a type that wraps http.Header to provide methods
// for setting CORS-related headers in the response.
type corsResponseHeaders http.Header

// corsWildcard is a constant that can be used to allow all origins in CORS
// headers.
const (
	corsWildcard                        = "*"
	HeaderAccessControlAllowOrigin      = "Access-Control-Allow-Origin"
	HeaderAccessControlAllowCredentials = "Access-Control-Allow-Credentials"
	HeaderAccessControlExposeHeaders    = "Access-Control-Expose-Headers"
	HeaderAccessControlMaxAge           = "Access-Control-Max-Age"
	HeaderAccessControlAllowMethods     = "Access-Control-Allow-Methods"
	HeaderAccessControlAllowHeaders     = "Access-Control-Allow-Headers"
)

// AllowOrigin sets the Access-Control-Allow-Origin header for the response
// based on the value passed.
//
// Origin Header refrence:
// https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Origin
//
// You would filter the `Origin` specified here if it mattered. The
// Access-Control-Allow-Origin header is only allowed to specify a single
// origin, and not an allow-list of acceptable origins.
//
// You would Read the Origin here, or at the start of any request and
// ensure that it matches the allowable list.  If not, you would indicate
// that they are not allowed.
//
// There may be cases where the Origin Header is not set. Technically
// speaking, it **should** be an error if an `OPTIONS` request,
// or any other request comes in lacking an `Origin` header.
//
// Unless we want to specifically control which Origins are allowed to
// hit us, we can just allow all of them with either a wildcard, or by
// returning the same `Origin` they provided to us in the request.
//
// Reference: https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Access-Control-Allow-Origin
// Allows for any Origin to make the request
func (h corsResponseHeaders) AllowOrigin(origin string) {
	http.Header(h).Set(HeaderAccessControlAllowOrigin, origin)
}

// AllowMethods sets the Access-Control-Allow-Methods header for the response
// based on the values passed. The `GET`, `HEAD`, and `POST` methods are
// always allowed regardless of the response here, so it is best not to
// event include them or `OPTIONS`.  `OPTIONS` is irrelevant as this
// request is already in response to an `OPTIONS` request and is
// implicitly allowed as well.
//
// This is more meant to indicate whether extra `VERB`s are allowed to be
// utilized, such as `DELETE`, and `PUT`.
//
// Reference: https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Access-Control-Allow-Methods
func (h corsResponseHeaders) AllowMethods(methods ...string) {
	if len(methods) <= 0 {
		return
	}

	http.Header(h).Add(HeaderAccessControlAllowMethods, strings.Join(methods, ","))
}

// AllowHeaders sets the Access-Control-Allow-Headers header for the response
// to inform the user of t he headers that are allowed to be utilized when
// making requests.
//
// The Headers specified here are able to be utilized in the actual request.
// The Wildcard can be utilized here, but it does **NOT** include the
// Authorization header.  If the Authorization header is needed then it
// must be listed separately, explicitly.
//
// NOTE: This is required to be set in response to a request for headers.
//
// Reference: https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Access-Control-Allow-Headers
func (h corsResponseHeaders) AllowHeaders(headers ...string) {
	if len(headers) <= 0 {
		return
	}

	hs := make([]string, len(headers))

	for j, v := range headers {
		hs[j] = textproto.CanonicalMIMEHeaderKey(v)
	}
	http.Header(h).Add(HeaderAccessControlAllowHeaders, strings.Join(hs, ","))
}

// MaxAge sets the Access-Control-Max-Age header for the response to inform
// the requester how longer this response will be valid for, so that further
// preflight requests are not needed.
//
// NOTE: Different browsers have different limits on the value of this response
// regardless of what is specified here. Beneficial mileage may vary.
//
// Reference: https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Access-Control-Max-Age
func (h corsResponseHeaders) MaxAge(duration time.Duration) {
	seconds := strconv.FormatInt(int64(duration.Seconds()), 10)
	http.Header(h).Set("Access-Control-Max-Age", seconds)
}

// AllowCredentials sets the Access-Control-Allow-Credentials header for the
// response. It specifies whether general "credentials" are allowable. This
// can include cookies, and HTTP Authorization credentaisl, or even TLS
// client certificates.
//
// Reference: https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Access-Control-Allow-Credentials
func (h corsResponseHeaders) AllowCredentials(allow bool) {
	http.Header(h).Set(HeaderAccessControlAllowCredentials, strconv.FormatBool(allow))
}

const corsPolicyMaxAge = 10 * time.Minute

const (
	HeaderOrigin                      = "Origin"
	HeaderAccessControlRequestMethod  = "Access-Control-Request-Method"
	HeaderAccessControlRequestHeaders = "Access-Control-Request-Headers"
)

// corsRequestHeaders is a type that wraps http.Header to provide methods for
// retrieving CORS-related headers from the request.
type corsRequestHeaders http.Header

// Origin returns the value of the Origin header from the request, which
// specifies the origin of the request. This is used in CORS to determine
// whether or not the request is allowed based on its origin.
//
// Reference: https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Origin
func (h corsRequestHeaders) Origin() string {
	return http.Header(h).Get(HeaderOrigin)
}

// isSeparator is a helper function that checks if a rune is part of the
// set of HTTP RFC2616 separators.
//
// See RFC2616 secion 2.2 for specifics.
// - https://datatracker.ietf.org/doc/html/rfc2616/#section-2.2
func isSeparator(r rune) bool {
	switch r {
	default:
		return false

	case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/',
		'[', ']', '?', '=', '{', '}', ' ', '\t':
		return true
	}
}

// isTokenSet is a helper function that checks if a rune is part of the token
// set allowable via the HTTP RFC2616 specification. This is used to validate
// the values of the
//
// The list of supported methods is dictated by RFC2616 parsing rules.  In
// this case any valid token is acceptable.
// See RFC2616 secion 5.1.1 and section 2.2 for specifics.
// - https://datatracker.ietf.org/doc/html/rfc2616/#section-5.1.1
// - https://datatracker.ietf.org/doc/html/rfc2616/#section-2.2
func isTokenSet(r rune) bool {
	return r <= unicode.MaxASCII && !unicode.IsControl(r) && !isSeparator(r)
}

func isNotTokenSet(r rune) bool {
	return !isTokenSet(r)
}

// RequestMethod returns the value of the
// Access-Control-Request-Method header. This is the method that this specific
// preflight request is asking about. This is used in CORS preflight requests
// to inform the requester whether this method is allowed or not.
//
// Reference: https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Access-Control-Request-Method
func (h corsRequestHeaders) RequestMethod() string {
	value := http.Header(h).Get(HeaderAccessControlRequestMethod)

	method := strings.TrimSpace(value)

	// Do these methods match expected, and supported method types?
	switch method {
	default:
		if strings.ContainsFunc(method, isNotTokenSet) {
			return ""
		}
		return method

	case "":
		return ""

	case http.MethodOptions, http.MethodGet, http.MethodHead, http.MethodPost,
		http.MethodPut, http.MethodDelete, http.MethodTrace, http.MethodConnect:
		// These are explicitly defined HTTP methods.
		return method
	}
}

// RequestHeaders returns the values of the Access-Control-Request-Headers.
//
// This indicates a query from the requester over whether the headers specified
// are allowed or not.
//
// The values are meant to be sorted alphabetically, and are meant to be
// represented as lowercase. As a result, Canonicalization may need to occur.
//
// Reference: https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Access-Control-Request-Headers
func (h corsRequestHeaders) RequestHeaders() []string {
	value := http.Header(h).Get(HeaderAccessControlRequestHeaders)
	rawValues := strings.Split(value, ",")

	var parsedHeaders []string
	for _, rawValue := range rawValues {
		trimmed := strings.TrimSpace(rawValue)

		if trimmed == "" {
			// This is "technically" fine, but should not be included in the list
			// of headers.
			continue
		}

		if strings.ContainsFunc(trimmed, isNotTokenSet) {
			// Invalid value
			return nil
		}

		parsedHeaders = append(parsedHeaders, trimmed)
	}

	return parsedHeaders
}

// isOriginAllowed is a helper function that checks if the given origin is
// allowed based on the configuration of the CORS middleware.
func (m *httpCORSMiddleware) isOriginAllowed(origin string) bool {
	return len(m.allowedOrigins) == 0 || slices.Contains(m.allowedOrigins, origin)
}

// isMethodAllowed is a helper function that checks if the given method is
// allowed based on the configuration of the CORS middleware.
func (m *httpCORSMiddleware) isMethodAllowed(method string) bool {
	return isSimpleMethod(method) || len(m.allowedMethods) == 0 || slices.Contains(m.allowedMethods, method)
}

// isHeaderAllowed is a helper function that checks if the given header is
// allowed based on the configuration of the CORS middleware.
func (m *httpCORSMiddleware) isHeaderAllowed(header string) bool {
	// Case-insenitive match, so we need to lowercase the header before checking.
	return isSimpleHeader(header) || len(m.allowedHeaders) == 0 || slices.Contains(m.allowedHeaders, strings.ToLower(header))
}

// doesAllowCredentials is a helper function that checks if credentials are
// allowed.
func (m *httpCORSMiddleware) doesAllowCredentials() bool {
	return m.allowCredentials
}

// isSimpleMethod is a helper function that checks if the given method is
// considered a simple method or not.
//
// This definition comes from the w3 deocumentation:
// - https://www.w3.org/TR/2020/SPSD-cors-20200602/#terminology
func isSimpleMethod(method string) bool {
	switch method {
	default:
		return false
	case http.MethodGet, http.MethodHead, http.MethodPost:
		return true

	}
}

// isSimpleHeader is a helper function that checks if the given header is
// a simple header or not.
//
// This definition comes from the w3 deocumentation:
// - https://www.w3.org/TR/2020/SPSD-cors-20200602/#terminology
func isSimpleHeader(header string) bool {
	switch strings.ToLower(header) {
	default:
		return false
	case "cache-control", "content-language", "content-type", "expires", "last-modified", "pragma":
		return true

	}
}

// appendAllowOrigin is a helper function that appends the appropriate
// Header values based on configuration for the Access-Control-Allow-Origin
// header, and the allow credentials header if needed.
func (m *httpCORSMiddleware) appendAllowOrigin(responseHeaders corsResponseHeaders, origin string) {
	if m.doesAllowCredentials() {
		responseHeaders.AllowOrigin(origin)
		responseHeaders.AllowCredentials(true)
	} else if len(m.allowedOrigins) == 0 {
		responseHeaders.AllowOrigin(corsWildcard)
	} else {
		responseHeaders.AllowOrigin(origin)
	}
}

// processPreflightRequest is a helper function that handles CORS preflight
// requests. It sets the appropriate CORS headers in the response based on
// the request headers.
//
// This implementation is informed by the standards for handling CORS
// preflight requests from the w3 documentation.
// Reference:
// https://www.w3.org/TR/2020/SPSD-cors-20200602/#resource-preflight-requests
func (m *httpCORSMiddleware) processPreflightRequest(w http.ResponseWriter, r *http.Request) {
	requestHeaders := corsRequestHeaders(r.Header)

	if _, isOriginHeaderPresent := requestHeaders[HeaderOrigin]; !isOriginHeaderPresent {
		// Request is outside of the scope.
		return
	}

	origin := requestHeaders.Origin()
	if !m.isOriginAllowed(origin) {
		// No header match, we stop processing.
		return
	}

	requestedMethod := requestHeaders.RequestMethod()
	if _, requestMethodHeaderPresent := requestHeaders[HeaderAccessControlRequestMethod]; !requestMethodHeaderPresent || requestedMethod == "" {
		// No header present, or an invalid value supplied
		return
	}

	requestedHeaders := requestHeaders.RequestHeaders()
	if !m.isMethodAllowed(requestedMethod) {
		// Method is not allowed
		return
	}

	var nonSimpleHeaders []string
	for _, header := range requestedHeaders {
		if !m.isHeaderAllowed(header) {
			// Header is not allowed
			return
		}

		if !isSimpleHeader(header) {
			nonSimpleHeaders = append(nonSimpleHeaders, header)
		}
	}

	responseHeaders := corsResponseHeaders(w.Header())
	m.appendAllowOrigin(responseHeaders, origin)

	// Set a decent max age, so they don't need to preflight too often.
	responseHeaders.MaxAge(corsPolicyMaxAge)

	if !isSimpleMethod(requestedMethod) {
		if len(m.allowedMethods) <= 0 {
			responseHeaders.AllowMethods(requestedMethod)
		} else {
			responseHeaders.AllowMethods(strings.Join(m.allowedMethods, ","))
		}
	}

	if len(nonSimpleHeaders) > 0 {
		responseHeaders.AllowHeaders(nonSimpleHeaders...)
	}
}

// processSimpleCrossOriginRequest is a helper function that handles CORS for
// regular requests.
//
// This implementation is informed by the standards for handling CORS
// simple cross-origin request from the w3 documentation.
// Reference:
// https://www.w3.org/TR/2020/SPSD-cors-20200602/#resource-requests
func (m *httpCORSMiddleware) processSimpleCrossOriginRequest(w http.ResponseWriter, r *http.Request) {
	requestHeaders := corsRequestHeaders(r.Header)
	if _, hasOriginHeader := requestHeaders[HeaderOrigin]; !hasOriginHeader {
		// There's nothing to be done specifically for this request.
		// This falls out of scope.
		return
	}

	origin := requestHeaders.Origin()
	// Does the origin match against our case-sensitive list?
	if !m.isOriginAllowed(origin) {
		// do not set any additional headers, and terminal this process.
		return
	}

	responseHeaders := corsResponseHeaders(w.Header())
	m.appendAllowOrigin(responseHeaders, origin)

	// Exposed Headers?
}

// ServeHTTP implements [http.Handler]
func (m *httpCORSMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		m.processPreflightRequest(w, r)
		// Send a response indicating no content.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Handle other Methods with CORS
	m.processSimpleCrossOriginRequest(w, r)

	// Forward the request to the underlying handler.
	m.handler.ServeHTTP(w, r)
}

// CORSMiddlewareOption is a function type that defines an option for
// configuring the CORS Middleware implementation.
type CORSMiddlewareOption func(*httpCORSMiddleware)

// WithCORSAllowedMethods is a CORSMiddlewareOption that allows for the
// the user to specify the allowed methods for CORS requests.
func WithCORSAllowedMethods(methods []string) CORSMiddlewareOption {
	return func(m *httpCORSMiddleware) {
		m.allowedMethods = methods
	}
}

// WithCORSAllowedHeaders is a CORSMiddlewareOption that allows for the
// the user to pass the specified headers for CORS requests.
//
// NOTE: These headers are meant to be case-insensitive, and are meant to be
// represented as lowercase.  They will automatically be translated to
// lower-case for convenience.
func WithCORSAllowedHeaders(headers []string) CORSMiddlewareOption {
	return func(m *httpCORSMiddleware) {
		resolvedHeaders := make([]string, len(headers))
		for i, header := range headers {
			resolvedHeaders[i] = strings.ToLower(header)
		}
		m.allowedHeaders = resolvedHeaders
	}
}

// WithCORSAllowedOrigins is a CORSMiddlewareOption that allows for the
// the user to specify which Origins are allowed to make CORS
// requests.
//
// NOTE: These values **ARE** case sensitive.
func WithCORSAllowedOrigins(origins []string) CORSMiddlewareOption {
	return func(m *httpCORSMiddleware) {
		m.allowedOrigins = origins
	}
}

// WithCORSAllowCredentials is a CORSMiddlewareOption that allows for the
// the use to specify whether credentials are allowed to be passed or not.
func WithCORSAllowCredentials(allow bool) CORSMiddlewareOption {
	return func(m *httpCORSMiddleware) {
		m.allowCredentials = allow
	}
}

// NewCORSMiddleware creates an [http.Handler] that will perform all CORS
// interceptions and handling for the given [http.Handler], before forwarding
// the request to the given [http.Handler].
//
// Without any additiona configuration or Options passed in, the middleware
// will allow all origins, all methods, all headers, and will not allow
// credentials.
//
// NOTE: Exposed headers are not currently supported by this implementation,
// but could be added fairly easily if needed.
func NewCORSMiddleware(next http.Handler, options ...CORSMiddlewareOption) http.Handler {
	m := &httpCORSMiddleware{handler: next}
	for _, option := range options {
		option(m)
	}
	return m
}

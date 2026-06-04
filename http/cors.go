package http

import (
	"net/http"
	"net/textproto"
	"strconv"
	"time"
)

// httpCORSMiddleware is a middleware that handles CORS preflight requests
// and sets appropriate CORS headers in the response.
type httpCORSMiddleware struct {
	handler http.Handler
}

// corsResponseHeaders is a type that wraps http.Header to provide methods
// for setting CORS-related headers in the response.
type corsResponseHeaders http.Header

// corsWildcard is a constant that can be used to allow all origins in CORS
// headers.
const corsWildcard = "*"

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
	http.Header(h).Set("Access-Control-Allow-Origin", origin)
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
	for _, m := range methods {
		http.Header(h).Add("Access-Control-Allow-Methods", m)
	}
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
	for _, v := range headers {
		http.Header(h).Add("Access-Control-Allow-Headers", textproto.CanonicalMIMEHeaderKey(v))
	}
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
	http.Header(h).Set("Access-Control-Allow-Credentials", strconv.FormatBool(allow))
}

const corsPolicyMaxAge = 10 * time.Minute

// corsRequestHeaders is a type that wraps http.Header to provide methods for
// retrieving CORS-related headers from the request.
type corsRequestHeaders http.Header

// Origin returns the value of the Origin header from the request, which
// specifies the origin of the request. This is used in CORS to determine
// whether or not the request is allowed based on its origin.
//
// Reference: https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Origin
func (h corsRequestHeaders) Origin() string {
	return http.Header(h).Get("Origin")
}

// RequestMethod returns the value of the
// Access-Control-Request-Method header. This is the method that this specific
// preflight request is asking about. This is used in CORS preflight requests
// to inform the requester whether this method is allowed or not.
//
// Reference: https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Access-Control-Request-Method
func (h corsRequestHeaders) RequestMethod() string {
	return http.Header(h).Get("Access-Control-Request-Method")
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
	return http.Header(h).Values("Access-Control-Request-Headers")
}

// handleCORSOptionsMethod is a helper function that handles CORS preflight
// requests. It sets the appropriate CORS headers in the response based on
// the request headers.
func handleCORSOptionsMethod(w http.ResponseWriter, r *http.Request) {
	responseHeaders := corsResponseHeaders(w.Header())
	requestHeaders := corsRequestHeaders(r.Header)

	// Allow for All Origins to call us
	responseHeaders.AllowOrigin(corsWildcard)

	// Allows for the specific methods of `POST` for `JSONRPC V2` requests, and
	// `OPTIONS` for `CORS` requests
	// This is a required header for CORS.
	if requestedMethod := requestHeaders.RequestMethod(); requestedMethod != "" {
		// Technically, we don't need to specify this as its always allowed.
		// But we need to send **something**, and we don't want to allow whatever
		// they're requesting blankly as it may be a `DELETE` or a `PUT` or some
		// other custom method.
		responseHeaders.AllowMethods(http.MethodPost)
	}

	// Set a decent max age, so they don't need to preflight too often.
	responseHeaders.MaxAge(corsPolicyMaxAge)

	if requestedHeaders := requestHeaders.RequestHeaders(); len(requestedHeaders) > 0 {
		// Response if they request headers
		responseHeaders.AllowHeaders(requestedHeaders...)
	}

	// Specify that we allow Credentialed requests.
	// This is required if we want to allow cookies or want to forward
	// Authorization headers.
	responseHeaders.AllowCredentials(true)
}

// ServeHTTP implements [http.Handler]
func (m *httpCORSMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		handleCORSOptionsMethod(w, r)
		return
	}

	m.handler.ServeHTTP(w, r)
}

// NewCORSMiddleware creates an [http.Handler] that will perform all CORS
// interceptions and handling for the given [http.Handler], before forwarding
// the request to the given [http.Handler].
func NewCORSMiddleware(next http.Handler) http.Handler {
	return &httpCORSMiddleware{handler: next}
}

package main

import (
	"bytes"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

var verbose = flag.Bool("verbose", false, "log request and response bodies")

func loggingHandler(upstream string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !*verbose {
			next.ServeHTTP(w, r)
			return
		}

		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		log.Printf("-> %s %s\n%s", r.Method, upstream, body)

		rw := &responseRecorder{ResponseWriter: w, buf: &bytes.Buffer{}}
		next.ServeHTTP(rw, r)
		log.Printf("<- %d\n%s", rw.status, rw.buf)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	buf    *bytes.Buffer
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

func main() {
	upstream := flag.String("upstream", "", "upstream URL to proxy to (required)")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	if *upstream == "" {
		log.Fatal("--upstream is required")
	}

	target, err := url.Parse(*upstream)
	if err != nil {
		log.Fatalf("invalid upstream URL %q: %v", *upstream, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(r *http.Request) {
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		r.Host = target.Host
	}

	log.Printf("fullnode-proxy: %s -> %s", *addr, *upstream)
	log.Fatal(http.ListenAndServe(*addr, loggingHandler(*upstream, proxy)))
}

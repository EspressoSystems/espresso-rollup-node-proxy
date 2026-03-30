package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

type MockEngine struct {
	interceptor  *Interceptor
	reverseProxy *httputil.ReverseProxy
}

func NewMockEngine(upstream string, jwtSecret []byte) *MockEngine {
	target, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("invalid upstream URL: %v", err)
	}

	m := &MockEngine{
		interceptor: &Interceptor{
			maliciousBlockHashes: make(map[string]string),
			allowMaliciousBlock:  false,
			jwtSecret:            jwtSecret,
			upstreamAddress:      upstream,
			maliciousBlockNum:    0,
		},
	}

	m.reverseProxy = &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
			req.Out.Host = target.Host
			token, err := m.interceptor.getJwt()
			if err == nil {
				req.Out.Header.Set("Authorization", "Bearer "+token)
			}

		},
	}

	return m
}

func (m *MockEngine) Serve(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		log.Printf("failed to read request body: %v", err)
		http.Error(writer, "failed to read request body", http.StatusBadRequest)
		return
	}

	body, modifiedBody, err := m.interceptor.Intercept(body)
	if err != nil {
		log.Printf("failed to intercept request: %v", err)
		http.Error(writer, "failed to intercept request", http.StatusInternalServerError)
		return
	}

	if modifiedBody != nil {
		writer.Header().Set("Content-Type", "application/json")
		writer.Write(modifiedBody)
		return
	}

	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	m.reverseProxy.ServeHTTP(writer, request)
}

func main() {
	upstream := flag.String("upstream", "", "upstream engine URL (required)")
	addr := flag.String("addr", ":8080", "listen address")
	jwtFile := flag.String("jwt-secret", "", "path to JWT secret file (hex-encoded)")
	flag.Parse()

	if *upstream == "" {
		log.Fatal("--upstream is required")
	}
	if *jwtFile == "" {
		log.Fatal("--jwt-secret is required")
	}

	raw, err := os.ReadFile(*jwtFile)
	if err != nil {
		log.Fatalf("failed to read JWT secret: %v", err)
	}
	jwtSecret, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(string(raw), "0x")))
	if err != nil {
		log.Fatalf("failed to decode JWT secret: %v", err)
	}

	engine := NewMockEngine(*upstream, jwtSecret)

	// Use mux to handle our custom endpoint `create-malicious-block`
	mux := http.NewServeMux()

	// Custom endpoint we dont want to forward these to upstream, handle request here
	mux.HandleFunc("/create-malicious-block", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		type Request struct {
			BlockNumber uint64 `json:"blockNumber"`
		}

		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BlockNumber <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "Invalid JSON or missing/invalid blockNumber",
			})
			return
		}

		engine.interceptor.mu.Lock()
		engine.interceptor.allowMaliciousBlock = true
		engine.interceptor.maliciousBlockNum = req.BlockNumber
		engine.interceptor.mu.Unlock()

		log.Println("setting malicious block number", "num", req.BlockNumber)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message":     "Malicious block configured",
			"blockNumber": req.BlockNumber,
		})
	})

	// All other paths go to upstream
	mux.HandleFunc("/", engine.Serve)

	log.Printf(" %s -> %s", *addr, *upstream)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

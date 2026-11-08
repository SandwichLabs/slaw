//go:build integration

package icebergtest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// ProxyOptions picks what a Proxy does to the traffic it forwards.
type ProxyOptions struct {
	// RequireBearer answers 401 to any request without
	// "Authorization: Bearer <BearerToken>".
	RequireBearer bool
	// Vended, when not nil, is merged into the "config" map of every LoadTable
	// response (GET .../namespaces/<ns>/tables/<t>), the way a catalog vends
	// storage credentials.
	Vended map[string]string
	// HangLoadTable makes every LoadTable request (GET .../tables/<t>) hang until
	// the request is abandoned or the test ends, the way a stalled catalog does.
	// Attach and namespace listings still answer.
	HangLoadTable bool
}

// Proxy is an in-test HTTP reverse proxy in front of the REST catalog.
type Proxy struct {
	// URL is the proxy's base URL; attach the catalog through it.
	URL string

	mu   sync.Mutex
	seen []string
}

// Requests returns "METHOD path" for every request the proxy received.
func (p *Proxy) Requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

var loadTablePath = regexp.MustCompile(`^/v1/(?:[^/]+/)?namespaces/[^/]+/tables/[^/]+$`)

// VendedConfig is the config a catalog would vend for the fixture's MinIO.
func (f *Fixture) VendedConfig() map[string]string {
	return map[string]string{
		"s3.access-key-id":     f.KeyID,
		"s3.secret-access-key": f.Secret,
		"s3.endpoint":          "http://" + f.S3Endpoint,
		"s3.region":            "us-east-1",
		"s3.path-style-access": "true",
	}
}

// Proxy starts a reverse proxy to the catalog, closed when the test ends.
func (f *Fixture) Proxy(t testing.TB, opts ProxyOptions) *Proxy {
	t.Helper()
	target, err := url.Parse(f.RESTURI)
	require.NoError(t, err)
	p := &Proxy{}
	done := make(chan struct{})
	rp := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Header.Del("Accept-Encoding") // so a modified body is never compressed
	}}
	rp.ModifyResponse = func(resp *http.Response) error {
		if opts.Vended == nil || resp.Request.Method != http.MethodGet ||
			!loadTablePath.MatchString(resp.Request.URL.Path) || resp.StatusCode != http.StatusOK {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			return err
		}
		cfg, _ := doc["config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
		}
		for k, v := range opts.Vended {
			cfg[k] = v
		}
		doc["config"] = cfg
		body, err = json.Marshal(doc)
		if err != nil {
			return err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		return nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.seen = append(p.seen, r.Method+" "+r.URL.Path)
		p.mu.Unlock()
		if opts.HangLoadTable && r.Method == http.MethodGet && loadTablePath.MatchString(r.URL.Path) {
			select {
			case <-r.Context().Done():
			case <-done:
			}
			return
		}
		if opts.RequireBearer && r.Header.Get("Authorization") != "Bearer "+BearerToken {
			http.Error(w, `{"error":{"message":"unauthorized","type":"NotAuthorizedException","code":401}}`, http.StatusUnauthorized)
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(done) }) // runs first: releases hung handlers so Close can return
	p.URL = srv.URL
	return p
}

// LogServer is an HTTP file server over a directory that records every request,
// for asserting that a hostile table never made the process fetch a URL.
type LogServer struct {
	// URL is the server's base URL (no trailing slash).
	URL string
	// Dir is the directory it serves.
	Dir string

	mu   sync.Mutex
	seen []string
}

// NewLogServer serves dir on a loopback port until the test ends.
func NewLogServer(t testing.TB, dir string) *LogServer {
	t.Helper()
	s := &LogServer{Dir: dir}
	fs := http.FileServer(http.Dir(dir))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		fs.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// Requests returns "METHOD path" for every request received so far.
func (s *LogServer) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

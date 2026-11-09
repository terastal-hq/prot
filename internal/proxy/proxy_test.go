package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestProxyHTTPRouting(t *testing.T) {
	// Upstream 1
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "one")
		_, _ = w.Write([]byte("response from upstream 1"))
	}))
	defer ts1.Close()

	// Upstream 2
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "two")
		_, _ = w.Write([]byte("response from upstream 2"))
	}))
	defer ts2.Close()

	router := NewRouter()
	if err := router.SetRoute("api", ts1.URL, false); err != nil {
		t.Fatalf("failed to set route api: %v", err)
	}
	if err := router.SetRoute("web", ts2.URL, false); err != nil {
		t.Fatalf("failed to set route web: %v", err)
	}

	proxySrv := NewServer(router, ServerOptions{ListenAddr: "127.0.0.1:0"})
	if err := proxySrv.Start(); err != nil {
		t.Fatalf("failed to start proxy: %v", err)
	}
	defer func() { _ = proxySrv.Close(context.Background()) }()

	proxyAddr := proxySrv.Addr()

	// Helper to send request with specific Host header
	doReq := func(host string) (int, string, string) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+proxyAddr+"/test", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("req failed: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("X-Upstream"), string(body)
	}

	// Test api.localhost
	code, up, body := doReq("api.localhost")
	if code != 200 || up != "one" || body != "response from upstream 1" {
		t.Errorf("api.localhost failed: code=%d, up=%s, body=%s", code, up, body)
	}

	// Test web.localhost with port
	code, up, body = doReq("web.localhost:8080")
	if code != 200 || up != "two" || body != "response from upstream 2" {
		t.Errorf("web.localhost failed: code=%d, up=%s, body=%s", code, up, body)
	}

	// Test route not found
	code, _, _ = doReq("unknown.localhost")
	if code != http.StatusNotFound {
		t.Errorf("unknown route expected 404, got %d", code)
	}
}

func TestDynamicPortUpdate(t *testing.T) {
	// Simulate port changing underneath (e.g. dev server restarted on new port)
	tsOld := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("version 1"))
	}))
	defer tsOld.Close()

	tsNew := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("version 2"))
	}))
	defer tsNew.Close()

	router := NewRouter()
	_ = router.SetRoute("app", tsOld.URL, false)

	proxySrv := NewServer(router, ServerOptions{ListenAddr: "127.0.0.1:0"})
	_ = proxySrv.Start()
	defer func() { _ = proxySrv.Close(context.Background()) }()

	proxyAddr := proxySrv.Addr()

	req, _ := http.NewRequest(http.MethodGet, "http://"+proxyAddr+"/", nil)
	req.Host = "app.localhost"
	resp1, _ := http.DefaultClient.Do(req)
	b1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if string(b1) != "version 1" {
		t.Errorf("expected version 1, got %s", b1)
	}

	// Now reconfigure upstream URL without restarting proxy
	_ = router.SetRoute("app", tsNew.URL, false)

	resp2, _ := http.DefaultClient.Do(req)
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(b2) != "version 2" {
		t.Errorf("expected version 2 after hot route update, got %s", b2)
	}
}

// Apps must receive every path unchanged, including ones that look like prot's own management routes.
func TestProxyForwardsManagementLookingPaths(t *testing.T) {
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		_, _ = w.Write([]byte("app:" + r.URL.Path))
	}))
	defer upstream.Close()

	router := NewRouter()
	if err := router.SetRoute("app", upstream.URL, false); err != nil {
		t.Fatalf("set route: %v", err)
	}
	proxySrv := NewServer(router, ServerOptions{ListenAddr: "127.0.0.1:0"})
	if err := proxySrv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = proxySrv.Close(context.Background()) }()

	for _, path := range []string{"/v1/users", "/v1/services?limit=5", "/v1/chat/completions", "/mcp"} {
		req, _ := http.NewRequest(http.MethodGet, "http://"+proxySrv.Addr()+path, nil)
		req.Host = "app.localhost"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "app:") {
			t.Errorf("%s: status %d body %q, want the upstream's response", path, resp.StatusCode, body)
		}
	}
	if len(seen) != 4 || seen[1] != "/v1/services?limit=5" {
		t.Errorf("upstream saw %v, want all four requests with paths and queries intact", seen)
	}
}

func TestStreamingSSE(t *testing.T) {
	// Verify that chunks/SSE flush immediately to the client
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Flushing not supported", 500)
			return
		}

		for i := 1; i <= 3; i++ {
			_, _ = fmt.Fprintf(w, "data: chunk %d\n\n", i)
			flusher.Flush()
			time.Sleep(30 * time.Millisecond)
		}
	}))
	defer ts.Close()

	router := NewRouter()
	_ = router.SetRoute("stream", ts.URL, false)

	proxySrv := NewServer(router, ServerOptions{ListenAddr: "127.0.0.1:0"})
	_ = proxySrv.Start()
	defer func() { _ = proxySrv.Close(context.Background()) }()

	req, _ := http.NewRequest(http.MethodGet, "http://"+proxySrv.Addr()+"/events", nil)
	req.Host = "stream.localhost"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream req failed: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	chunksReceived := 0
	start := time.Now()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "data: chunk") {
			chunksReceived++
			if chunksReceived == 1 {
				// First chunk should arrive almost immediately, well before stream finishes
				if time.Since(start) > 200*time.Millisecond {
					t.Errorf("first chunk buffered too long: took %v", time.Since(start))
				}
			}
		}
	}

	if chunksReceived != 3 {
		t.Errorf("expected 3 chunks, got %d", chunksReceived)
	}
}

func TestWebSocketForwarding(t *testing.T) {
	// Set up an upstream WebSocket echo server
	wsServer := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		var msg string
		for {
			if err := websocket.Message.Receive(ws, &msg); err != nil {
				return
			}
			if err := websocket.Message.Send(ws, "echo: "+msg); err != nil {
				return
			}
		}
	}))
	defer wsServer.Close()

	router := NewRouter()
	_ = router.SetRoute("ws", wsServer.URL, false)

	proxySrv := NewServer(router, ServerOptions{ListenAddr: "127.0.0.1:0"})
	_ = proxySrv.Start()
	defer func() { _ = proxySrv.Close(context.Background()) }()

	// Connect WebSocket client through the reverse proxy
	wsURL := "ws://ws.localhost/"
	origin := "http://localhost/"

	config, err := websocket.NewConfig(wsURL, origin)
	if err != nil {
		t.Fatalf("ws config err: %v", err)
	}

	tcpConn, err := net.Dial("tcp", proxySrv.Addr())
	if err != nil {
		t.Fatalf("tcp dial err: %v", err)
	}

	wsClient, err := websocket.NewClient(config, tcpConn)
	if err != nil {
		t.Fatalf("websocket handshake through proxy failed: %v", err)
	}
	defer wsClient.Close()

	testMsg := "hello prot proxy!"
	if err := websocket.Message.Send(wsClient, testMsg); err != nil {
		t.Fatalf("send err: %v", err)
	}

	var reply string
	if err := websocket.Message.Receive(wsClient, &reply); err != nil {
		t.Fatalf("receive err: %v", err)
	}

	expected := "echo: " + testMsg
	if reply != expected {
		t.Errorf("expected reply %q, got %q", expected, reply)
	}
}

func newTestProxy(t *testing.T, router *Router) *Server {
	t.Helper()
	srv := NewServer(router, ServerOptions{ListenAddr: "127.0.0.1:0"})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	return srv
}

func get(t *testing.T, srv *Server, host, path string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+srv.Addr()+path, nil)
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestProxyRewritesPathQueryAndHost(t *testing.T) {
	var gotURI, gotHost, gotFwdHost, gotFwdFor string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI, gotHost = r.URL.RequestURI(), r.Host
		gotFwdHost, gotFwdFor = r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Forwarded-For")
	}))
	defer upstream.Close()

	router := NewRouter()
	// The upstream URL itself carries a base path and query that must be merged with the request's.
	_ = router.SetRoute("app", upstream.URL+"/base?token=1", false)
	srv := newTestProxy(t, router)

	get(t, srv, "app.localhost:8080", "/users?page=2", nil)
	if gotURI != "/base/users?token=1&page=2" {
		t.Errorf("upstream saw %q, want /base/users?token=1&page=2", gotURI)
	}
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	if gotHost != upstreamHost {
		t.Errorf("upstream Host = %q, want %q", gotHost, upstreamHost)
	}
	if gotFwdHost != "app.localhost:8080" {
		t.Errorf("X-Forwarded-Host = %q", gotFwdHost)
	}
	if gotFwdFor != "127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q", gotFwdFor)
	}
}

func TestProxyAppendsToTrustedForwardedChain(t *testing.T) {
	var gotFor, gotProto string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFor, gotProto = r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Forwarded-Proto")
	}))
	defer upstream.Close()

	router := NewRouter()
	_ = router.SetRoute("app", upstream.URL, false)
	srv := newTestProxy(t, router)

	get(t, srv, "app.localhost", "/", map[string]string{
		"X-Forwarded-For":   "203.0.113.9",
		"X-Forwarded-Proto": "https",
	})
	if gotFor != "203.0.113.9, 127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q, want the inbound chain with this hop appended", gotFor)
	}
	if gotProto != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want the inbound value preserved", gotProto)
	}
}

// Diagnostic pages echo request-controlled data, so they must escape it.
func TestDiagnosticPagesEscapeHTML(t *testing.T) {
	router := NewRouter()
	srv := NewServer(router, ServerOptions{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = `<script>alert(1)</script>.localhost`
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("Host was not escaped in the not-found page: %s", body)
	}
	if !strings.Contains(rec.Body.String(), "prot alias &lt;alias&gt; &lt;upstream_url&gt;") {
		t.Errorf("not-found page should show the real alias syntax: %s", rec.Body.String())
	}

	// A refused upstream produces a 502 page that also escapes the error text.
	_ = router.SetRoute("dead", "http://127.0.0.1:1/<b>x</b>", false)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "dead.localhost"
	req.RemoteAddr = "127.0.0.1:50000" // httptest defaults to a non-loopback address, which private routes refuse
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<b>x</b>") {
		t.Errorf("target URL was not escaped in the 502 page: %s", rec.Body.String())
	}
}

// Updating a route while requests are in flight must be race-free (run with -race).
func TestRouteUpdatesDuringTrafficAreRaceFree(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("a")) }))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("b")) }))
	defer b.Close()

	router := NewRouter()
	_ = router.SetRoute("app", a.URL, false)
	srv := newTestProxy(t, router)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			target := a.URL
			if i%2 == 1 {
				target = b.URL
			}
			_ = router.SetRoute("app", target, i%3 == 0)
		}
	}()

	for i := 0; i < 60; i++ {
		resp, body := get(t, srv, "app.localhost", "/", nil)
		if resp.StatusCode != http.StatusOK || (body != "a" && body != "b") {
			t.Fatalf("request %d: %d %q", i, resp.StatusCode, body)
		}
	}
	close(stop)
	wg.Wait()
}

func TestSetRoutePreservesCreatedAt(t *testing.T) {
	router := NewRouter()
	_ = router.SetRoute("app", "http://127.0.0.1:1", false)
	first, _ := router.GetRoute("app")
	time.Sleep(5 * time.Millisecond)
	_ = router.SetRoute("app", "http://127.0.0.1:2", true)
	second, _ := router.GetRoute("app")

	if !second.CreatedAt.Equal(first.CreatedAt) || !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("CreatedAt must be preserved and UpdatedAt advance: %+v -> %+v", first, second)
	}
	if second.TargetURL != "http://127.0.0.1:2" || !second.LANExposed {
		t.Errorf("update not applied: %+v", second)
	}
	if first.TargetURL != "http://127.0.0.1:1" {
		t.Errorf("a previously returned copy must not change: %+v", first)
	}
}

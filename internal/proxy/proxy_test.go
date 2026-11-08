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

func TestManagementIsolation(t *testing.T) {
	router := NewRouter()
	proxySrv := NewServer(router, ServerOptions{ListenAddr: "127.0.0.1:0"})
	_ = proxySrv.Start()
	defer func() { _ = proxySrv.Close(context.Background()) }()

	proxyAddr := proxySrv.Addr()

	for _, path := range []string{"/v1/services", "/v1/events", "/mcp"} {
		req, _ := http.NewRequest(http.MethodGet, "http://"+proxyAddr+path, nil)
		req.Host = "app.localhost"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("req failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for management path %s, got %d", path, resp.StatusCode)
		}
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

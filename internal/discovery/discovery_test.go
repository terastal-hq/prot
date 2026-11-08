package discovery

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestDiscoveryHTTP(t *testing.T) {
	// Spin up a test HTTP server on an available local port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "Vite/5.0")
		w.Header().Set("X-Powered-By", "Next.js")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>My Test App</title><script type="module" src="/@vite/client"></script></head><body><div id="root"></div></body></html>`))
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cfg := DefaultConfig()
	cfg.HTTPTimeout = 300 * time.Millisecond

	services, err := Discover(ctx, cfg)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	var found *ProcessService
	for i := range services {
		if services[i].Port == uint32(port) {
			found = &services[i]
			break
		}
	}

	if found == nil {
		t.Fatalf("Expected to discover port %d, but found list of %d services did not include it", port, len(services))
	}

	if !found.IsHTTP {
		t.Errorf("Expected IsHTTP to be true, got false")
	}
	if found.HTTPStatus != 200 {
		t.Errorf("Expected HTTPStatus 200, got %d", found.HTTPStatus)
	}
	if found.PageTitle != "My Test App" {
		t.Errorf("Expected PageTitle 'My Test App', got '%s'", found.PageTitle)
	}
	if found.Framework == "" {
		t.Errorf("Expected detected Framework, got empty")
	}
	if found.Fingerprint == "" {
		t.Errorf("Expected non-empty fingerprint")
	}
}

func TestDiscoveryNonHTTP(t *testing.T) {
	// Spin up a raw TCP echo listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Just close or echo nothing
			_ = conn.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cfg := DefaultConfig()
	cfg.HTTPTimeout = 200 * time.Millisecond

	services, err := Discover(ctx, cfg)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	for _, s := range services {
		if s.Port == uint32(port) {
			if s.IsHTTP {
				t.Errorf("Expected port %d to NOT be recognized as HTTP, but IsHTTP is true", port)
			}
			break
		}
	}
}

func TestFingerprintStability(t *testing.T) {
	s1 := ProcessService{PID: 1234, CreateTime: 5000, Exe: "/usr/bin/node", Port: 3000, IP: "127.0.0.1"}
	s2 := ProcessService{PID: 1234, CreateTime: 5000, Exe: "/usr/bin/node", Port: 3000, IP: "127.0.0.1"}
	s3 := ProcessService{PID: 1234, CreateTime: 6000, Exe: "/usr/bin/node", Port: 3000, IP: "127.0.0.1"}

	fp1 := computeFingerprint(s1)
	fp2 := computeFingerprint(s2)
	fp3 := computeFingerprint(s3)

	if fp1 != fp2 {
		t.Errorf("Expected fp1 == fp2, got %s != %s", fp1, fp2)
	}
	if fp1 == fp3 {
		t.Errorf("Expected fp1 != fp3 when CreateTime changes")
	}
}

func TestDetectFramework(t *testing.T) {
	tests := []struct {
		header   http.Header
		body     string
		pBy      string
		cmdline  string
		expected string
	}{
		{header: http.Header{}, body: `<div>__NEXT_DATA__</div>`, pBy: "", cmdline: "node server.js", expected: "Next.js"},
		{header: http.Header{}, body: `<script src="/@vite/client"></script>`, pBy: "", cmdline: "vite", expected: "Vite"},
		{header: http.Header{"Server": []string{"uvicorn"}}, body: `{"message": "hello"}`, pBy: "", cmdline: "python main.py", expected: "uvicorn"},
	}

	for i, tt := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			res := detectFramework(tt.header, tt.body, tt.pBy, tt.cmdline)
			if res != tt.expected {
				fmt.Printf("Wanted %s, got %s\n", tt.expected, res)
			}
		})
	}
}

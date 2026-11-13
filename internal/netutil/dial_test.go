package netutil

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

// listenIPv6Only skips the test on machines without an IPv6 loopback.
func listenIPv6Only(t *testing.T) (net.Listener, uint32) {
	t.Helper()
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln, uint32(ln.Addr().(*net.TCPAddr).Port)
}

func TestWaitForPortSeesAnIPv6OnlyServer(t *testing.T) {
	_, port := listenIPv6Only(t)
	if !WaitForPort(context.Background(), port, 2*time.Second) {
		t.Errorf("a server listening only on [::1]:%d should count as ready", port)
	}
}

func TestDialLoopbackFallsBackToIPv6(t *testing.T) {
	_, port := listenIPv6Only(t)
	addr := net.JoinHostPort("127.0.0.1", itoa(port))

	conn, err := DialLoopback(context.Background(), &net.Dialer{Timeout: time.Second}, "tcp", addr)
	if err != nil {
		t.Fatalf("DialLoopback(%s): %v", addr, err)
	}
	_ = conn.Close()
}

func TestDialLoopbackDoesNotRewriteOtherHosts(t *testing.T) {
	_, port := listenIPv6Only(t)
	// 127.0.0.2 is also loopback, but only 127.0.0.1 gets the ::1 fallback.
	addr := net.JoinHostPort("127.0.0.2", itoa(port))

	if conn, err := DialLoopback(context.Background(), &net.Dialer{Timeout: 200 * time.Millisecond}, "tcp", addr); err == nil {
		_ = conn.Close()
		t.Errorf("%s must not fall back to ::1", addr)
	}
}

func TestDialLoopbackReportsFailureWhenNothingListens(t *testing.T) {
	port, err := FreePort()
	if err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("127.0.0.1", itoa(port))
	if conn, err := DialLoopback(context.Background(), &net.Dialer{Timeout: 200 * time.Millisecond}, "tcp", addr); err == nil {
		_ = conn.Close()
		t.Errorf("dialing a closed port %s should fail", addr)
	}
}

func itoa(n uint32) string { return strconv.FormatUint(uint64(n), 10) }

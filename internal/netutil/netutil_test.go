package netutil

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestFreePortIsUsable(t *testing.T) {
	port, err := FreePort()
	if err != nil || port == 0 {
		t.Fatalf("FreePort = %d, %v", port, err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		t.Fatalf("returned port %d is not bindable: %v", port, err)
	}
	ln.Close()
}

func TestWaitForPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	open := uint32(ln.Addr().(*net.TCPAddr).Port)

	if !WaitForPort(context.Background(), open, time.Second) {
		t.Error("open port should be reported ready")
	}

	closed, _ := FreePort()
	start := time.Now()
	if WaitForPort(context.Background(), closed, 300*time.Millisecond) {
		t.Error("closed port should not be reported ready")
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Errorf("timeout not honoured, took %v", elapsed)
	}
}

func TestWaitForPortStopsOnCancel(t *testing.T) {
	closed, _ := FreePort()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if WaitForPort(ctx, closed, 10*time.Second) {
		t.Error("cancelled wait must not report ready")
	}
	if time.Since(start) > time.Second {
		t.Error("cancellation was not prompt")
	}
}

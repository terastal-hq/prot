// Package netutil holds small loopback networking helpers shared by the supervisor and tunnel manager.
package netutil

import (
	"context"
	"fmt"
	"net"
	"time"
)

// FreePort asks the kernel for an unused loopback TCP port.
//
// The port is released before returning, so another process can claim it before the caller binds it. Callers
// that spawn a process on the port should treat a bind failure as retryable.
func FreePort() (uint32, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("finding free port: %w", err)
	}
	defer ln.Close()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", ln.Addr())
	}
	return uint32(addr.Port), nil
}

// WaitForPort polls 127.0.0.1:port until it accepts a connection, the timeout elapses or ctx is cancelled.
// It reports whether the port became reachable.
func WaitForPort(ctx context.Context, port uint32, timeout time.Duration) bool {
	const (
		dialTimeout  = 150 * time.Millisecond
		pollInterval = 75 * time.Millisecond
	)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	dialer := net.Dialer{Timeout: dialTimeout}
	for {
		if conn, err := dialer.DialContext(ctx, "tcp", addr); err == nil {
			_ = conn.Close()
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

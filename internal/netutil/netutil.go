// Package netutil holds small loopback networking helpers shared by the supervisor and tunnel manager.
package netutil

import (
	"context"
	"fmt"
	"net"
	"time"
)

// LoopbackURL returns the http URL of a service listening on a loopback port.
func LoopbackURL(port uint32) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

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

// DialLoopback dials addr like d.DialContext, but when addr is on 127.0.0.1 and nothing answers there it also
// tries [::1] on the same port.
//
// Dev servers often bind "localhost", which Node 17 and later resolve to ::1 first, so a server can be up on the
// IPv6 loopback and invisible on IPv4. The error returned when both fail is the IPv4 one.
func DialLoopback(ctx context.Context, d *net.Dialer, network, addr string) (net.Conn, error) {
	conn, err := d.DialContext(ctx, network, addr)
	if err == nil {
		return conn, nil
	}
	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil || host != "127.0.0.1" || ctx.Err() != nil {
		return nil, err
	}
	if conn6, err6 := d.DialContext(ctx, network, net.JoinHostPort("::1", port)); err6 == nil {
		return conn6, nil
	}
	return nil, err
}

// WaitForPort polls localhost:port, on both the IPv4 and IPv6 loopback, until it accepts a connection, the
// timeout elapses or ctx is cancelled. It reports whether the port became reachable.
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
		if conn, err := DialLoopback(ctx, &dialer, "tcp", addr); err == nil {
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

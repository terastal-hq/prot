package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Endpoint is where a running daemon accepts MCP requests.
type Endpoint struct {
	URL   string // for example http://127.0.0.1:4400/mcp
	Token string // management bearer token
}

// Resolver locates the daemon. It is called for every message so a daemon that starts, stops or
// restarts during an agent session is picked up without restarting the bridge.
type Resolver func() (Endpoint, error)

// Bridge relays MCP JSON-RPC messages from a stdio transport to the daemon's HTTP /mcp endpoint.
//
// It holds no registry, supervisor or router of its own: every tool call runs inside protd, so agents
// see exactly the same services, logs and process state as the CLI and the desktop app.
type Bridge struct {
	resolve Resolver
	client  *http.Client
}

// NewBridge creates a Bridge that locates the daemon through resolve.
func NewBridge(resolve Resolver) *Bridge {
	// Starting a service can wait for its port to open, so allow generous time per request.
	return &Bridge{resolve: resolve, client: &http.Client{Timeout: 2 * time.Minute}}
}

// Daemon-unavailable and transport failures use the JSON-RPC server error range.
const (
	codeDaemonUnavailable = -32001
	codeDaemonRejected    = -32002
)

// ServeStdio reads newline-delimited JSON-RPC messages from in and writes the daemon's replies to out.
func (b *Bridge) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := b.relay(ctx, line, out); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// relay forwards one message and writes at most one line to out.
func (b *Bridge) relay(ctx context.Context, line []byte, out io.Writer) error {
	var head struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return writeLine(out, errorResponse(nil, -32700, "Parse error: "+err.Error()))
	}
	hasID := len(head.ID) > 0 && string(head.ID) != "null"

	reply, err := b.post(ctx, line)
	if err != nil {
		if !hasID {
			return nil // nothing can be reported for a notification
		}
		code := codeDaemonUnavailable
		var rejected *rejectedError
		if errors.As(err, &rejected) {
			code = codeDaemonRejected
		}
		return writeLine(out, errorResponse(head.ID, code, err.Error()))
	}
	if len(reply) == 0 {
		return nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, reply); err != nil {
		return writeLine(out, errorResponse(head.ID, -32603, "Invalid response from daemon: "+err.Error()))
	}
	return writeLine(out, compact.Bytes())
}

type rejectedError struct {
	status int
	body   string
}

func (e *rejectedError) Error() string {
	return fmt.Sprintf("prot daemon rejected the request (HTTP %d): %s", e.status, e.body)
}

func (b *Bridge) post(ctx context.Context, body []byte) ([]byte, error) {
	ep, err := b.resolve()
	if err != nil {
		return nil, fmt.Errorf("prot daemon unavailable: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building daemon request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ep.Token)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prot daemon unavailable at %s (is protd running?): %w", ep.URL, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("reading daemon response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &rejectedError{status: resp.StatusCode, body: string(bytes.TrimSpace(data))}
	}
	return bytes.TrimSpace(data), nil
}

func errorResponse(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	data, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   RPCError        `json:"error"`
	}{"2.0", id, RPCError{Code: code, Message: message}})
	return data
}

func writeLine(out io.Writer, data []byte) error {
	_, err := out.Write(append(data, '\n'))
	return err
}

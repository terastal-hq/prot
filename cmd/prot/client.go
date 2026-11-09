package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// requestTimeout bounds ordinary daemon calls so a wedged daemon cannot hang the CLI forever. Streaming calls
// (log following) use a client without a timeout.
const requestTimeout = 30 * time.Second

type daemonClient struct {
	adminAddr string
	token     string
}

var (
	callClient   = &http.Client{Timeout: requestTimeout}
	streamClient = &http.Client{}
)

// dataDir is where prot keeps its token and daemon state: $PROT_DATA_DIR or ~/.prot.
func dataDir() (string, error) {
	if dir := os.Getenv("PROT_DATA_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".prot"), nil
}

func getDaemonClient() (*daemonClient, error) {
	dir, err := dataDir()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(dir, "daemon.json"))
	if err != nil {
		return nil, errors.New("daemon is not running (start with 'protd')")
	}

	var state struct {
		PID       int    `json:"pid"`
		AdminAddr string `json:"admin_addr"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("corrupt daemon state file: %w", err)
	}

	tokenBytes, err := os.ReadFile(filepath.Join(dir, "auth.token"))
	if err != nil {
		return nil, fmt.Errorf("reading auth token: %w", err)
	}

	return &daemonClient{
		adminAddr: state.AdminAddr,
		token:     strings.TrimSpace(string(tokenBytes)),
	}, nil
}

// connect returns a client for the running daemon or exits with a helpful message.
func connect() *daemonClient {
	client, err := getDaemonClient()
	if err != nil {
		fatalf("Error: %v\nStart the daemon with 'systemctl --user start protd' or run 'protd'.", err)
	}
	return client
}

// fatalf prints to stderr and exits with status 1.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// servicePath builds /v1/services/<id>[/suffix...] with the id escaped, so an alias containing "/" or "?" cannot
// change which endpoint is called.
func servicePath(id string, suffix ...string) string {
	return "/v1/services/" + url.PathEscape(id) + strings.Join(append([]string{""}, suffix...), "/")
}

func (c *daemonClient) newRequest(method, path string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, "http://"+c.adminAddr+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// request sends a call and returns the raw response; the caller closes the body.
func (c *daemonClient) request(method, path string, body any) (*http.Response, error) {
	req, err := c.newRequest(method, path, body)
	if err != nil {
		return nil, err
	}
	return callClient.Do(req)
}

// stream opens a long-lived response (server-sent events) with no overall timeout.
func (c *daemonClient) stream(path string) (*http.Response, error) {
	req, err := c.newRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return streamClient.Do(req)
}

// apiError is a non-success reply from the daemon.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
}

// call performs a request, expects status want and decodes a JSON reply into out (when non-nil). Any other status
// becomes an *apiError carrying the daemon's message.
func (c *daemonClient) call(method, path string, body, out any, want int) error {
	resp, err := c.request(method, path, body)
	if err != nil {
		return fmt.Errorf("daemon request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != want {
		return readAPIError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding daemon response: %w", err)
	}
	return nil
}

func readAPIError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var payload struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	message := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &payload) == nil {
		switch {
		case payload.Message != "":
			message = payload.Message
		case payload.Error != "":
			message = payload.Error
		}
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return &apiError{Status: resp.StatusCode, Message: message}
}

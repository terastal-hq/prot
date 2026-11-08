package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
)

// JSONRPCRequest represents an incoming MCP request.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse represents an outgoing MCP response.
type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

// RPCError is a standard JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Tool represents an MCP tool definition.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// TextContent is an MCP content item.
type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolResult is the standard tools/call result.
type ToolResult struct {
	Content []TextContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Server handles MCP protocol interactions.
type Server struct {
	reg *registry.Registry
}

// NewServer constructs an MCP Server.
func NewServer(reg *registry.Registry) *Server {
	return &Server{reg: reg}
}

// HandleRequest processes an incoming JSON-RPC message.
func (s *Server) HandleRequest(ctx context.Context, req JSONRPCRequest) *JSONRPCResponse {
	resp := &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
	}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]string{
				"name":    "prot",
				"version": "0.1.0",
			},
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
		}
		return resp

	case "notifications/initialized":
		return nil // Notifications do not return a response

	case "ping":
		resp.Result = map[string]interface{}{}
		return resp

	case "tools/list":
		resp.Result = map[string]interface{}{
			"tools": s.listTools(),
		}
		return resp

	case "tools/call":
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &RPCError{Code: -32602, Message: "Invalid params: " + err.Error()}
			return resp
		}

		result, err := s.callTool(ctx, params.Name, params.Arguments)
		if err != nil {
			resp.Result = ToolResult{
				Content: []TextContent{{Type: "text", Text: "Error: " + err.Error()}},
				IsError: true,
			}
			return resp
		}
		resp.Result = result
		return resp

	default:
		resp.Error = &RPCError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)}
		return resp
	}
}

func (s *Server) listTools() []Tool {
	return []Tool{
		{
			Name: "list_services",
			Description: "Discover and list all registered managed services and currently " +
				"observed local TCP development services.",
			InputSchema: map[string]interface{}{
				"type": "object",
			},
		},
		{
			Name:        "get_service",
			Description: "Get detailed configuration and runtime state for a service by ID or alias.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"required":   []string{"id_or_alias"},
				"properties": map[string]interface{}{"id_or_alias": map[string]string{"type": "string"}},
			},
		},
		{
			Name:        "get_service_url",
			Description: "Get the canonical .localhost browser-facing URL and direct IP:port for a service.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"required":   []string{"id_or_alias"},
				"properties": map[string]interface{}{"id_or_alias": map[string]string{"type": "string"}},
			},
		},
		{
			Name:        "get_service_logs",
			Description: "Retrieve bounded recent logs (stdout/stderr) for a managed service.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"id_or_alias"},
				"properties": map[string]interface{}{
					"id_or_alias": map[string]string{"type": "string"},
					"limit":       map[string]string{"type": "integer"},
				},
			},
		},
		{
			Name:        "ensure_running",
			Description: "Idempotently ensure a service is running. If stopped, it will be started.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"required":   []string{"id_or_alias"},
				"properties": map[string]interface{}{"id_or_alias": map[string]string{"type": "string"}},
			},
		},
		{
			Name:        "start_service",
			Description: "Start a managed development service.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"required":   []string{"id"},
				"properties": map[string]interface{}{"id": map[string]string{"type": "string"}},
			},
		},
		{
			Name:        "stop_service",
			Description: "Stop a running managed service.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"required":   []string{"id"},
				"properties": map[string]interface{}{"id": map[string]string{"type": "string"}},
			},
		},
		{
			Name:        "restart_service",
			Description: "Restart a managed service.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"required":   []string{"id"},
				"properties": map[string]interface{}{"id": map[string]string{"type": "string"}},
			},
		},
		{
			Name:        "set_alias",
			Description: "Map or update a .localhost alias for a service.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"alias", "target_url"},
				"properties": map[string]interface{}{
					"alias":      map[string]string{"type": "string"},
					"target_url": map[string]string{"type": "string"},
				},
			},
		},
	}
}

func (s *Server) callTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	switch name {
	case "list_services":
		services, err := s.reg.ListServices(ctx)
		if err != nil {
			return nil, err
		}
		data, _ := json.MarshalIndent(services, "", "  ")
		return &ToolResult{Content: []TextContent{{Type: "text", Text: string(data)}}}, nil

	case "get_service":
		idOrAlias, _ := args["id_or_alias"].(string)
		if idOrAlias == "" {
			return nil, fmt.Errorf("id_or_alias is required")
		}
		svc, err := s.reg.GetService(ctx, idOrAlias)
		if err != nil {
			return nil, err
		}
		data, _ := json.MarshalIndent(svc, "", "  ")
		return &ToolResult{Content: []TextContent{{Type: "text", Text: string(data)}}}, nil

	case "get_service_url":
		idOrAlias, _ := args["id_or_alias"].(string)
		svc, err := s.reg.GetService(ctx, idOrAlias)
		if err != nil {
			return nil, err
		}
		res := fmt.Sprintf("Canonical URL: %s\nDirect URL: %s\nStatus: %s", svc.CanonicalURL, svc.DirectURL, svc.Status)
		return &ToolResult{Content: []TextContent{{Type: "text", Text: res}}}, nil

	case "get_service_logs":
		idOrAlias, _ := args["id_or_alias"].(string)
		limit := 100
		if l, ok := args["limit"].(float64); ok && l > 0 {
			limit = int(l)
		}
		svc, err := s.reg.GetService(ctx, idOrAlias)
		if err != nil {
			return nil, err
		}
		logs, err := s.reg.GetLogs(ctx, svc.ID, limit)
		if err != nil {
			return nil, err
		}
		lines := make([]string, len(logs))
		for i, l := range logs {
			lines[i] = fmt.Sprintf("[%s] [%s] %s", l.Timestamp.Format("15:04:05"), l.Stream, l.Message)
		}
		data, _ := json.MarshalIndent(lines, "", "  ")
		return &ToolResult{Content: []TextContent{{Type: "text", Text: string(data)}}}, nil

	case "ensure_running":
		idOrAlias, _ := args["id_or_alias"].(string)
		svc, err := s.reg.GetService(ctx, idOrAlias)
		if err != nil {
			return nil, err
		}
		if svc.Status == "running" {
			return &ToolResult{
				Content: []TextContent{
					{
						Type: "text",
						Text: fmt.Sprintf("Service %s is already running at %s", svc.Name, svc.CanonicalURL),
					},
				},
			}, nil
		}
		started, err := s.reg.StartService(ctx, svc.ID)
		if err != nil {
			return nil, err
		}
		return &ToolResult{
			Content: []TextContent{
				{
					Type: "text",
					Text: fmt.Sprintf(
						"Service %s started (status: %s, URL: %s)",
						started.Name,
						started.Status,
						started.CanonicalURL,
					),
				},
			},
		}, nil

	case "start_service":
		id, _ := args["id"].(string)
		started, err := s.reg.StartService(ctx, id)
		if err != nil {
			return nil, err
		}
		return &ToolResult{
			Content: []TextContent{
				{
					Type: "text",
					Text: fmt.Sprintf("Started service %s (%s)", started.Name, started.Status),
				},
			},
		}, nil

	case "stop_service":
		id, _ := args["id"].(string)
		stopped, err := s.reg.StopService(ctx, id)
		if err != nil {
			return nil, err
		}
		return &ToolResult{
			Content: []TextContent{
				{
					Type: "text",
					Text: fmt.Sprintf("Stopped service %s", stopped.Name),
				},
			},
		}, nil

	case "restart_service":
		id, _ := args["id"].(string)
		restarted, err := s.reg.RestartService(ctx, id)
		if err != nil {
			return nil, err
		}
		return &ToolResult{
			Content: []TextContent{
				{
					Type: "text",
					Text: fmt.Sprintf("Restarted service %s (%s)", restarted.Name, restarted.Status),
				},
			},
		}, nil

	case "set_alias":
		alias, _ := args["alias"].(string)
		targetURL, _ := args["target_url"].(string)
		if alias == "" || targetURL == "" {
			return nil, fmt.Errorf("alias and target_url are required")
		}
		svc := storage.Service{
			ID:       "alias_" + alias,
			Name:     alias,
			Alias:    alias,
			Status:   "running",
			Runtime:  "discovered",
			AutoPort: false,
		}
		_, err := s.reg.RegisterService(ctx, svc)
		if err != nil {
			return nil, err
		}
		return &ToolResult{
			Content: []TextContent{
				{
					Type: "text",
					Text: fmt.Sprintf("Alias mapped: http://%s.localhost -> %s", alias, targetURL),
				},
			},
		}, nil

	default:
		return nil, fmt.Errorf("unknown tool: %q", name)
	}
}

// ServeStdio runs the MCP JSON-RPC protocol over standard input/output.
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	// Allow large messages up to 10MB
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				Error:   &RPCError{Code: -32700, Message: "Parse error: " + err.Error()},
			}
			data, _ := json.Marshal(resp)
			_, _ = fmt.Fprintf(out, "%s\n", string(data))
			continue
		}

		resp := s.HandleRequest(ctx, req)
		if resp != nil {
			data, _ := json.Marshal(resp)
			_, _ = fmt.Fprintf(out, "%s\n", string(data))
		}
	}

	return scanner.Err()
}

// ServeHTTP serves the MCP protocol over loopback HTTP POST /mcp.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: -32700, Message: "Parse error: " + err.Error()},
		})
		return
	}

	resp := s.HandleRequest(r.Context(), req)
	w.Header().Set("Content-Type", "application/json")
	if resp != nil {
		_ = json.NewEncoder(w).Encode(resp)
	}
}

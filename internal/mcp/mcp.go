package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/terastal-hq/prot/internal/registry"
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
	reg     *registry.Registry
	version string
	tools   []toolSpec
}

// NewServer constructs an MCP Server.
func NewServer(reg *registry.Registry) *Server {
	s := &Server{reg: reg, version: "dev"}
	s.tools = s.toolSpecs()
	return s
}

// SetVersion sets the version reported to clients in the initialize handshake.
func (s *Server) SetVersion(version string) {
	if version != "" {
		s.version = version
	}
}

// HandleRequest processes an incoming JSON-RPC message.
func (s *Server) HandleRequest(ctx context.Context, req JSONRPCRequest) *JSONRPCResponse {
	// JSON-RPC notifications carry no id and must never be answered, even when the method is unknown
	// (for example notifications/cancelled).
	if req.ID == nil && strings.HasPrefix(req.Method, "notifications/") {
		return nil
	}

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
				"version": s.version,
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

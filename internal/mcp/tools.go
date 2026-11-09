package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/terastal-hq/prot/internal/storage"
)

// toolSpec pairs a tool's advertised definition with its implementation, so the schema an agent reads and the
// code that runs can never drift apart.
type toolSpec struct {
	tool Tool
	run  func(ctx context.Context, args map[string]any) (string, error)
}

// Tool schema building blocks.
var (
	stringProp  = map[string]string{"type": "string"}
	integerProp = map[string]string{"type": "integer"}
)

func objectSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{"type": "object"}
	if len(required) > 0 {
		schema["required"] = required
	}
	if len(properties) > 0 {
		schema["properties"] = properties
	}
	return schema
}

// serviceTool is a tool that takes only a target service.
func serviceTool(
	name, description string,
	run func(ctx context.Context, id string) (string, error),
) toolSpec {
	return toolSpec{
		tool: Tool{
			Name:        name,
			Description: description,
			InputSchema: objectSchema([]string{"id_or_alias"}, map[string]any{"id_or_alias": stringProp}),
		},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			id, err := serviceArg(args)
			if err != nil {
				return "", err
			}
			return run(ctx, id)
		},
	}
}

func (s *Server) toolSpecs() []toolSpec {
	return []toolSpec{
		{
			tool: Tool{
				Name: "list_services",
				Description: "Discover and list all registered managed services and currently " +
					"observed local TCP development services.",
				InputSchema: objectSchema(nil, nil),
			},
			run: func(ctx context.Context, _ map[string]any) (string, error) {
				services, err := s.reg.ListServices(ctx)
				if err != nil {
					return "", err
				}
				return prettyJSON(services)
			},
		},
		serviceTool(
			"get_service",
			"Get detailed configuration and runtime state for a service by ID or alias.",
			func(ctx context.Context, id string) (string, error) {
				svc, err := s.reg.GetService(ctx, id)
				if err != nil {
					return "", err
				}
				return prettyJSON(svc)
			},
		),
		serviceTool(
			"get_service_url",
			"Get the canonical .localhost browser-facing URL and direct IP:port for a service.",
			func(ctx context.Context, id string) (string, error) {
				svc, err := s.reg.GetService(ctx, id)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf(
					"Canonical URL: %s\nDirect URL: %s\nStatus: %s",
					svc.CanonicalURL, svc.DirectURL, svc.Status,
				), nil
			},
		),
		s.logsTool(),
		serviceTool(
			"ensure_running",
			"Idempotently ensure a service is running. If stopped, it will be started.",
			func(ctx context.Context, id string) (string, error) {
				svc, err := s.reg.GetService(ctx, id)
				if err != nil {
					return "", err
				}
				if svc.Status == storage.StatusRunning {
					return fmt.Sprintf("Service %s is already running at %s", svc.Name, svc.CanonicalURL), nil
				}
				started, err := s.reg.StartService(ctx, svc.ID)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf(
					"Service %s started (status: %s, URL: %s)",
					started.Name, started.Status, started.CanonicalURL,
				), nil
			},
		),
		serviceTool("start_service", "Start a managed development service.",
			func(ctx context.Context, id string) (string, error) {
				started, err := s.reg.StartService(ctx, id)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Started service %s (%s)", started.Name, started.Status), nil
			},
		),
		serviceTool("stop_service", "Stop a running managed service.",
			func(ctx context.Context, id string) (string, error) {
				stopped, err := s.reg.StopService(ctx, id)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Stopped service %s", stopped.Name), nil
			},
		),
		serviceTool("restart_service", "Restart a managed service.",
			func(ctx context.Context, id string) (string, error) {
				restarted, err := s.reg.RestartService(ctx, id)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Restarted service %s (%s)", restarted.Name, restarted.Status), nil
			},
		),
		s.aliasTool(),
	}
}

func (s *Server) logsTool() toolSpec {
	return toolSpec{
		tool: Tool{
			Name:        "get_service_logs",
			Description: "Retrieve bounded recent logs (stdout/stderr) for a managed service.",
			InputSchema: objectSchema([]string{"id_or_alias"}, map[string]any{
				"id_or_alias": stringProp,
				"limit":       integerProp,
			}),
		},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			id, err := serviceArg(args)
			if err != nil {
				return "", err
			}
			limit := 100
			if l, ok := args["limit"].(float64); ok && l > 0 {
				limit = int(l)
			}

			logs, err := s.reg.GetLogs(ctx, id, limit)
			if err != nil {
				return "", err
			}
			lines := make([]string, len(logs))
			for i, l := range logs {
				lines[i] = fmt.Sprintf("[%s] [%s] %s", l.Timestamp.Format("15:04:05"), l.Stream, l.Message)
			}
			return prettyJSON(lines)
		},
	}
}

func (s *Server) aliasTool() toolSpec {
	return toolSpec{
		tool: Tool{
			Name: "set_alias",
			Description: "Change the .localhost alias of a registered service. The new alias routes " +
				"immediately; the old one stops resolving.",
			InputSchema: objectSchema([]string{"id_or_alias", "alias"}, map[string]any{
				"id_or_alias": stringProp,
				"alias":       stringProp,
			}),
		},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			id, err := serviceArg(args)
			if err != nil {
				return "", err
			}
			alias, _ := args["alias"].(string)
			if alias == "" {
				return "", fmt.Errorf("alias is required")
			}
			svc, err := s.reg.RenameAlias(ctx, id, alias)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Alias set: %s -> %s", svc.CanonicalURL, svc.DirectURL), nil
		},
	}
}

func (s *Server) listTools() []Tool {
	tools := make([]Tool, len(s.tools))
	for i, spec := range s.tools {
		tools[i] = spec.tool
	}
	return tools
}

func (s *Server) callTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	for _, spec := range s.tools {
		if spec.tool.Name != name {
			continue
		}
		text, err := spec.run(ctx, args)
		if err != nil {
			return nil, err
		}
		return &ToolResult{Content: []TextContent{{Type: "text", Text: text}}}, nil
	}
	return nil, fmt.Errorf("unknown tool: %q", name)
}

// serviceArg reads the target service from tool arguments. "id_or_alias" is canonical; the older "id"
// spelling is still accepted so existing agent prompts keep working.
func serviceArg(args map[string]any) (string, error) {
	for _, key := range []string{"id_or_alias", "id"} {
		if v, _ := args[key].(string); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("id_or_alias is required")
}

func prettyJSON(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding result: %w", err)
	}
	return string(data), nil
}

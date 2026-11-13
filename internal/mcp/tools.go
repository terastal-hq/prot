package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/terastal-hq/prot/internal/registry"
	"github.com/terastal-hq/prot/internal/storage"
	"github.com/terastal-hq/prot/internal/tunnels"
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

// serviceTool is a tool that takes only a target service (or tunnel) by id or alias.
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
		s.listTunnelsTool(),
		s.listSSHHostsTool(),
		s.addTunnelTool(),
		serviceTool(
			"start_tunnel",
			"Connect a saved SSH tunnel by id or alias and wait until it is reachable (or report why it is not).",
			func(ctx context.Context, id string) (string, error) {
				if _, err := s.reg.StartTunnel(ctx, id); err != nil {
					return "", err
				}
				view, err := s.reg.WaitForTunnel(ctx, id, tunnelConnectWait)
				if err != nil {
					return "", err
				}
				switch {
				case view.State == tunnels.StateConnected:
					return fmt.Sprintf(
						"Tunnel %s is connected: %s (local port %d)",
						view.Config.Alias, view.CanonicalURL, view.AssignedLocalPort,
					), nil
				case view.LastError != "":
					return "", fmt.Errorf("tunnel %s could not connect: %s", view.Config.Alias, view.LastError)
				default:
					return fmt.Sprintf("Tunnel %s is still connecting; check list_tunnels", view.Config.Alias), nil
				}
			},
		),
		serviceTool("stop_tunnel", "Disconnect an SSH tunnel by id or alias.",
			func(_ context.Context, id string) (string, error) {
				view, err := s.reg.StopTunnel(id)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Stopped tunnel %s", view.Config.Alias), nil
			},
		),
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
		s.addServiceTool(),
		s.updateServiceTool(),
		s.aliasTool(),
	}
}

// tunnelConnectWait bounds how long start_tunnel waits for a connection before answering.
const tunnelConnectWait = 15 * time.Second

func (s *Server) listTunnelsTool() toolSpec {
	return toolSpec{
		tool: Tool{
			Name: "list_tunnels",
			Description: "List saved SSH tunnels with their state, the .localhost address each is reachable at, " +
				"and the last error when one cannot connect.",
			InputSchema: objectSchema(nil, nil),
		},
		run: func(_ context.Context, _ map[string]any) (string, error) {
			return prettyJSON(s.reg.ListTunnels())
		},
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

// serviceFieldProps are the settings add_service and update_service share.
func serviceFieldProps() map[string]any {
	return map[string]any{
		"name": map[string]string{"type": "string", "description": "Display name"},
		"cmd": map[string]string{
			"type": "string",
			"description": "Shell command that starts the service, run with `sh -c`. " +
				"Include any arguments, e.g. `npm run dev -- --host 127.0.0.1`.",
		},
		"cwd": map[string]string{"type": "string", "description": "Absolute working directory for the command"},
		"port": map[string]string{
			"type":        "integer",
			"description": "TCP port the service listens on; the .localhost route targets it. Also passed as $PORT.",
		},
		"alias": map[string]string{
			"type": "string",
			"description": "Name used for http://<alias>.localhost. Lowercase letters, digits and hyphens. " +
				"Defaults to the name, lowercased with spaces turned into hyphens.",
		},
		"auto_port": map[string]string{
			"type":        "boolean",
			"description": "Pick a free port at each start and pass it as $PORT (overrides port)",
		},
		"auto_start": map[string]string{"type": "boolean", "description": "Start the service whenever protd starts"},
		"env": map[string]any{
			"type":                 "object",
			"additionalProperties": stringProp,
			"description": "Environment variables set on top of the daemon's environment. Values are literal: " +
				"$VAR is not expanded, so give a full PATH if you override it.",
		},
		"restart_policy": map[string]any{
			"type":        "string",
			"enum":        []string{storage.RestartNever, storage.RestartOnFailure, storage.RestartAlways},
			"description": "When to restart the process after it exits (default on-failure)",
		},
	}
}

func (s *Server) addServiceTool() toolSpec {
	props := serviceFieldProps()
	props["start"] = map[string]string{"type": "boolean", "description": "Start the service right after registering it"}

	return toolSpec{
		tool: Tool{
			Name: "add_service",
			Description: "Register a new managed development service so it can be started, routed at " +
				"http://<alias>.localhost and supervised. The command runs as the user, in the daemon's " +
				"environment (login-shell PATH) plus env. Set port to the port the server listens on, or " +
				"auto_port to have one assigned and passed as $PORT. Servers should listen on 127.0.0.1 or localhost. " +
				"Use update_service to change one later.",
			InputSchema: objectSchema([]string{"name", "cmd"}, props),
		},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			svc := storage.Service{RestartPolicy: storage.RestartOnFailure}
			var err error
			if svc.Name, err = stringArg(args, "name"); err != nil {
				return "", err
			}
			if svc.Command, err = stringArg(args, "cmd"); err != nil {
				return "", err
			}
			if svc.Cwd, err = stringArg(args, "cwd"); err != nil {
				return "", err
			}
			if svc.Alias, err = stringArg(args, "alias"); err != nil {
				return "", err
			}
			if svc.Alias == "" {
				svc.Alias = strings.ToLower(strings.Join(strings.Fields(svc.Name), "-"))
			}
			if v, ok, err := uintArg(args, "port"); err != nil {
				return "", err
			} else if ok {
				svc.Port = v
			}
			if v, ok, err := boolArg(args, "auto_port"); err != nil {
				return "", err
			} else if ok {
				svc.AutoPort = v
			}
			if v, ok, err := boolArg(args, "auto_start"); err != nil {
				return "", err
			} else if ok {
				svc.AutoStart = v
			}
			if v, ok, err := stringOptArg(args, "restart_policy"); err != nil {
				return "", err
			} else if ok {
				svc.RestartPolicy = v
			}
			if env, ok, err := envArg(args, "env"); err != nil {
				return "", err
			} else if ok {
				svc.Env = env
			}
			start, _, err := boolArg(args, "start")
			if err != nil {
				return "", err
			}

			view, err := s.reg.CreateService(ctx, svc)
			if err != nil {
				return "", err
			}
			summary := fmt.Sprintf("Registered service %s (id %s)", view.Name, view.ID)
			if start {
				started, err := s.reg.StartService(ctx, view.ID)
				if err != nil {
					return "", fmt.Errorf("registered %s (id %s) but it failed to start: %w", view.Name, view.ID, err)
				}
				view = started
				summary += fmt.Sprintf("; started (%s)", view.Status)
			}
			body, err := prettyJSON(view)
			return summary + "\n" + body, err
		},
	}
}

func (s *Server) updateServiceTool() toolSpec {
	return toolSpec{
		tool: Tool{
			Name: "update_service",
			Description: "Change a registered service's settings. Only the fields you pass change. env replaces " +
				"the whole map (pass {} to clear it). A running service keeps its old command, cwd, port, " +
				"env and restart policy until restart_service; alias and name apply immediately.",
			InputSchema: objectSchema([]string{"id_or_alias"}, func() map[string]any {
				props := serviceFieldProps()
				props["id_or_alias"] = stringProp
				props["lan_exposed"] = map[string]any{
					"type":        "boolean",
					"description": "Make the route reachable from other machines on the network. Leave unset unless asked.",
				}
				return props
			}()),
		},
		run: func(ctx context.Context, args map[string]any) (string, error) {
			id, err := serviceArg(args)
			if err != nil {
				return "", err
			}

			var patch registry.ServicePatch
			for key, dst := range map[string]**string{
				"name": &patch.Name, "cmd": &patch.Command, "cwd": &patch.Cwd,
				"alias": &patch.Alias, "restart_policy": &patch.RestartPolicy,
			} {
				v, ok, err := stringOptArg(args, key)
				if err != nil {
					return "", err
				}
				if ok {
					*dst = &v
				}
			}
			if v, ok, err := uintArg(args, "port"); err != nil {
				return "", err
			} else if ok {
				patch.Port = &v
			}
			for key, dst := range map[string]**bool{
				"auto_port": &patch.AutoPort, "auto_start": &patch.AutoStart, "lan_exposed": &patch.LANExposed,
			} {
				v, ok, err := boolArg(args, key)
				if err != nil {
					return "", err
				}
				if ok {
					*dst = &v
				}
			}
			if env, ok, err := envArg(args, "env"); err != nil {
				return "", err
			} else if ok {
				if env == nil {
					env = map[string]string{}
				}
				patch.Env = &env
			}

			view, err := s.reg.UpdateService(ctx, id, patch)
			if err != nil {
				return "", err
			}
			body, err := prettyJSON(view)
			return fmt.Sprintf("Updated service %s; restart it for command, cwd, port and env changes to apply\n%s", view.Name, body), err
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
		text, err := spec.run(registry.WithSource(ctx, registry.SourceMCP), args)
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

// stringOptArg reads an optional string argument and reports whether it was given.
func stringOptArg(args map[string]any, key string) (string, bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", false, nil
	}
	v, isString := raw.(string)
	if !isString {
		return "", false, fmt.Errorf("%s must be a string", key)
	}
	return strings.TrimSpace(v), true, nil
}

// stringArg reads an optional string argument, returning "" when it is absent.
func stringArg(args map[string]any, key string) (string, error) {
	v, _, err := stringOptArg(args, key)
	return v, err
}

// uintArg reads an optional non-negative whole number.
func uintArg(args map[string]any, key string) (uint32, bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	n, isNumber := raw.(float64)
	if !isNumber || n != math.Trunc(n) || n < 0 || n > 65535 {
		return 0, false, fmt.Errorf("%s must be a whole number between 0 and 65535", key)
	}
	return uint32(n), true, nil
}

// boolArg reads an optional boolean.
func boolArg(args map[string]any, key string) (bool, bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return false, false, nil
	}
	v, isBool := raw.(bool)
	if !isBool {
		return false, false, fmt.Errorf("%s must be true or false", key)
	}
	return v, true, nil
}

// envArg reads an optional object of string values.
func envArg(args map[string]any, key string) (map[string]string, bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, false, nil
	}
	obj, isObject := raw.(map[string]any)
	if !isObject {
		return nil, false, fmt.Errorf("%s must be an object of string values", key)
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		s, isString := v.(string)
		if !isString {
			return nil, false, fmt.Errorf("%s.%s must be a string", key, k)
		}
		out[k] = s
	}
	return out, true, nil
}

func prettyJSON(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding result: %w", err)
	}
	return string(data), nil
}

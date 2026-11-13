package registry

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/terastal-hq/prot/internal/storage"
)

type sourceKey struct{}

// Sources recorded in the event log for service changes.
const (
	SourceAPI = "api" // the REST API: the CLI and the desktop app
	SourceMCP = "mcp" // an AI agent over MCP
)

// WithSource tags ctx with who is making a change, so the event log can tell an agent's edits from yours.
func WithSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

func sourceOf(ctx context.Context) string {
	if s, ok := ctx.Value(sourceKey{}).(string); ok && s != "" {
		return s
	}
	return SourceAPI
}

// eventPayload renders fields plus the change's source as the JSON payload of an event log entry.
func eventPayload(ctx context.Context, fields map[string]any) string {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["source"] = sourceOf(ctx)
	data, err := json.Marshal(fields)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// changedFields describes what an update changed. Values are included for everything except the environment,
// whose values may hold secrets: only its variable names are recorded.
func changedFields(before, after storage.Service) map[string]any {
	changed := map[string]any{}
	add := func(name string, b, a any) {
		if !reflect.DeepEqual(b, a) {
			changed[name] = a
		}
	}
	add("name", before.Name, after.Name)
	add("alias", before.Alias, after.Alias)
	add("command", before.Command, after.Command)
	add("cwd", before.Cwd, after.Cwd)
	add("port", before.Port, after.Port)
	add("auto_port", before.AutoPort, after.AutoPort)
	add("auto_start", before.AutoStart, after.AutoStart)
	add("restart_policy", before.RestartPolicy, after.RestartPolicy)
	add("lan_exposed", before.LANExposed, after.LANExposed)

	if !reflect.DeepEqual(before.Env, after.Env) && (len(before.Env) > 0 || len(after.Env) > 0) {
		names := make([]string, 0, len(after.Env))
		for k := range after.Env {
			names = append(names, k)
		}
		sort.Strings(names)
		changed["env_vars"] = names
	}
	return map[string]any{"changed": changed}
}

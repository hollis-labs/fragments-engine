package mcpserver

import (
	"fmt"
	"strings"

	gomcpserver "github.com/hollis-labs/go-mcp/server"
)

// argString, argFloat, argBool and argStringSlice replace mark3labs'
// CallToolRequest.GetString/GetFloat/GetBool/GetStringSlice: go-mcp hands a
// handler a plain map[string]any with no accessor methods of its own, so
// this file gets its own -- with the same per-type coercions mark3labs used,
// so behavior is unchanged by the port.

func argString(args map[string]any, key, defaultValue string) string {
	if val, ok := args[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return defaultValue
}

func requiredString(args map[string]any, key string) (string, error) {
	val, ok := args[key].(string)
	if !ok || strings.TrimSpace(val) == "" {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	return val, nil
}

func argFloat(args map[string]any, key string, defaultValue float64) float64 {
	if val, ok := args[key]; ok {
		switch v := val.(type) {
		case float64:
			return v
		case int:
			return float64(v)
		}
	}
	return defaultValue
}

func argBool(args map[string]any, key string, defaultValue bool) bool {
	if val, ok := args[key]; ok {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return defaultValue
}

func argStringSlice(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// propDef, strProp, numProp, boolProp, arrProp and inputSchema replace
// mark3labs' mcp.WithString/mcp.WithNumber/mcp.WithBoolean/mcp.WithArray/
// mcp.Required/mcp.Description chain: go-mcp's Tool.InputSchema is `any`
// with no functional-option builder of its own.
type propDef struct {
	name     string
	schema   map[string]any
	required bool
}

func strProp(name, desc string, required bool) propDef {
	return propDef{name: name, schema: map[string]any{"type": "string", "description": desc}, required: required}
}

func numProp(name, desc string, required bool) propDef {
	return propDef{name: name, schema: map[string]any{"type": "number", "description": desc}, required: required}
}

func boolProp(name, desc string, required bool) propDef {
	return propDef{name: name, schema: map[string]any{"type": "boolean", "description": desc}, required: required}
}

func arrProp(name, desc string, required bool) propDef {
	return propDef{name: name, schema: map[string]any{
		"type":        "array",
		"description": desc,
		"items":       map[string]any{"type": "string"},
	}, required: required}
}

func inputSchema(defs ...propDef) map[string]any {
	properties := make(map[string]any, len(defs))
	var required []string
	for _, d := range defs {
		properties[d.name] = d.schema
		if d.required {
			required = append(required, d.name)
		}
	}
	return gomcpserver.ObjectSchema(properties, required...)
}

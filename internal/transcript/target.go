package transcript

import (
	"encoding/json"
	"net/url"
	"strings"
)

const maxTargetBytes = 200

// toolTarget says what a tool call acted on, from its already redacted input: a file path, the start of a command,
// a URL host, a search pattern. It is bounded and empty when nothing recognisable is there.
func toolTarget(name, inputRedacted string) string {
	var in map[string]any
	if strings.TrimSpace(inputRedacted) == "" || json.Unmarshal([]byte(inputRedacted), &in) != nil {
		return ""
	}
	text := func(keys ...string) string {
		for _, k := range keys {
			switch v := in[k].(type) {
			case string:
				if s := strings.TrimSpace(v); s != "" {
					return s
				}
			case []any:
				var parts []string
				for _, p := range v {
					if s, ok := p.(string); ok {
						parts = append(parts, s)
					}
				}
				if len(parts) > 0 {
					return strings.Join(parts, " ")
				}
			}
		}
		return ""
	}
	var out string
	switch strings.ToLower(name) {
	case "bash", "shell", "exec_command", "shell_command", "local_shell", "run_command":
		out = commandStart(text("command", "cmd", "script"))
	case "webfetch", "web_fetch", "fetch":
		out = urlHost(text("url"))
	case "websearch", "web_search":
		out = text("query", "q")
	case "grep", "glob":
		out = text("pattern", "query")
		if p := text("path"); p != "" && out != "" {
			out += " in " + p
		}
	case "task", "agent":
		out = text("description", "subagent_type")
	default:
		out = text("file_path", "path", "notebook_path", "filepath")
		if out == "" {
			out = commandStart(text("command", "cmd"))
		}
		if out == "" {
			out = urlHost(text("url"))
		}
		if out == "" {
			out = text("pattern", "query", "description")
		}
	}
	if out == "" {
		out = text("file_path", "path")
	}
	return clip(out, maxTargetBytes)
}

func commandStart(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

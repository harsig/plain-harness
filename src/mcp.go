package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/harsig/harness-kit/text"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpConn struct {
	session     *mcp.ClientSession
	callTimeout time.Duration
	maxBytes    int
}

type route struct {
	conn *mcpConn
	tool string // tool name on the server
}

// Toolbox exposes the tools of all configured MCP servers as "<server>__<tool>".
type Toolbox struct {
	defs   []ToolDef
	routes map[string]route
	conns  []*mcpConn
}

func ConnectTools(ctx context.Context, servers []MCPServer) (*Toolbox, error) {
	tb := &Toolbox{routes: map[string]route{}}
	for _, s := range servers {
		if err := tb.add(ctx, s, transportFor(s)); err != nil {
			tb.Close()
			return nil, fmt.Errorf("mcp server %q: %w", s.Name, err)
		}
	}
	return tb, nil
}

func (tb *Toolbox) add(ctx context.Context, s MCPServer, transport mcp.Transport) error {
	connectCtx, cancel := context.WithTimeout(ctx, time.Duration(s.ConnectTimeoutSeconds)*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "plain-harness", Version: version}, nil)
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return err
	}
	conn := &mcpConn{session, time.Duration(s.CallTimeoutSeconds) * time.Second, s.MaxResultBytes}
	tb.conns = append(tb.conns, conn)

	for tool, err := range session.Tools(connectCtx, nil) {
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil || string(schema) == "null" {
			schema = []byte(`{"type":"object","properties":{}}`)
		}
		name := toolName(s.Name, tool.Name)
		if len(name) > maxToolNameLen {
			return fmt.Errorf("tool name %q is longer than %d characters", name, maxToolNameLen)
		}
		if _, dup := tb.routes[name]; dup {
			return fmt.Errorf("tool name %q collides with another tool after sanitizing", name)
		}
		tb.defs = append(tb.defs, ToolDef{Name: name, Description: tool.Description, Schema: schema})
		tb.routes[name] = route{conn, tool.Name}
	}
	return nil
}

func transportFor(s MCPServer) mcp.Transport {
	if s.HTTP != nil {
		return &mcp.StreamableClientTransport{
			Endpoint:   s.HTTP.URL,
			HTTPClient: &http.Client{Transport: headerTransport(s.HTTP.Headers)},
		}
	}
	cmd := exec.Command(s.Stdio.Command, s.Stdio.Args...)
	cmd.Dir = s.Stdio.dir
	cmd.Env = stdioEnv(s.Stdio.Env)
	cmd.Stderr = os.Stderr
	return &mcp.CommandTransport{Command: cmd}
}

// inheritedEnv is the part of the harness environment every stdio server gets,
// the same allowlist the official MCP SDKs use; secrets must be set in env.
var inheritedEnv = []string{"HOME", "LOGNAME", "PATH", "SHELL", "TERM", "USER"}

// stdioEnv returns the allowlisted harness variables, overridden or extended by env.
func stdioEnv(env map[string]string) []string {
	out := []string{} // non-nil: do not inherit the whole harness environment
	for _, k := range inheritedEnv {
		if _, set := env[k]; !set {
			if v, ok := os.LookupEnv(k); ok {
				out = append(out, k+"="+v)
			}
		}
	}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

type headerTransport map[string]string

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (tb *Toolbox) Defs() []ToolDef { return tb.defs }

// Call runs a tool and returns its text result; any error is meant to be shown to the model.
func (tb *Toolbox) Call(ctx context.Context, call ToolCall) (string, error) {
	rt, ok := tb.routes[call.Name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", call.Name)
	}
	args := map[string]any{}
	if strings.TrimSpace(call.Args) != "" {
		if err := json.Unmarshal([]byte(call.Args), &args); err != nil {
			return "", fmt.Errorf("arguments are not a valid JSON object: %w", err)
		}
	}
	if args == nil { // arguments were the JSON literal null
		args = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(ctx, rt.conn.callTimeout)
	defer cancel()
	res, err := rt.conn.session.CallTool(ctx, &mcp.CallToolParams{Name: rt.tool, Arguments: args})
	if err != nil {
		return "", err
	}
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		} else {
			parts = append(parts, "[non-text content omitted]")
		}
	}
	out := text.Truncate(strings.Join(parts, "\n"), rt.conn.maxBytes)
	if res.IsError {
		return "", fmt.Errorf("%s", out)
	}
	return out, nil
}

func (tb *Toolbox) Close() {
	for _, c := range tb.conns {
		c.session.Close()
	}
}

// maxToolNameLen is the limit both model APIs put on tool names.
const maxToolNameLen = 64

var toolNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// toolName builds the model-facing name; the APIs only accept [A-Za-z0-9_-].
func toolName(server, tool string) string {
	return server + "__" + toolNameUnsafe.ReplaceAllString(tool, "_")
}

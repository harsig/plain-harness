package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testToolbox connects to an in-memory MCP server named "t" with an "echo" tool.
func testToolbox(t *testing.T, maxResultBytes int) *Toolbox {
	t.Helper()
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args struct {
				Text string `json:"text"`
			}
			json.Unmarshal(req.Params.Arguments, &args)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil
		})
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	tb := &Toolbox{routes: map[string]route{}}
	s := MCPServer{Name: "t", ConnectTimeoutSeconds: 5, CallTimeoutSeconds: 5, MaxResultBytes: maxResultBytes}
	if err := tb.add(ctx, s, clientT); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tb.Close)
	return tb
}

func TestToolboxCall(t *testing.T) {
	tb := testToolbox(t, 5)
	if defs := tb.Defs(); len(defs) != 1 || defs[0].Name != "t__echo" {
		t.Fatalf("defs = %+v, want one tool t__echo", defs)
	}
	tests := []struct {
		call    ToolCall
		want    string
		wantErr string
	}{
		{call: ToolCall{Name: "t__echo", Args: `{"text":"hi"}`}, want: "hi"},
		{call: ToolCall{Name: "t__echo", Args: ""}, want: ""},
		{call: ToolCall{Name: "t__echo", Args: `{"text":"hello world"}`}, want: "hello\n[truncated after 5 bytes]"},
		{call: ToolCall{Name: "t__nope"}, wantErr: "unknown tool"},
		{call: ToolCall{Name: "t__echo", Args: "{"}, wantErr: "not a valid JSON object"},
	}
	for _, tt := range tests {
		got, err := tb.Call(context.Background(), tt.call)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("%+v: err = %v, want containing %q", tt.call, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%+v: got %q, %v; want %q", tt.call, got, err, tt.want)
		}
	}
}

func TestStdioTransport(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/h")
	t.Setenv("SECRET", "leak")
	for _, k := range []string{"LOGNAME", "SHELL", "TERM", "USER"} {
		t.Setenv(k, "") // restored after the test
		os.Unsetenv(k)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := `
model: {provider: openai_compatible, endpoint: http://x/v1, name: m}
mcp_servers:
  - name: tool
    stdio:
      command: ./bin/server
      args: [server.yml]
      env: {HOME: /override, TOKEN: t}
`
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := transportFor(c.MCPServers[0]).(*mcp.CommandTransport).Command

	if cmd.Dir != dir {
		t.Errorf("Dir = %q, want the config directory %q", cmd.Dir, dir)
	}
	if want := filepath.Join(dir, "bin/server"); cmd.Path != want {
		t.Errorf("Path = %q, want %q", cmd.Path, want)
	}
	env := slices.Sorted(slices.Values(cmd.Env))
	want := []string{"HOME=/override", "PATH=/usr/bin", "TOKEN=t"}
	if !slices.Equal(env, want) {
		t.Errorf("Env = %v, want %v", env, want)
	}
}

func TestTransportForGitServerPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := `
model: {provider: openai_compatible, endpoint: http://x/v1, name: m}
mcp_servers:
  - name: tool
    stdio:
      git: {url: "git@example.com:acme/tool.git"}
      command: ./bin/server
      args: [./server.yml, ../shared/x.yml, dist/main.js, --flag=./kept, "."]
`
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	clone := c.MCPServers[0].Stdio.Git.Dir
	cmd := transportFor(c.MCPServers[0]).(*mcp.CommandTransport).Command

	if cmd.Dir != clone {
		t.Errorf("Dir = %q, want the clone %q", cmd.Dir, clone)
	}
	if want := filepath.Join(clone, "bin/server"); cmd.Path != want {
		t.Errorf("Path = %q, want %q (inside the clone)", cmd.Path, want)
	}
	wantArgs := []string{filepath.Join(dir, "server.yml"), filepath.Join(filepath.Dir(dir), "shared/x.yml"), "dist/main.js", "--flag=./kept", "."}
	if got := cmd.Args[1:]; !slices.Equal(got, wantArgs) {
		t.Errorf("Args = %v, want %v", got, wantArgs)
	}
}

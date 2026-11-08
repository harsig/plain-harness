package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAnthropicMessages(t *testing.T) {
	msgs := []Message{
		{Role: roleUser, Text: "q"},
		{Role: roleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "a", Args: `{"x":1}`}, {ID: "2", Name: "b", Args: "not json"}}},
		{Role: roleTool, ToolCallID: "1", Text: "r1"},
		{Role: roleTool, ToolCallID: "2", Text: "r2", IsError: true},
		{Role: roleAssistant}, // empty: the API rejects it, so it is dropped
		{Role: roleUser, Text: "next"},
	}
	got, err := json.Marshal((&anthropic{}).messages(msgs))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"role":"user","content":[{"type":"text","text":"q"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"a","input":{"x":1}},{"type":"tool_use","id":"2","name":"b","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"r1"},{"type":"tool_result","tool_use_id":"2","content":"r2","is_error":true}]},` +
		`{"role":"user","content":[{"type":"text","text":"next"}]}]`
	if string(got) != want {
		t.Errorf("messages =\n%s\nwant\n%s", got, want)
	}
}

func TestAnthropicChat(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantText string
		wantArgs string
		wantErr  string
	}{
		{
			name:     "response",
			body:     `{"type":"message","content":[{"type":"text","text":"Hello"},{"type":"tool_use","id":"t1","name":"f","input":{"a":1}}],"stop_reason":"tool_use"}`,
			wantText: "Hello", wantArgs: `{"a":1}`,
		},
		{
			name: "stream",
			body: sse(
				`{"type":"message_start"}`,
				`{"type":"content_block_start","content_block":{"type":"text"}}`,
				`{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}`,
				`{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}`,
				`{"type":"content_block_stop"}`,
				`{"type":"content_block_start","content_block":{"type":"tool_use","id":"t1","name":"f"}}`,
				`{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`,
				`{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"1}"}}`,
				`{"type":"content_block_stop"}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
				`{"type":"message_stop"}`,
			),
			wantText: "Hello", wantArgs: `{"a":1}`,
		},
		{
			name:    "cut off while calling a tool",
			body:    `{"type":"message","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}],"stop_reason":"max_tokens"}`,
			wantErr: "max_tokens",
		},
		{
			name:    "stream error event",
			body:    sse(`{"type":"error","error":{"message":"overloaded"}}`),
			wantErr: "overloaded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, req := fakeAPI(t, http.StatusOK, tt.body)
			a := newAnthropic(ModelConfig{Endpoint: url, Name: "m", APIKey: "k", TimeoutSeconds: 5, MaxTokens: 100})
			var streamed strings.Builder
			var onText func(string)
			if strings.HasPrefix(tt.body, "data:") {
				onText = func(s string) { streamed.WriteString(s) }
			}
			reply, err := a.Chat(context.Background(), "sys", []Message{{Role: roleUser, Text: "q"}}, nil, onText)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reply.Text != tt.wantText || len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Args != tt.wantArgs {
				t.Errorf("reply = %+v, want text %q and one call with args %s", reply, tt.wantText, tt.wantArgs)
			}
			if onText != nil && streamed.String() != tt.wantText {
				t.Errorf("streamed %q, want %q", streamed.String(), tt.wantText)
			}
			body := req()
			if cc, _ := body["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
				t.Errorf("cache_control = %v, want automatic prompt caching", body["cache_control"])
			}
			if body["system"] != "sys" {
				t.Errorf("system = %v, want sys", body["system"])
			}
		})
	}
}

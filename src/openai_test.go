package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAIMessages(t *testing.T) {
	msgs := []Message{
		{Role: roleUser, Text: "q"},
		{Role: roleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "a", Args: `{"x":1}`}}},
		{Role: roleTool, ToolCallID: "1", Text: "r1"},
		{Role: roleAssistant, Text: "done"},
	}
	got, err := json.Marshal((&openAI{}).messages("sys", msgs))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"role":"system","content":"sys"},{"role":"user","content":"q"},` +
		`{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"a","arguments":"{\"x\":1}"}}]},` +
		`{"role":"tool","content":"r1","tool_call_id":"1"},{"role":"assistant","content":"done"}]`
	if string(got) != want {
		t.Errorf("messages =\n%s\nwant\n%s", got, want)
	}
}

func TestOpenAIChat(t *testing.T) {
	type call struct{ id, name, args string }
	tests := []struct {
		name      string
		body      string
		wantText  string
		wantCalls []call
		wantErr   string
	}{
		{
			name:      "response without call id",
			body:      `{"choices":[{"message":{"content":"Hi","tool_calls":[{"function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			wantText:  "Hi",
			wantCalls: []call{{"call_0", "f", "{}"}},
		},
		{
			name: "stream with index",
			body: sse(
				`{"choices":[{"delta":{"content":"Hi"}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			),
			wantText:  "Hi",
			wantCalls: []call{{"c1", "f", `{"a":1}`}},
		},
		{
			name: "stream without index, name repeated",
			body: sse(
				`{"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"f","arguments":"{\"x\""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"f","arguments":":1}"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"id":"b","function":{"name":"g","arguments":"{}"}}]}}]}`,
				`[DONE]`,
			),
			wantCalls: []call{{"a", "f", `{"x":1}`}, {"b", "g", "{}"}},
		},
		{
			name:    "cut off while calling a tool",
			body:    `{"choices":[{"message":{"tool_calls":[{"id":"a","function":{"name":"f","arguments":"{"}}]},"finish_reason":"length"}]}`,
			wantErr: "token limit",
		},
		{
			name:    "api error",
			body:    `{"error":{"message":"bad model"}}`,
			wantErr: "bad model",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, _ := fakeAPI(t, http.StatusOK, tt.body)
			o := newOpenAI(ModelConfig{Endpoint: url, Name: "m", TimeoutSeconds: 5})
			var onText func(string)
			if strings.HasPrefix(tt.body, "data:") {
				onText = func(string) {}
			}
			reply, err := o.Chat(context.Background(), "", []Message{{Role: roleUser, Text: "q"}}, nil, onText)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reply.Text != tt.wantText {
				t.Errorf("text = %q, want %q", reply.Text, tt.wantText)
			}
			var got []call
			for _, c := range reply.ToolCalls {
				got = append(got, call{c.ID, c.Name, c.Args})
			}
			if len(got) != len(tt.wantCalls) {
				t.Fatalf("calls = %v, want %v", got, tt.wantCalls)
			}
			for i := range got {
				if got[i] != tt.wantCalls[i] {
					t.Errorf("call %d = %v, want %v", i, got[i], tt.wantCalls[i])
				}
			}
		})
	}
}

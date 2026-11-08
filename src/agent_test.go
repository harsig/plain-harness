package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// step is one scripted model turn: optional streamed text, then a reply or an error.
type step struct {
	emit  string
	reply Message
	err   error
}

type fakeProvider struct {
	steps []step
	calls int
}

func (f *fakeProvider) Chat(_ context.Context, _ string, _ []Message, _ []ToolDef, onText func(string)) (Message, error) {
	s := f.steps[min(f.calls, len(f.steps)-1)]
	f.calls++
	if s.emit != "" && onText != nil {
		onText(s.emit)
	}
	return s.reply, s.err
}

func roles(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Role)
	}
	return out
}

func TestAgentRun(t *testing.T) {
	echo := Message{Role: roleAssistant, ToolCalls: []ToolCall{
		{ID: "1", Name: "t__echo", Args: `{"text":"hi"}`},
		{ID: "2", Name: "t__nope"},
	}}
	answer := Message{Role: roleAssistant, Text: "done"}
	transient := retryableError{error: errors.New("503"), after: time.Millisecond}

	tests := []struct {
		name      string
		steps     []step
		stream    bool
		wantCalls int
		wantRoles []string
		wantErr   string
	}{
		{
			name:      "tools until answer",
			steps:     []step{{reply: echo}, {reply: answer}},
			wantCalls: 2,
			wantRoles: []string{roleUser, roleAssistant, roleTool, roleTool, roleAssistant},
		},
		{
			name:      "max iterations",
			steps:     []step{{reply: echo}},
			wantCalls: 2,
			wantRoles: []string{roleUser, roleAssistant, roleTool, roleTool, roleAssistant, roleTool, roleTool},
			wantErr:   "max_iterations",
		},
		{
			name:      "retry transient error",
			steps:     []step{{err: transient}, {reply: answer}},
			wantCalls: 2,
			wantRoles: []string{roleUser, roleAssistant},
		},
		{
			name:      "no retry after streamed text",
			steps:     []step{{emit: "partial", err: transient}, {reply: answer}},
			stream:    true,
			wantCalls: 1,
			wantRoles: []string{roleUser},
			wantErr:   "503",
		},
		{
			name:      "no retry on permanent error",
			steps:     []step{{err: errors.New("HTTP 400")}, {reply: answer}},
			wantCalls: 1,
			wantRoles: []string{roleUser},
			wantErr:   "HTTP 400",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakeProvider{steps: tt.steps}
			a := &Agent{provider: p, tools: testToolbox(t, 100), maxIterations: 2}
			var onText func(string)
			if tt.stream {
				onText = func(string) {}
			}
			history, got, err := a.Run(context.Background(), nil, "q", onText)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
			} else if err != nil || got != "done" {
				t.Fatalf("Run = %q, %v; want done", got, err)
			}
			if p.calls != tt.wantCalls {
				t.Errorf("model calls = %d, want %d", p.calls, tt.wantCalls)
			}
			if r := roles(history); !slices.Equal(r, tt.wantRoles) {
				t.Errorf("history roles = %v, want %v", r, tt.wantRoles)
			}
		})
	}
}

func TestAgentToolResults(t *testing.T) {
	p := &fakeProvider{steps: []step{{reply: Message{Role: roleAssistant, ToolCalls: []ToolCall{
		{ID: "1", Name: "t__echo", Args: `{"text":"hi"}`},
		{ID: "2", Name: "t__nope"},
	}}}, {reply: Message{Role: roleAssistant, Text: "done"}}}}
	a := &Agent{provider: p, tools: testToolbox(t, 100), maxIterations: 5}
	history, _, err := a.Run(context.Background(), nil, "q", nil)
	if err != nil {
		t.Fatal(err)
	}
	ok, failed := history[2], history[3]
	if ok.ToolCallID != "1" || ok.Text != "hi" || ok.IsError {
		t.Errorf("result 1 = %+v, want text hi", ok)
	}
	if failed.ToolCallID != "2" || !failed.IsError || !strings.Contains(failed.Text, "unknown tool") {
		t.Errorf("result 2 = %+v, want an error result the model can read", failed)
	}
}

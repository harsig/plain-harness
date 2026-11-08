package main

import (
	"context"
	"encoding/json"
)

const (
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"
)

// Message is the provider-neutral conversation unit; it is also the session file format.
type Message struct {
	Role       string     `json:"role"`
	Text       string     `json:"text,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant only
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool only
	IsError    bool       `json:"is_error,omitempty"`     // tool only
}

// ToolCall.Args is the raw argument text as produced by the model (may be invalid JSON).
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Provider performs one model turn. A non-nil onText enables streaming and receives text deltas.
type Provider interface {
	Chat(ctx context.Context, system string, msgs []Message, tools []ToolDef, onText func(string)) (Message, error)
}

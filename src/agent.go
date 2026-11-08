package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

const maxAttempts = 3

type Agent struct {
	provider      Provider
	tools         *Toolbox
	system        string
	maxIterations int
}

// Run appends prompt to history and loops until the model answers without tool calls.
// The returned history is always consistent and safe to persist, even on error.
func (a *Agent) Run(ctx context.Context, history []Message, prompt string, onText func(string)) ([]Message, string, error) {
	history = append(history, Message{Role: roleUser, Text: prompt})
	for i := 0; i < a.maxIterations; i++ {
		reply, err := a.chat(ctx, history, onText)
		if err != nil {
			return history, "", err
		}
		if onText != nil && reply.Text != "" {
			onText("\n")
		}
		history = append(history, reply)
		if len(reply.ToolCalls) == 0 {
			return history, reply.Text, nil
		}
		for _, call := range reply.ToolCalls {
			history = append(history, a.callTool(ctx, call))
		}
	}
	return history, "", fmt.Errorf("stopped after max_iterations (%d) without a final answer", a.maxIterations)
}

// callTool turns tool failures into error results so the model can recover.
func (a *Agent) callTool(ctx context.Context, call ToolCall) Message {
	log.Printf("tool call: %s", call.Name)
	out, err := a.tools.Call(ctx, call)
	res := Message{Role: roleTool, ToolCallID: call.ID, Text: out}
	if err != nil {
		log.Printf("tool %s failed: %v", call.Name, err)
		res.Text, res.IsError = "error: "+err.Error(), true
	}
	return res
}

// chat retries transient failures, but never once text has been streamed out.
func (a *Agent) chat(ctx context.Context, history []Message, onText func(string)) (Message, error) {
	emitted := false
	var emit func(string)
	if onText != nil {
		emit = func(s string) { emitted = true; onText(s) }
	}
	for attempt := 1; ; attempt++ {
		reply, err := a.provider.Chat(ctx, a.system, history, a.tools.Defs(), emit)
		var retryable retryableError
		if err == nil || emitted || attempt == maxAttempts || !errors.As(err, &retryable) {
			return reply, err
		}
		log.Printf("model request failed (attempt %d/%d): %v", attempt, maxAttempts, err)
		delay := time.Duration(attempt) * time.Second
		if retryable.after > 0 {
			delay = min(retryable.after, 30*time.Second)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return Message{}, ctx.Err()
		}
	}
}

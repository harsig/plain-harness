package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"
)

type anthropic struct {
	api       apiClient
	endpoint  string
	model     string
	maxTokens int
}

func newAnthropic(m ModelConfig) *anthropic {
	h := map[string]string{"anthropic-version": "2023-06-01"}
	if m.APIKey != "" {
		h["x-api-key"] = m.APIKey
	}
	return &anthropic{
		api:       apiClient{timeout: time.Duration(m.TimeoutSeconds) * time.Second, headers: h},
		endpoint:  strings.TrimRight(m.Endpoint, "/"),
		model:     m.Name,
		maxTokens: m.MaxTokens,
	}
}

// anBlock covers the content block kinds used in requests, responses and stream events.
type anBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anMessage struct {
	Role    string    `json:"role"`
	Content []anBlock `json:"content"`
}

type anEvent struct {
	Type         string    `json:"type"`
	ContentBlock anBlock   `json:"content_block"`
	Content      []anBlock `json:"content"`
	StopReason   string    `json:"stop_reason"` // non-stream response
	Delta        struct {
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"` // message_delta event
	} `json:"delta"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (a *anthropic) Chat(ctx context.Context, system string, msgs []Message, tools []ToolDef, onText func(string)) (Message, error) {
	stream := onText != nil
	body := map[string]any{
		"model": a.model, "max_tokens": a.maxTokens, "messages": a.messages(msgs), "stream": stream,
		// Automatic prompt caching: the API puts the breakpoint on the last block, so each
		// turn of the agent loop reads the previous turns from the cache.
		"cache_control": map[string]string{"type": "ephemeral"},
	}
	if system != "" {
		body["system"] = system
	}
	if len(tools) > 0 {
		body["tools"] = a.tools(tools)
	}

	reply := Message{Role: roleAssistant}
	var cur *ToolCall
	var stop string
	// handle folds a non-stream response or a stream event into the reply.
	handle := func(ev anEvent) error {
		if ev.Error != nil {
			return fmt.Errorf("api error: %s", ev.Error.Message)
		}
		if ev.StopReason != "" {
			stop = ev.StopReason
		}
		if ev.Delta.StopReason != "" {
			stop = ev.Delta.StopReason
		}
		switch ev.Type {
		case "message": // non-stream response
			for _, b := range ev.Content {
				switch b.Type {
				case "text":
					reply.Text += b.Text
				case "tool_use":
					reply.ToolCalls = append(reply.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: string(b.Input)})
				}
			}
		case "content_block_start":
			if b := ev.ContentBlock; b.Type == "tool_use" {
				reply.ToolCalls = append(reply.ToolCalls, ToolCall{ID: b.ID, Name: b.Name})
				cur = &reply.ToolCalls[len(reply.ToolCalls)-1]
			}
		case "content_block_delta":
			if ev.Delta.Text != "" {
				reply.Text += ev.Delta.Text
				onText(ev.Delta.Text)
			}
			if ev.Delta.PartialJSON != "" && cur != nil {
				cur.Args += ev.Delta.PartialJSON
			}
		case "content_block_stop":
			cur = nil
		}
		return nil
	}

	err := a.api.post(ctx, a.endpoint+"/v1/messages", body, stream, func(r io.Reader) error {
		if !stream {
			var ev anEvent
			if err := json.NewDecoder(r).Decode(&ev); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			return handle(ev)
		}
		return readSSE(r, func(data []byte) error {
			var ev anEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				return fmt.Errorf("decode stream event: %w", err)
			}
			if err := handle(ev); err != nil {
				return err
			}
			if ev.Type == "message_stop" {
				return io.EOF
			}
			return nil
		})
	})
	if err == nil && stop == "max_tokens" {
		if len(reply.ToolCalls) > 0 {
			return reply, fmt.Errorf("response cut off by max_tokens (%d) while calling tools; raise model.max_tokens", a.maxTokens)
		}
		log.Printf("warning: response cut off by max_tokens (%d)", a.maxTokens)
	}
	return reply, err
}

func (a *anthropic) messages(msgs []Message) []anMessage {
	var out []anMessage
	for _, m := range msgs {
		switch m.Role {
		case roleUser:
			out = append(out, anMessage{Role: "user", Content: []anBlock{{Type: "text", Text: m.Text}}})
		case roleAssistant:
			var blocks []anBlock
			if m.Text != "" {
				blocks = append(blocks, anBlock{Type: "text", Text: m.Text})
			}
			for _, tc := range m.ToolCalls {
				in := json.RawMessage(tc.Args)
				if !json.Valid(in) {
					in = json.RawMessage("{}")
				}
				blocks = append(blocks, anBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: in})
			}
			if len(blocks) == 0 { // the API rejects empty content
				continue
			}
			out = append(out, anMessage{Role: "assistant", Content: blocks})
		case roleTool:
			res := anBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Text, IsError: m.IsError}
			// Consecutive tool results must share one user message.
			if n := len(out); n > 0 && out[n-1].Role == "user" && out[n-1].Content[0].Type == "tool_result" {
				out[n-1].Content = append(out[n-1].Content, res)
			} else {
				out = append(out, anMessage{Role: "user", Content: []anBlock{res}})
			}
		}
	}
	return out
}

func (a *anthropic) tools(defs []ToolDef) []any {
	out := make([]any, len(defs))
	for i, d := range defs {
		out[i] = map[string]any{"name": d.Name, "description": d.Description, "input_schema": d.Schema}
	}
	return out
}

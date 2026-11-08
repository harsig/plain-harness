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

type openAI struct {
	api      apiClient
	endpoint string
	model    string
}

func newOpenAI(m ModelConfig) *openAI {
	h := map[string]string{}
	if m.APIKey != "" {
		h["Authorization"] = "Bearer " + m.APIKey
	}
	return &openAI{
		api:      apiClient{timeout: time.Duration(m.TimeoutSeconds) * time.Second, headers: h},
		endpoint: strings.TrimRight(m.Endpoint, "/"),
		model:    m.Name,
	}
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    *string       `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	Index    *int   `json:"index,omitempty"` // absent from some servers' stream deltas
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// oaiReply is the model output, either a full message or a streamed delta.
type oaiReply struct {
	Content   string        `json:"content"`
	ToolCalls []oaiToolCall `json:"tool_calls"`
}

type oaiResponse struct {
	Choices []struct {
		Message      oaiReply `json:"message"`
		Delta        oaiReply `json:"delta"`
		FinishReason string   `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *openAI) Chat(ctx context.Context, system string, msgs []Message, tools []ToolDef, onText func(string)) (Message, error) {
	stream := onText != nil
	body := map[string]any{"model": o.model, "messages": o.messages(system, msgs), "stream": stream}
	if len(tools) > 0 {
		body["tools"] = o.tools(tools)
	}

	var text strings.Builder
	var calls []oaiToolCall
	var finish string
	// merge folds a message or streamed delta into the accumulated reply.
	merge := func(m oaiReply) {
		text.WriteString(m.Content)
		for _, tc := range m.ToolCalls {
			idx := len(calls) - 1 // default: continuation of the latest call
			switch {
			case tc.Index != nil:
				idx = *tc.Index
			case tc.ID != "":
				idx = len(calls)
				for i, c := range calls {
					if c.ID == tc.ID {
						idx = i
					}
				}
			case idx < 0:
				idx = 0
			}
			for len(calls) <= idx {
				calls = append(calls, oaiToolCall{})
			}
			c := &calls[idx]
			if tc.ID != "" {
				c.ID = tc.ID
			}
			if tc.Function.Name != c.Function.Name { // some servers repeat the full name in every delta
				c.Function.Name += tc.Function.Name
			}
			c.Function.Arguments += tc.Function.Arguments
		}
	}

	err := o.api.post(ctx, o.endpoint+"/chat/completions", body, stream, func(r io.Reader) error {
		if !stream {
			var resp oaiResponse
			if err := json.NewDecoder(r).Decode(&resp); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			if resp.Error != nil {
				return fmt.Errorf("api error: %s", resp.Error.Message)
			}
			if len(resp.Choices) == 0 {
				return fmt.Errorf("response has no choices")
			}
			m := resp.Choices[0].Message
			for i := range m.ToolCalls {
				ix := i
				m.ToolCalls[i].Index = &ix
			}
			merge(m)
			finish = resp.Choices[0].FinishReason
			return nil
		}
		return readSSE(r, func(data []byte) error {
			if string(data) == "[DONE]" {
				return io.EOF
			}
			var resp oaiResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				return fmt.Errorf("decode stream chunk: %w", err)
			}
			if resp.Error != nil {
				return fmt.Errorf("api error: %s", resp.Error.Message)
			}
			if len(resp.Choices) == 0 {
				return nil
			}
			d := resp.Choices[0].Delta
			merge(d)
			if f := resp.Choices[0].FinishReason; f != "" {
				finish = f
			}
			if d.Content != "" {
				onText(d.Content)
			}
			return nil
		})
	})
	if err != nil {
		return Message{}, err
	}

	reply := Message{Role: roleAssistant, Text: text.String()}
	for i, c := range calls {
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		reply.ToolCalls = append(reply.ToolCalls, ToolCall{ID: c.ID, Name: c.Function.Name, Args: c.Function.Arguments})
	}
	if finish == "length" {
		if len(reply.ToolCalls) > 0 {
			return Message{}, fmt.Errorf("response cut off by the token limit while calling tools")
		}
		log.Printf("warning: response cut off by the token limit")
	}
	return reply, nil
}

func (o *openAI) messages(system string, msgs []Message) []oaiMessage {
	var out []oaiMessage
	if system != "" {
		out = append(out, oaiMessage{Role: "system", Content: &system})
	}
	for _, m := range msgs {
		om := oaiMessage{Role: m.Role, ToolCallID: m.ToolCallID}
		if m.Text != "" || m.Role != roleAssistant || len(m.ToolCalls) == 0 {
			text := m.Text
			om.Content = &text
		}
		for _, tc := range m.ToolCalls {
			c := oaiToolCall{ID: tc.ID, Type: "function"}
			c.Function.Name, c.Function.Arguments = tc.Name, tc.Args
			om.ToolCalls = append(om.ToolCalls, c)
		}
		out = append(out, om)
	}
	return out
}

func (o *openAI) tools(defs []ToolDef) []any {
	out := make([]any, len(defs))
	for i, d := range defs {
		out[i] = map[string]any{"type": "function", "function": map[string]any{
			"name": d.Name, "description": d.Description, "parameters": d.Schema,
		}}
	}
	return out
}

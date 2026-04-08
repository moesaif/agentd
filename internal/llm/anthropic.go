package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type AnthropicClient struct {
	apiKey string
	model  string
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicContentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *AnthropicClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	messages := make([]anthropicMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, anthropicMessage{Role: m.Role, Content: m.Content})
	}

	if len(messages) == 0 {
		messages = append(messages, anthropicMessage{Role: "user", Content: "Process the event."})
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := anthropicRequest{
		Model:     c.model,
		MaxTokens: maxTokens,
		System:    req.SystemPrompt,
		Messages:  messages,
	}

	if len(req.Tools) > 0 {
		tools := make([]anthropicTool, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.Parameters,
			}
		}
		body.Tools = tools
	}

	data, err := json.Marshal(body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(data))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("reading response: %w", err)
	}

	var result anthropicResponse
	if err := json.Unmarshal(respData, &result); err != nil {
		return CompletionResponse{}, fmt.Errorf("parsing response: %w", err)
	}

	if result.Error != nil {
		return CompletionResponse{}, fmt.Errorf("API error: %s", result.Error.Message)
	}

	if len(result.Content) == 0 {
		return CompletionResponse{}, fmt.Errorf("no content in response")
	}

	var text string
	var toolCalls []ToolCall
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			text = block.Text
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				Name:  block.Name,
				Input: block.Input,
			})
		}
	}

	return CompletionResponse{
		Content:    text,
		ToolCalls:  toolCalls,
		TokensUsed: result.Usage.InputTokens + result.Usage.OutputTokens,
	}, nil
}

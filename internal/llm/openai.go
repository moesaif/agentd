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

type OpenAIClient struct {
	apiKey  string
	model   string
	baseURL string
}

type openaiToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type openaiTool struct {
	Type     string              `json:"type"`
	Function openaiToolFunction  `json:"function"`
}

type openaiToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature"`
	Tools       []openaiTool    `json:"tools,omitempty"`
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiResponse struct {
	Choices []struct {
		Message struct {
			Content   string           `json:"content"`
			ToolCalls []openaiToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *OpenAIClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	messages := make([]openaiMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, openaiMessage{Role: "system", Content: req.SystemPrompt})
	}
	for _, m := range req.Messages {
		messages = append(messages, openaiMessage{Role: m.Role, Content: m.Content})
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := openaiRequest{
		Model:       c.model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
	}

	if len(req.Tools) > 0 {
		tools := make([]openaiTool, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = openaiTool{
				Type: "function",
				Function: openaiToolFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			}
		}
		body.Tools = tools
	}

	data, err := json.Marshal(body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

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

	var result openaiResponse
	if err := json.Unmarshal(respData, &result); err != nil {
		return CompletionResponse{}, fmt.Errorf("parsing response: %w", err)
	}

	if result.Error != nil {
		return CompletionResponse{}, fmt.Errorf("API error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("no choices in response")
	}

	msg := result.Choices[0].Message
	var toolCalls []ToolCall
	for _, tc := range msg.ToolCalls {
		var input map[string]any
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
		toolCalls = append(toolCalls, ToolCall{
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	return CompletionResponse{
		Content:    msg.Content,
		ToolCalls:  toolCalls,
		TokensUsed: result.Usage.TotalTokens,
	}, nil
}

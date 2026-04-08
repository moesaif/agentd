package llm

import (
	"context"
	"fmt"

	"github.com/moesaif/agentd/internal/config"
)

type Client interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema object
}

type ToolCall struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type CompletionRequest struct {
	SystemPrompt string
	Messages     []Message
	MaxTokens    int
	Temperature  float64
	Tools        []ToolDefinition // optional; enables native tool-use
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CompletionResponse struct {
	Content    string
	ToolCalls  []ToolCall // populated when the model uses tools
	TokensUsed int
}

func NewClient(cfg config.LLMConfig) (Client, error) {
	switch cfg.Provider {
	case "openai":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return &OpenAIClient{
			apiKey:  cfg.APIKey,
			model:   cfg.Model,
			baseURL: baseURL,
		}, nil
	case "anthropic":
		return &AnthropicClient{
			apiKey: cfg.APIKey,
			model:  cfg.Model,
		}, nil
	case "ollama", "openai-compatible":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434/v1"
		}
		apiKey := cfg.APIKey
		if apiKey == "" {
			apiKey = "ollama"
		}
		return &OpenAIClient{
			apiKey:  apiKey,
			model:   cfg.Model,
			baseURL: baseURL,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.Provider)
	}
}

package llm

import (
	"context"
	"fmt"

	"github.com/moesaif/agentd/internal/config"
)

type Client interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

type CompletionRequest struct {
	SystemPrompt string
	Messages     []Message
	MaxTokens    int
	Temperature  float64
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CompletionResponse struct {
	Content    string
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
	case "ollama":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434/v1"
		}
		return &OpenAIClient{
			apiKey:  "ollama",
			model:   cfg.Model,
			baseURL: baseURL,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.Provider)
	}
}

// Package ai provides LLM client construction and the review tool set.
package ai

import (
	"fmt"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/openai"
)

// Supported LLM providers.
const (
	// ProviderOpenAI selects the OpenAI client.
	ProviderOpenAI = "openai"
	// ProviderAnthropic selects the Anthropic client.
	ProviderAnthropic = "anthropic"
)

// NewLLM creates an LLM client for the given provider and API key
// (FR-11 uses this for agent reasoning). provider must be "openai" or
// "anthropic".
func NewLLM(provider, apiKey string) (llms.LLM, error) {
	switch provider {
	case ProviderOpenAI:
		llm, err := openai.New(openai.WithToken(apiKey))
		if err != nil {
			return nil, fmt.Errorf("create openai client: %w", err)
		}
		return llm, nil
	case ProviderAnthropic:
		llm, err := anthropic.New(anthropic.WithToken(apiKey))
		if err != nil {
			return nil, fmt.Errorf("create anthropic client: %w", err)
		}
		return llm, nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider %q (want %q or %q)",
			provider, ProviderOpenAI, ProviderAnthropic)
	}
}

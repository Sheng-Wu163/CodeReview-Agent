// Package ai provides LLM client construction and the review tool set.
package ai

import (
	"context"
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
//
// The returned client strips per-call stop sequences: LangChainGo's ReAct
// agent templates inject "\nObservation:" stop words, but reasoning models
// echo "Observation:" inside their hidden reasoning, so the server
// truncates generation mid-reasoning and returns an empty final content
// (the agent loop then fails with "unable to parse agent output"). The
// externalized prompts already pin the output format, so the stop words
// are unnecessary.
func NewLLM(provider, apiKey string) (llms.LLM, error) {
	switch provider {
	case ProviderOpenAI:
		llm, err := openai.New(openai.WithToken(apiKey))
		if err != nil {
			return nil, fmt.Errorf("create openai client: %w", err)
		}
		return stripStopWords{Model: llm}, nil
	case ProviderAnthropic:
		llm, err := anthropic.New(anthropic.WithToken(apiKey))
		if err != nil {
			return nil, fmt.Errorf("create anthropic client: %w", err)
		}
		return stripStopWords{Model: llm}, nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider %q (want %q or %q)",
			provider, ProviderOpenAI, ProviderAnthropic)
	}
}

// stripStopWords wraps an llms.Model and clears any stop sequences the
// caller (e.g. the ReAct agent templates) set. See NewLLM for the rationale.
type stripStopWords struct {
	llms.Model
}

// GenerateContent implements llms.Model, clearing stop words.
func (m stripStopWords) GenerateContent(ctx context.Context, msgs []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	return m.Model.GenerateContent(ctx, msgs, withoutStopWords(options)...)
}

// Call implements llms.Model, clearing stop words.
func (m stripStopWords) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return m.Model.Call(ctx, prompt, withoutStopWords(options)...)
}

// withoutStopWords copies options and appends a nil stop-word option so it
// wins over any previously supplied stop sequences.
func withoutStopWords(options []llms.CallOption) []llms.CallOption {
	out := make([]llms.CallOption, 0, len(options)+1)
	out = append(out, options...)
	return append(out, llms.WithStopWords(nil))
}

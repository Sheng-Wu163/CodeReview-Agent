package ai

import (
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestNewLLM(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		apiKey   string
		wantErr  bool
		wantType bool
		setupEnv func(*testing.T)
	}{
		{
			name:     "openai provider",
			provider: ProviderOpenAI,
			apiKey:   "test-key",
			wantType: true,
		},
		{
			name:     "anthropic provider",
			provider: ProviderAnthropic,
			apiKey:   "test-key",
			wantType: true,
		},
		{
			name:     "openai with empty key errors",
			provider: ProviderOpenAI,
			apiKey:   "",
			wantErr:  true,
			setupEnv: func(t *testing.T) { t.Setenv("OPENAI_API_KEY", "") },
		},
		{
			name:     "unknown provider",
			provider: "mistral",
			apiKey:   "test-key",
			wantErr:  true,
		},
		{
			name:     "empty provider",
			provider: "",
			apiKey:   "test-key",
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupEnv != nil {
				tt.setupEnv(t)
			}
			llm, err := NewLLM(tt.provider, tt.apiKey)
			if tt.wantErr {
				if err == nil {
					t.Errorf("NewLLM(%q) = %v, want error", tt.provider, llm)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewLLM(%q): %v", tt.provider, err)
			}
			if tt.wantType && llm == nil {
				t.Errorf("NewLLM(%q) = nil, want non-nil llms.LLM", tt.provider)
			}
			var _ llms.LLM = llm
		})
	}
}

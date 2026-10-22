package context

import "testing"

// blockWithTokens builds a test block with an explicit token count so no
// real estimation runs over large text (CLAUDE.v.1.9.md).
func blockWithTokens(source string, tokens int) ContextBlock {
	return ContextBlock{Source: source, Content: source, Tokens: tokens}
}

func TestShouldCompress(t *testing.T) {
	cfg := CompressionConfig{
		TriggerRatio:       DefaultTriggerRatio,
		AbsoluteTokenLimit: DefaultAbsoluteTokenLimit,
		KeepRecentRounds:   DefaultKeepRecentRounds,
	}

	tests := []struct {
		name    string
		tokens  int
		window  int
		cfg     CompressionConfig
		want    bool
		comment string
	}{
		{
			name: "ratio trigger at 65% of 8K", tokens: 5300, window: 8000, cfg: cfg, want: true,
			comment: "AC-107",
		},
		{
			name: "exactly at ratio does not trigger", tokens: 5200, window: 8000, cfg: cfg, want: false,
			comment: "strict greater-than",
		},
		{
			name: "absolute trigger on 128K window", tokens: 33000, window: 131072, cfg: cfg, want: true,
			comment: "AC-108",
		},
		{
			name: "below both thresholds", tokens: 10000, window: 131072, cfg: cfg, want: false,
		},
		{
			name: "zero config uses defaults", tokens: 33000, window: 131072,
			cfg: CompressionConfig{}, want: true,
		},
		{
			name: "zero window disables ratio trigger", tokens: 5300, window: 0, cfg: cfg, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := []ContextBlock{blockWithTokens(SourceToolOutput, tt.tokens)}
			if got := ShouldCompress(blocks, tt.window, tt.cfg); got != tt.want {
				t.Errorf("ShouldCompress(%d, %d) = %v, want %v (%s)",
					tt.tokens, tt.window, got, tt.want, tt.comment)
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
	}
	if got := EstimateTokens("12345678"); got != 2 {
		t.Errorf("EstimateTokens(8 chars) = %d, want 2", got)
	}
	// Explicit Tokens must win over estimation.
	if got := BlockTokens(ContextBlock{Content: "12345678", Tokens: 99}); got != 99 {
		t.Errorf("BlockTokens = %d, want explicit 99", got)
	}
}

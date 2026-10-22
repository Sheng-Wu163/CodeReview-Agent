package context

import "unicode/utf8"

// Compression defaults (FR-53, FR-54).
const (
	// DefaultTriggerRatio is the default fraction of the model window that
	// triggers compression.
	DefaultTriggerRatio = 0.65
	// DefaultAbsoluteTokenLimit is the default absolute token ceiling.
	DefaultAbsoluteTokenLimit = 32000
	// DefaultKeepRecentRounds is the default number of newest tool outputs
	// kept intact by the sliding window.
	DefaultKeepRecentRounds = 2
)

// CompressionConfig controls the dual-track compression trigger (v1.9 spec
// 18.3, FROZEN).
type CompressionConfig struct {
	// TriggerRatio is the fraction of the model context window that
	// triggers compression (default 0.65).
	TriggerRatio float64
	// AbsoluteTokenLimit is the absolute token ceiling (default 32000).
	AbsoluteTokenLimit int
	// KeepRecentRounds is the number of newest tool-output rounds kept
	// intact during compression (default 2).
	KeepRecentRounds int
}

// withDefaults fills zero values with the spec defaults.
func (c CompressionConfig) withDefaults() CompressionConfig {
	if c.TriggerRatio <= 0 {
		c.TriggerRatio = DefaultTriggerRatio
	}
	if c.AbsoluteTokenLimit <= 0 {
		c.AbsoluteTokenLimit = DefaultAbsoluteTokenLimit
	}
	if c.KeepRecentRounds <= 0 {
		c.KeepRecentRounds = DefaultKeepRecentRounds
	}
	return c
}

// EstimateTokens returns a lightweight token estimate without a real
// tokenizer: roughly four characters per token (CLAUDE.v.1.9.md).
func EstimateTokens(content string) int {
	if content == "" {
		return 0
	}
	return (utf8.RuneCountInString(content) + 3) / 4
}

// BlockTokens returns the block's explicit token count, or a lightweight
// estimate when it was not provided.
func BlockTokens(block ContextBlock) int {
	if block.Tokens > 0 {
		return block.Tokens
	}
	return EstimateTokens(block.Content)
}

// TotalTokens sums the token count of every block.
func TotalTokens(blocks []ContextBlock) int {
	total := 0
	for _, block := range blocks {
		total += BlockTokens(block)
	}
	return total
}

// ShouldCompress implements the dual-track trigger (FR-53): compression is
// needed when the current tokens exceed either the ratio of the model
// context window or the absolute token limit.
func ShouldCompress(blocks []ContextBlock, modelContextWindow int, cfg CompressionConfig) bool {
	cfg = cfg.withDefaults()
	total := TotalTokens(blocks)
	if modelContextWindow > 0 && float64(total) > cfg.TriggerRatio*float64(modelContextWindow) {
		return true
	}
	return total > cfg.AbsoluteTokenLimit
}

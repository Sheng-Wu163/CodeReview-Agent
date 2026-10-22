package context

import (
	"strings"
	"testing"
)

func toolBlock(sequence int, content string, tokens int) ContextBlock {
	return ContextBlock{
		Source:   SourceToolOutput,
		Content:  content,
		Tokens:   tokens,
		Sequence: sequence,
	}
}

func TestCompressorKeepsNewestTwoRounds(t *testing.T) {
	// Deliberately shuffled: compression must use Sequence, not indexes.
	blocks := []ContextBlock{
		toolBlock(5, "file5.go full content", 100),
		toolBlock(2, "file2.go full content", 100),
		toolBlock(4, "file4.go full content", 100),
		toolBlock(1, "file1.go full content", 100),
		toolBlock(3, "file3.go full content", 100),
		{Source: SourceSystem, Content: "system prompt", Tokens: 10, IsPinned: true, Sequence: 0},
	}

	c := NewCompressor(CompressionConfig{
		TriggerRatio:       0.65,
		AbsoluteTokenLimit: 32000,
		KeepRecentRounds:   2,
	})
	got := c.Compress(blocks)

	bySequence := make(map[int]ContextBlock)
	for _, block := range got {
		if block.Source == SourceToolOutput {
			bySequence[block.Sequence] = block
		}
	}

	for _, seq := range []int{1, 2, 3} {
		block := bySequence[seq]
		if !strings.HasPrefix(block.Content, CompressedPrefix) {
			t.Errorf("sequence %d = %q, want compressed summary", seq, block.Content)
		}
		if block.Tokens >= 100 {
			t.Errorf("sequence %d Tokens = %d, want a smaller estimate", seq, block.Tokens)
		}
	}
	for _, seq := range []int{4, 5} {
		want := "file" + string(rune('0'+seq)) + ".go full content"
		if bySequence[seq].Content != want {
			t.Errorf("sequence %d = %q, want untouched %q", seq, bySequence[seq].Content, want)
		}
		if bySequence[seq].Tokens != 100 {
			t.Errorf("sequence %d Tokens = %d, want original 100", seq, bySequence[seq].Tokens)
		}
	}

	// Pinned blocks are never compressed.
	for _, block := range got {
		if block.IsPinned && block.Content != "system prompt" {
			t.Errorf("pinned block content = %q, want unchanged", block.Content)
		}
	}
}

func TestCompressorForcesMinimalSummaryAboveAbsoluteLimit(t *testing.T) {
	blocks := []ContextBlock{
		{Source: SourceSystem, Content: "pinned system", IsPinned: true},
		toolBlock(1, "old1.go with a lot of content", 100),
		toolBlock(2, "old2.go with a lot of content", 100),
		toolBlock(3, "new3.go full content", 100),
		toolBlock(4, "new4.go full content", 100),
	}
	// The absolute limit is too small for the original history; after
	// summarization it still exceeds it, so old rounds collapse to the
	// minimal summary.
	c := NewCompressor(CompressionConfig{
		TriggerRatio:       0.65,
		AbsoluteTokenLimit: 1,
		KeepRecentRounds:   2,
	})
	got := c.Compress(blocks)
	for _, block := range got {
		if block.Source != SourceToolOutput || block.Sequence > 2 {
			continue
		}
		if !strings.Contains(block.Content, "曾读取过") {
			t.Errorf("sequence %d = %q, want minimal historical summary", block.Sequence, block.Content)
		}
	}
}

func TestCompressorIgnoresPinnedToolOutput(t *testing.T) {
	blocks := []ContextBlock{
		{Source: SourceToolOutput, Content: "pinned tool output", IsPinned: true, Sequence: 1},
		toolBlock(2, "normal", 10),
		toolBlock(3, "normal2", 10),
		toolBlock(4, "normal3", 10),
	}
	c := NewCompressor(CompressionConfig{KeepRecentRounds: 2, AbsoluteTokenLimit: 32000})
	got := c.Compress(blocks)
	for _, block := range got {
		if block.IsPinned && block.Content != "pinned tool output" {
			t.Errorf("pinned tool output = %q, want unchanged", block.Content)
		}
	}
}

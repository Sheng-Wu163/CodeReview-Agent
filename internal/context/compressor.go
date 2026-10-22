package context

import (
	"regexp"
	"sort"
	"strings"
)

// CompressedPrefix marks a block whose content was replaced by a summary.
const CompressedPrefix = "[摘要]"

// minimalSummary is the last-resort summary for history that must be dropped
// to satisfy the absolute token limit (spec 18.4).
const minimalSummary = "[摘要] 曾读取过历史文件（工具输出已丢弃）"

// compressorMaxSummaryRunes bounds the first-line excerpt kept in a
// structured summary.
const compressorMaxSummaryRunes = 120

// fileHintRe extracts a file-like token from a tool output line.
var fileHintRe = regexp.MustCompile(`[A-Za-z0-9_./-]+\.[A-Za-z0-9_]+`)

// Compressor implements the sliding-window compression policy (FR-54). It
// never touches pinned blocks.
type Compressor struct {
	cfg CompressionConfig
}

// NewCompressor creates a Compressor. Zero config values fall back to the
// spec defaults.
func NewCompressor(cfg CompressionConfig) *Compressor {
	return &Compressor{cfg: cfg.withDefaults()}
}

// Compress returns a copy of blocks in which every TOOL_OUTPUT block older
// than the newest KeepRecentRounds is replaced by a structured summary. The
// newest blocks are selected by Sequence, not by slice index, so shuffled
// input is handled safely (CLAUDE.v.1.9.md).
func (c *Compressor) Compress(blocks []ContextBlock) []ContextBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]ContextBlock, len(blocks))
	copy(out, blocks)

	type candidate struct {
		index    int
		sequence int
	}
	var outputs []candidate
	for i, block := range out {
		if block.Source == SourceToolOutput && !block.IsPinned {
			outputs = append(outputs, candidate{index: i, sequence: block.Sequence})
		}
	}
	if len(outputs) == 0 {
		return out
	}

	// Newest first.
	sort.SliceStable(outputs, func(i, j int) bool {
		return outputs[i].sequence > outputs[j].sequence
	})

	keep := c.cfg.KeepRecentRounds
	if keep > len(outputs) {
		keep = len(outputs)
	}
	for i := keep; i < len(outputs); i++ {
		idx := outputs[i].index
		out[idx] = summarizeBlock(out[idx])
	}

	// Edge case 18.4: if the context is still above the absolute limit,
	// reduce the older rounds to the minimal "曾经读取过" summary.
	if TotalTokens(out) > c.cfg.AbsoluteTokenLimit {
		for i := keep; i < len(outputs); i++ {
			idx := outputs[i].index
			out[idx] = minimalizeBlock(out[idx])
		}
	}
	return out
}

// summarizeBlock replaces a block's content with a bounded first-line
// excerpt and recomputes its token estimate.
func summarizeBlock(block ContextBlock) ContextBlock {
	excerpt := firstNonEmptyLine(block.Content)
	excerpt = truncateRunes(excerpt, compressorMaxSummaryRunes)
	if excerpt == "" {
		excerpt = "工具输出为空"
	}
	block.Content = CompressedPrefix + " " + excerpt + " …（历史工具输出已压缩）"
	block.Tokens = EstimateTokens(block.Content)
	return block
}

// minimalizeBlock reduces a block to the minimal historical summary.
func minimalizeBlock(block ContextBlock) ContextBlock {
	if strings.HasPrefix(block.Content, minimalSummary) {
		return block
	}
	hint := fileHintRe.FindString(firstNonEmptyLine(block.Content))
	block.Content = minimalSummary
	if hint != "" {
		block.Content = CompressedPrefix + " 曾读取过 " + hint + "（工具输出已丢弃）"
	}
	block.Tokens = EstimateTokens(block.Content)
	return block
}

// firstNonEmptyLine returns the first non-blank line, trimmed.
func firstNonEmptyLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// truncateRunes shortens s to at most limit runes without splitting UTF-8.
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

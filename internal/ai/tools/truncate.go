package tools

import (
	"strings"
	"unicode/utf8"
)

// Tool output pre-filter limits (v1.9 FR-55).
const (
	// DefaultToolOutputHardLimit caps a single tool result in characters.
	DefaultToolOutputHardLimit = 10000
	// MaxSearchResults caps the number of search matches returned to the
	// agent (spec 18.4).
	MaxSearchResults = 20
	// TruncationWarning is appended whenever a tool output is truncated.
	TruncationWarning = "[警告：输出过长已截断，建议缩小检索范围]"
)

// limitOutput truncates s to at most limit characters (runes) and appends a
// warning when anything was dropped. A non-positive limit uses the default
// hard limit. The check runs at the outermost layer of the tool so a single
// call can never blow up the model context (CLAUDE.v.1.9.md).
func limitOutput(s string, limit int) string {
	if limit <= 0 {
		limit = DefaultToolOutputHardLimit
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	notice := "\n\n" + TruncationWarning
	keep := limit - utf8.RuneCountInString(notice)
	if keep < 0 {
		keep = 0
	}
	return string(runes[:keep]) + notice
}

// searchTruncationNotice is appended when the match count itself was capped.
func searchTruncationNotice() string {
	return TruncationWarning + "（匹配过多，建议更精确的关键词）"
}

// ensureNotice appends notice unless the output already carries it.
func ensureNotice(output, notice string) string {
	if notice == "" || strings.Contains(output, notice) {
		return output
	}
	return output + "\n\n" + notice
}

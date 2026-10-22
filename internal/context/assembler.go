package context

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Context sources, ordered by assembly priority (FR-52).
const (
	// SourceSystem marks pinned system instructions.
	SourceSystem = "SYSTEM"
	// SourceTaskState marks the pinned task goal, diff and pre-analysis
	// findings.
	SourceTaskState = "TASK_STATE"
	// SourceToolOutput marks compressible tool results.
	SourceToolOutput = "TOOL_OUTPUT"
	// SourceMemory marks compressible historical experience.
	SourceMemory = "MEMORY"
)

// OverflowWarning is emitted when the context still exceeds the absolute
// token limit after compression, for example because the pinned diff alone
// is larger than the limit (spec 18.4).
const OverflowWarning = "[警告：failed_context_overflow 上下文压缩后仍超过绝对水位，请缩小 Diff 或检索范围]"

// ContextBlock is one piece of the prompt context (v1.9 spec 18.3, FROZEN).
type ContextBlock struct { //nolint:revive // name is frozen by spec 18.3
	// Source is one of SYSTEM, TASK_STATE, TOOL_OUTPUT, MEMORY.
	Source string
	// Content is the block payload.
	Content string
	// Tokens is the estimated token count. When zero, a lightweight
	// character-based estimate is used.
	Tokens int
	// IsPinned marks blocks that must never be compressed.
	IsPinned bool
	// Sequence orders tool outputs chronologically (higher is newer).
	Sequence int
}

// ContextAssembler assembles context blocks into the final prompt and
// decides whether compression is needed (v1.9 spec 18.3, FROZEN).
type ContextAssembler interface { //nolint:revive // name is frozen by spec 18.3
	// Assemble sorts and concatenates the blocks into a prompt.
	Assemble(ctx context.Context, blocks []ContextBlock) (string, error)
	// ShouldCompress reports whether the block list exceeds either of the
	// two compression thresholds.
	ShouldCompress(blocks []ContextBlock, modelContextWindow int, cfg CompressionConfig) bool
}

// Assembler is the default ContextAssembler implementation. It compresses
// non-pinned blocks when needed and orders the result by source priority.
type Assembler struct {
	cfg         CompressionConfig
	modelWindow int
	compressor  *Compressor
}

// Compile-time check that Assembler implements ContextAssembler.
var _ ContextAssembler = (*Assembler)(nil)

// NewAssembler creates an Assembler. Zero config values fall back to the
// spec defaults.
func NewAssembler(cfg CompressionConfig) *Assembler {
	cfg = cfg.withDefaults()
	return &Assembler{cfg: cfg, compressor: NewCompressor(cfg)}
}

// WithModelWindow sets the model context window used by the ratio trigger
// before Assemble is called. A zero window disables the ratio trigger.
func (a *Assembler) WithModelWindow(window int) *Assembler {
	a.modelWindow = window
	return a
}

// ShouldCompress implements ContextAssembler.
func (a *Assembler) ShouldCompress(blocks []ContextBlock, modelContextWindow int, cfg CompressionConfig) bool {
	return ShouldCompress(blocks, modelContextWindow, cfg)
}

// Assemble implements ContextAssembler. It compresses older tool outputs
// when a threshold is crossed, then emits blocks in priority order:
// SYSTEM -> TASK_STATE -> TOOL_OUTPUT -> MEMORY (FR-52, FR-54).
func (a *Assembler) Assemble(ctx context.Context, blocks []ContextBlock) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("assemble context: %w", err)
	}
	if len(blocks) == 0 {
		return "", nil
	}

	work := blocks
	if ShouldCompress(blocks, a.modelWindow, a.cfg) {
		work = a.compressor.Compress(blocks)
	}
	ordered := sortBlocks(work)

	var b strings.Builder
	lastSource := ""
	for _, block := range ordered {
		if block.Source != lastSource {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "[%s]\n", block.Source)
			lastSource = block.Source
		}
		b.WriteString(block.Content)
		if !strings.HasSuffix(block.Content, "\n") {
			b.WriteString("\n")
		}
	}

	if ShouldCompress(ordered, a.modelWindow, a.cfg) {
		return OverflowWarning + "\n" + b.String(), nil
	}
	return b.String(), nil
}

// SourcePriority returns the assembly rank of a source: lower comes first.
func SourcePriority(source string) int {
	switch source {
	case SourceSystem:
		return 0
	case SourceTaskState:
		return 1
	case SourceToolOutput:
		return 2
	case SourceMemory:
		return 3
	default:
		return 4
	}
}

// sortBlocks orders blocks by source priority. Compressible history
// (TOOL_OUTPUT, MEMORY) is ordered newest-first by Sequence; pinned blocks
// keep their natural sequence order. Sequence is used instead of slice
// indexes so out-of-order input is handled correctly (CLAUDE.v.1.9.md).
func sortBlocks(blocks []ContextBlock) []ContextBlock {
	ordered := make([]ContextBlock, len(blocks))
	copy(ordered, blocks)
	sort.SliceStable(ordered, func(i, j int) bool {
		pi, pj := SourcePriority(ordered[i].Source), SourcePriority(ordered[j].Source)
		if pi != pj {
			return pi < pj
		}
		if ordered[i].Source == SourceToolOutput || ordered[i].Source == SourceMemory {
			return ordered[i].Sequence > ordered[j].Sequence
		}
		return ordered[i].Sequence < ordered[j].Sequence
	})
	return ordered
}

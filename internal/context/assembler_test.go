package context

import (
	"context"
	"strings"
	"testing"
)

func TestAssembleOrdersBySourcePriority(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n+new line\n"
	blocks := []ContextBlock{
		{Source: SourceToolOutput, Content: "tool output", Sequence: 2},
		{Source: SourceTaskState, Content: diff, IsPinned: true, Sequence: 1},
		{Source: SourceSystem, Content: "system instructions", IsPinned: true, Sequence: 0},
		{Source: SourceMemory, Content: "memory note", Sequence: 0},
	}

	a := NewAssembler(CompressionConfig{})
	got, err := a.Assemble(context.Background(), blocks)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	systemIdx := strings.Index(got, "[SYSTEM]")
	taskIdx := strings.Index(got, "[TASK_STATE]")
	toolIdx := strings.Index(got, "[TOOL_OUTPUT]")
	memoryIdx := strings.Index(got, "[MEMORY]")
	if systemIdx < 0 || taskIdx < 0 || toolIdx < 0 || memoryIdx < 0 {
		t.Fatalf("missing source headers: %q", got)
	}
	if systemIdx >= taskIdx || taskIdx >= toolIdx || toolIdx >= memoryIdx {
		t.Errorf("source order wrong: SYSTEM=%d TASK_STATE=%d TOOL_OUTPUT=%d MEMORY=%d",
			systemIdx, taskIdx, toolIdx, memoryIdx)
	}

	// AC-112: pinned content must be preserved verbatim.
	if !strings.Contains(got, diff) {
		t.Errorf("assembled prompt must preserve the pinned diff, got %q", got)
	}
	if !strings.Contains(got, "system instructions") {
		t.Errorf("assembled prompt must preserve the system block")
	}
}

func TestAssembleToolOutputNewestFirst(t *testing.T) {
	blocks := []ContextBlock{
		toolBlock(1, "oldest", 1),
		toolBlock(3, "newest", 1),
		toolBlock(2, "middle", 1),
	}
	a := NewAssembler(CompressionConfig{})
	got, err := a.Assemble(context.Background(), blocks)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	newest := strings.Index(got, "newest")
	middle := strings.Index(got, "middle")
	oldest := strings.Index(got, "oldest")
	if newest >= middle || middle >= oldest {
		t.Errorf("tool outputs not newest-first: %q", got)
	}
}

func TestAssembleAddsOverflowWarningForPinnedDiff(t *testing.T) {
	blocks := []ContextBlock{
		{Source: SourceSystem, Content: "system", IsPinned: true, Tokens: 100},
		{Source: SourceTaskState, Content: "huge diff", IsPinned: true, Tokens: 40000},
	}
	a := NewAssembler(CompressionConfig{AbsoluteTokenLimit: 32000})
	got, err := a.Assemble(context.Background(), blocks)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(got, "failed_context_overflow") {
		t.Errorf("expected overflow warning, got %q", got)
	}
	if !strings.Contains(got, "huge diff") {
		t.Errorf("pinned diff must be preserved, got %q", got)
	}
}

func TestAssembleHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := NewAssembler(CompressionConfig{})
	if _, err := a.Assemble(ctx, []ContextBlock{{Source: SourceSystem, Content: "x"}}); err == nil {
		t.Fatal("Assemble must return the context error")
	}
}

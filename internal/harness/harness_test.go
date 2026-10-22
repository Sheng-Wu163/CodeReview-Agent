package harness

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	ltools "github.com/tmc/langchaingo/tools"
	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
	ctxcontext "codereviewagent/internal/context"
)

// stubTool is a scripted tools.Tool. The first failTimes calls fail.
type stubTool struct {
	name      string
	failTimes int

	mu    sync.Mutex
	calls int
}

func (s *stubTool) Name() string { return s.name }

func (s *stubTool) Description() string { return "stub tool" }

func (s *stubTool) Call(_ context.Context, input string) (string, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n <= s.failTimes {
		return "", errors.New("transient tool failure")
	}
	return "ok:" + input, nil
}

func (s *stubTool) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// scriptedAgent runs a test-supplied function as its Review implementation.
type scriptedAgent struct {
	name string
	run  func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error)
}

func (a *scriptedAgent) Name() string { return a.name }

func (a *scriptedAgent) Review(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
	return a.run(ctx, input)
}

func testFinding() agents.AgentFinding {
	return agents.AgentFinding{
		Agent: "security", Severity: agents.SeverityCritical,
		FilePath: "main.go", LineNumber: 1, Message: "issue", Confidence: 0.9,
	}
}

func TestExecuteCompletedWithToolCalls(t *testing.T) {
	tool := &stubTool{name: "read_file"}
	agent := &scriptedAgent{name: "security", run: func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
		if len(input.Tools) != 1 {
			t.Fatalf("tools = %d, want 1", len(input.Tools))
		}
		if _, err := input.Tools[0].Call(ctx, "main.go"); err != nil {
			return nil, err
		}
		return []agents.AgentFinding{testFinding()}, nil
	}}

	h := New(zap.NewNop(), WithRetryPolicy(2, time.Millisecond))
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{Tools: []ltools.Tool{tool}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", res.Status)
	}
	if res.ToolCallCount != 1 || len(res.ExecutionLogs) != 1 {
		t.Errorf("tool calls = %d, logs = %d, want 1/1", res.ToolCallCount, len(res.ExecutionLogs))
	}
	if !res.ExecutionLogs[0].Success {
		t.Error("execution log should be successful")
	}
	if len(res.Findings) != 1 {
		t.Errorf("findings = %d, want 1", len(res.Findings))
	}
}

func TestExecuteNoToolCallsDowngradesToPartial(t *testing.T) {
	agent := &scriptedAgent{name: "logic", run: func(context.Context, agents.ReviewInput) ([]agents.AgentFinding, error) {
		return nil, nil
	}}

	h := New(zap.NewNop())
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != StatusPartial {
		t.Errorf("Status = %q, want partial (AC-102)", res.Status)
	}
	if res.ErrorReason != ErrorReasonNoToolCalls {
		t.Errorf("ErrorReason = %q, want %q", res.ErrorReason, ErrorReasonNoToolCalls)
	}
	if res.ToolCallCount != 0 {
		t.Errorf("ToolCallCount = %d, want 0", res.ToolCallCount)
	}
}

func TestExecuteTimeoutFails(t *testing.T) {
	agent := &scriptedAgent{name: "performance", run: func(ctx context.Context, _ agents.ReviewInput) ([]agents.AgentFinding, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}

	h := New(zap.NewNop(), WithTimeout(20*time.Millisecond))
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{})
	if err == nil {
		t.Fatal("Execute error = nil, want timeout error (AC-103)")
	}
	if res.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", res.Status)
	}
	if !strings.Contains(res.ErrorReason, "timeout") {
		t.Errorf("ErrorReason = %q, want timeout reason", res.ErrorReason)
	}
}

func TestExecuteInterruptsRepeatedFingerprint(t *testing.T) {
	tool := &stubTool{name: "read_file"}
	agent := &scriptedAgent{name: "security", run: func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
		var err error
		for i := 0; i < 3; i++ {
			if _, err = input.Tools[0].Call(ctx, "main.go"); err != nil {
				break
			}
		}
		return nil, err
	}}

	h := New(zap.NewNop(), WithRetryPolicy(0, time.Millisecond))
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{Tools: []ltools.Tool{tool}})
	if !errors.Is(err, ErrOscillation) {
		t.Fatalf("Execute error = %v, want ErrOscillation (AC-104)", err)
	}
	if res.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", res.Status)
	}
	if res.ToolCallCount != RepeatThreshold {
		t.Errorf("ToolCallCount = %d, want %d", res.ToolCallCount, RepeatThreshold)
	}
}

func TestExecuteInterruptsAlternatingFingerprint(t *testing.T) {
	first := &stubTool{name: "search_code"}
	second := &stubTool{name: "read_file"}
	agent := &scriptedAgent{name: "logic", run: func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
		var err error
		for i := 0; i < AlternationRounds*2; i++ {
			idx := i % 2
			if _, err = input.Tools[idx].Call(ctx, []string{"x", "y"}[idx]); err != nil {
				break
			}
		}
		return nil, err
	}}

	h := New(zap.NewNop(), WithRetryPolicy(0, time.Millisecond))
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{Tools: []ltools.Tool{first, second}})
	if !errors.Is(err, ErrOscillation) {
		t.Fatalf("Execute error = %v, want ErrOscillation (AC-105)", err)
	}
	if res.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", res.Status)
	}
	if res.ToolCallCount != AlternationRounds*2 {
		t.Errorf("ToolCallCount = %d, want %d", res.ToolCallCount, AlternationRounds*2)
	}
}

func TestExecuteRetriesReadOnlyTool(t *testing.T) {
	tool := &stubTool{name: "read_file", failTimes: 2}
	agent := &scriptedAgent{name: "security", run: func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
		if _, err := input.Tools[0].Call(ctx, "main.go"); err != nil {
			return nil, err
		}
		return []agents.AgentFinding{testFinding()}, nil
	}}

	h := New(zap.NewNop(), WithRetryPolicy(2, time.Millisecond))
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{Tools: []ltools.Tool{tool}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", res.Status)
	}
	if tool.callCount() != 3 {
		t.Errorf("underlying calls = %d, want 3 (1 + 2 retries)", tool.callCount())
	}
	if res.ToolCallCount != 1 {
		t.Errorf("ToolCallCount = %d, want 1 logical call", res.ToolCallCount)
	}
	if len(res.ExecutionLogs) != 1 || !res.ExecutionLogs[0].Success {
		t.Errorf("ExecutionLogs = %+v, want one successful call", res.ExecutionLogs)
	}
}

func TestExecuteDoesNotRetryNonReadOnlyTool(t *testing.T) {
	tool := &stubTool{name: "write_file", failTimes: 1}
	agent := &scriptedAgent{name: "lint", run: func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
		if _, err := input.Tools[0].Call(ctx, "main.go"); err != nil {
			return nil, err
		}
		return nil, nil
	}}

	h := New(zap.NewNop(), WithRetryPolicy(2, time.Millisecond))
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{Tools: []ltools.Tool{tool}})
	if err == nil {
		t.Fatal("Execute error = nil, want tool failure")
	}
	if res.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", res.Status)
	}
	if tool.callCount() != 1 {
		t.Errorf("underlying calls = %d, want 1 (no retry)", tool.callCount())
	}
}

func TestExecuteOscillationPreservesFindingsAsPartial(t *testing.T) {
	tool := &stubTool{name: "read_file"}
	agent := &scriptedAgent{name: "security", run: func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
		var err error
		for i := 0; i < 3; i++ {
			if _, err = input.Tools[0].Call(ctx, "main.go"); err != nil {
				break
			}
		}
		return []agents.AgentFinding{testFinding()}, err
	}}

	h := New(zap.NewNop(), WithRetryPolicy(0, time.Millisecond))
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{Tools: []ltools.Tool{tool}})
	if !errors.Is(err, ErrOscillation) {
		t.Fatalf("Execute error = %v, want ErrOscillation", err)
	}
	if res.Status != StatusPartial {
		t.Errorf("Status = %q, want partial (spec 17.4)", res.Status)
	}
	if len(res.Findings) != 1 {
		t.Errorf("findings = %d, want preserved finding", len(res.Findings))
	}
}

func TestPreparePromptKeepsPinnedAndRecentBlocks(t *testing.T) {
	h := New(zap.NewNop(), WithCompression(8000, ctxcontext.CompressionConfig{
		TriggerRatio:       0.65,
		AbsoluteTokenLimit: 32000,
		KeepRecentRounds:   2,
	}))

	diff := "diff --git a/main.go b/main.go\n+new line\n"
	blocks := []ctxcontext.ContextBlock{
		{Source: ctxcontext.SourceSystem, Content: "system prompt", IsPinned: true, Sequence: 0},
		{Source: ctxcontext.SourceTaskState, Content: diff, IsPinned: true, Tokens: 500, Sequence: 1},
		{Source: ctxcontext.SourceToolOutput, Content: "file1.go:\nSECRET_OLD_1", Tokens: 1000, Sequence: 1},
		{Source: ctxcontext.SourceToolOutput, Content: "file2.go:\nSECRET_OLD_2", Tokens: 1000, Sequence: 2},
		{Source: ctxcontext.SourceToolOutput, Content: "file3.go:\nSECRET_OLD_3", Tokens: 1000, Sequence: 3},
		{Source: ctxcontext.SourceToolOutput, Content: "file4.go:\nRECENT_4", Tokens: 1000, Sequence: 4},
		{Source: ctxcontext.SourceToolOutput, Content: "file5.go:\nRECENT_5", Tokens: 1000, Sequence: 5},
	}

	got, err := h.preparePrompt(context.Background(), blocks)
	if err != nil {
		t.Fatalf("preparePrompt: %v", err)
	}
	if !strings.Contains(got, diff) {
		t.Errorf("pinned diff must be preserved verbatim\n%s", got)
	}
	if !strings.Contains(got, "RECENT_4") || !strings.Contains(got, "RECENT_5") {
		t.Errorf("newest two tool outputs must be intact\n%s", got)
	}
	if strings.Contains(got, "SECRET_OLD_1") || strings.Contains(got, "SECRET_OLD_3") {
		t.Errorf("older tool outputs should be summarized\n%s", got)
	}
	if !strings.Contains(got, ctxcontext.CompressedPrefix) {
		t.Errorf("expected compressed summaries\n%s", got)
	}
}

func TestExecuteRejectsEmptyToolArguments(t *testing.T) {
	tool := &stubTool{name: "read_file"}
	agent := &scriptedAgent{name: "logic", run: func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
		_, err := input.Tools[0].Call(ctx, "   ")
		return nil, err
	}}

	h := New(zap.NewNop())
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{Tools: []ltools.Tool{tool}})
	if err == nil {
		t.Fatal("Execute error = nil, want validation error")
	}
	if res.ToolCallCount != 1 || len(res.ExecutionLogs) != 1 || res.ExecutionLogs[0].Success {
		t.Errorf("execution logs = %+v, want one failed call", res.ExecutionLogs)
	}
	if tool.callCount() != 0 {
		t.Errorf("underlying calls = %d, want 0 (validation rejected)", tool.callCount())
	}
}

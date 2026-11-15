package harness

import (
	"context"
	"errors"
	"testing"
	"time"

	ltools "github.com/tmc/langchaingo/tools"
	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
)

// policyAgent adds a declared allowlist to the scripted test agent.
type policyAgent struct {
	*scriptedAgent
	allowed []string
}

func (a *policyAgent) AllowedTools() []string { return a.allowed }

func TestExecuteRejectsToolOutsideAllowlist(t *testing.T) {
	tool := &stubTool{name: "git_blame"}
	agent := &policyAgent{
		scriptedAgent: &scriptedAgent{name: "security", run: func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
			_, err := input.Tools[0].Call(ctx, "main.go:1:5")
			return nil, err
		}},
		allowed: []string{"read_file"},
	}

	h := New(zap.NewNop(), WithMaxToolCalls(20))
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{Tools: []ltools.Tool{tool}})
	if err == nil {
		t.Fatal("Execute error = nil, want allowlist rejection (AC-143)")
	}
	if tool.callCount() != 0 {
		t.Errorf("tool executed %d times, want 0", tool.callCount())
	}
	if len(res.ExecutionLogs) != 1 || res.ExecutionLogs[0].Success {
		t.Errorf("execution logs = %+v, want one failed log", res.ExecutionLogs)
	}
}

func TestExecuteToolBudgetInterruptsAsPartial(t *testing.T) {
	tool := &stubTool{name: "read_file"}
	agent := &scriptedAgent{name: "logic", run: func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
		for i := 0; i < 3; i++ {
			if _, err := input.Tools[0].Call(ctx, "main.go"); err != nil {
				return []agents.AgentFinding{testFinding()}, err
			}
		}
		return nil, nil
	}}

	h := New(zap.NewNop(), WithMaxToolCalls(2), WithRetryPolicy(0, time.Millisecond))
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{Tools: []ltools.Tool{tool}})
	if !errors.Is(err, ErrToolBudgetExhausted) {
		t.Fatalf("Execute error = %v, want ErrToolBudgetExhausted (AC-146)", err)
	}
	if res.Status != StatusPartial {
		t.Errorf("Status = %q, want partial", res.Status)
	}
	if res.ErrorReason != ErrorReasonToolBudgetExhausted {
		t.Errorf("ErrorReason = %q, want %q", res.ErrorReason, ErrorReasonToolBudgetExhausted)
	}
	if tool.callCount() != 2 {
		t.Errorf("tool executed %d times, want 2", tool.callCount())
	}
	if len(res.Findings) != 1 {
		t.Errorf("findings = %d, want 1 preserved", len(res.Findings))
	}
}

func TestNormalizeFingerprintArgs(t *testing.T) {
	tests := []struct {
		tool string
		in   string
		want string
	}{
		{tool: "read_file", in: "./a.go", want: "a.go"},
		{tool: "read_file", in: "./a.go:1:10", want: "a.go:1:10"},
		{tool: "git_blame", in: `.\src\a.go:2:4`, want: "src/a.go:2:4"},
		{tool: "git_log_file", in: "./a.go:100", want: "a.go:100"},
		{tool: "search_code", in: `\./a\.go`, want: `\./a\.go`},
	}
	for _, tt := range tests {
		if got := NormalizeFingerprintArgs(tt.tool, tt.in); got != tt.want {
			t.Errorf("NormalizeFingerprintArgs(%q, %q) = %q, want %q", tt.tool, tt.in, got, tt.want)
		}
	}
}

func TestExecuteTreatsCleanedPathsAsSameFingerprint(t *testing.T) {
	tool := &stubTool{name: "read_file"}
	agent := &scriptedAgent{name: "security", run: func(ctx context.Context, input agents.ReviewInput) ([]agents.AgentFinding, error) {
		var err error
		for _, args := range []string{"./a.go", "a.go", "./a.go"} {
			if _, err = input.Tools[0].Call(ctx, args); err != nil {
				break
			}
		}
		return nil, err
	}}

	h := New(zap.NewNop(), WithRetryPolicy(0, time.Millisecond))
	res, err := h.Execute(context.Background(), agent, agents.ReviewInput{Tools: []ltools.Tool{tool}})
	if !errors.Is(err, ErrOscillation) {
		t.Fatalf("Execute error = %v, want ErrOscillation (AC-145)", err)
	}
	if res.ToolCallCount != RepeatThreshold {
		t.Errorf("ToolCallCount = %d, want %d", res.ToolCallCount, RepeatThreshold)
	}
	if len(res.ExecutionLogs) < 2 || res.ExecutionLogs[0].ArgsHash != res.ExecutionLogs[1].ArgsHash {
		t.Error("cleaned path variants must share one fingerprint")
	}
}

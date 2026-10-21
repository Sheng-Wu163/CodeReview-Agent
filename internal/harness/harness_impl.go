package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	ltools "github.com/tmc/langchaingo/tools"
	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
)

// ErrorReasonNoToolCalls is the completion-gate reason attached when an
// agent claims completion without invoking any tool (FR-49, AC-102).
const ErrorReasonNoToolCalls = "suspicious_no_tool_calls"

const (
	defaultTimeout        = 60 * time.Second
	defaultToolTimeout    = 30 * time.Second
	defaultMaxRetries     = 2
	defaultRetryBaseDelay = 50 * time.Millisecond
)

// Executor is the default Harness implementation. It wraps the agent's tool
// set, records every logical invocation, enforces the retry policy and
// detects path oscillation.
type Executor struct {
	logger         *zap.Logger
	timeout        time.Duration
	toolTimeout    time.Duration
	maxRetries     int
	retryBaseDelay time.Duration
	readOnly       map[string]bool
}

// Compile-time check that Executor implements Harness.
var _ Harness = (*Executor)(nil)

// Option customizes an Executor.
type Option func(*Executor)

// WithTimeout bounds one Harness execution. Zero disables the extra timeout
// (the caller's context still applies).
func WithTimeout(d time.Duration) Option {
	return func(e *Executor) { e.timeout = d }
}

// WithToolTimeout bounds a single tool invocation. Zero disables the
// per-call timeout.
func WithToolTimeout(d time.Duration) Option {
	return func(e *Executor) { e.toolTimeout = d }
}

// WithRetryPolicy overrides the read-only retry policy. maxRetries is the
// number of retries after the first attempt (spec: at most 2).
func WithRetryPolicy(maxRetries int, baseDelay time.Duration) Option {
	return func(e *Executor) {
		if maxRetries < 0 {
			maxRetries = 0
		}
		e.maxRetries = maxRetries
		if baseDelay >= 0 {
			e.retryBaseDelay = baseDelay
		}
	}
}

// WithReadOnlyTools replaces the set of tool names eligible for retries.
// Read-only tools are the only ones retried in this iteration.
func WithReadOnlyTools(names ...string) Option {
	return func(e *Executor) {
		e.readOnly = make(map[string]bool, len(names))
		for _, name := range names {
			e.readOnly[name] = true
		}
	}
}

// New creates a Harness executor. A nil logger falls back to a no-op logger.
func New(logger *zap.Logger, opts ...Option) *Executor {
	if logger == nil {
		logger = zap.NewNop()
	}
	e := &Executor{
		logger:         logger,
		timeout:        defaultTimeout,
		toolTimeout:    defaultToolTimeout,
		maxRetries:     defaultMaxRetries,
		retryBaseDelay: defaultRetryBaseDelay,
		readOnly: map[string]bool{
			"read_file":               true,
			"search_code":             true,
			"get_function_definition": true,
		},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Execute implements Harness (FR-48). It wraps the input tools, runs the
// agent, then applies the completion gate and returns the structured result.
func (e *Executor) Execute(ctx context.Context, agent agents.ReviewAgent, input agents.ReviewInput) (AgentResult, error) {
	if agent == nil {
		return AgentResult{Status: StatusFailed, ErrorReason: "nil agent"},
			errors.New("harness: nil agent")
	}

	result := AgentResult{Status: StatusFailed}
	runCtx := ctx
	if e.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}

	recorder := &recorder{}
	runInput := input
	runInput.Tools = e.governTools(input.Tools, recorder)

	findings, err := agent.Review(runCtx, runInput)
	result.Findings = findings
	result.ToolCallCount = recorder.count()
	result.ExecutionLogs = recorder.snapshot()

	if err != nil {
		result.ErrorReason = err.Error()
		switch {
		case errors.Is(err, ErrOscillation):
			// 17.4: preserve findings produced before the interruption and
			// expose the run as partial; a loop without findings is a hard
			// failure (FR-51).
			if len(findings) > 0 {
				result.Status = StatusPartial
			} else {
				result.Status = StatusFailed
			}
			e.logger.Sugar().Warnw("agent aborted by oscillation",
				"agent", agent.Name(), "findings", len(findings))
			return result, fmt.Errorf("%s: %w", agent.Name(), ErrOscillation)
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			result.Status = StatusFailed
			result.ErrorReason = "timeout: " + context.DeadlineExceeded.Error()
			return result, fmt.Errorf("%s: timeout: %w", agent.Name(), context.DeadlineExceeded)
		case errors.Is(runCtx.Err(), context.Canceled):
			result.Status = StatusFailed
			result.ErrorReason = "canceled: " + context.Canceled.Error()
			return result, fmt.Errorf("%s: canceled: %w", agent.Name(), context.Canceled)
		default:
			result.Status = StatusFailed
			return result, fmt.Errorf("%s: %w", agent.Name(), err)
		}
	}

	result.Status = StatusCompleted
	if result.ToolCallCount == 0 {
		// Completion gate (FR-49): an agent that used no tools cannot have
		// inspected the diff, so its review is suspicious.
		result.Status = StatusPartial
		result.ErrorReason = ErrorReasonNoToolCalls
	}
	return result, nil
}

// governTools copies the tool slice and wraps every tool with the Harness
// governance layer.
func (e *Executor) governTools(tools []ltools.Tool, rec *recorder) []ltools.Tool {
	out := make([]ltools.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		out = append(out, &governedTool{inner: tool, executor: e, recorder: rec})
	}
	return out
}

// recorder accumulates tool logs for one execution. It is safe for
// concurrent use even though the current ReAct loop is sequential.
type recorder struct {
	mu   sync.Mutex
	logs []ToolLog
}

// add appends one logical invocation.
func (r *recorder) add(log ToolLog) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, log)
}

// snapshot returns a copy of the recorded logs.
func (r *recorder) snapshot() []ToolLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ToolLog, len(r.logs))
	copy(out, r.logs)
	return out
}

// count reports the number of logical invocations.
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.logs)
}

// governedTool applies the five-stage governance contract to one tool:
// validate, execute with timeout, retry read-only tools, record the
// fingerprint, and abort on oscillation (FR-50, FR-51).
type governedTool struct {
	inner    ltools.Tool
	executor *Executor
	recorder *recorder
}

// Compile-time check that governedTool implements tools.Tool.
var _ ltools.Tool = (*governedTool)(nil)

// Name implements tools.Tool.
func (t *governedTool) Name() string { return t.inner.Name() }

// Description implements tools.Tool.
func (t *governedTool) Description() string { return t.inner.Description() }

// Call implements tools.Tool.
func (t *governedTool) Call(ctx context.Context, input string) (string, error) {
	toolName := t.inner.Name()
	hash, err := ArgsHash(input)
	if err != nil {
		return "", err
	}
	entry := ToolLog{
		ToolName:  toolName,
		ArgsHash:  hash,
		Timestamp: time.Now(),
	}

	// Validate: reject empty arguments before touching the tool.
	if strings.TrimSpace(input) == "" {
		t.recorder.add(entry)
		return "", fmt.Errorf("harness: %s: empty tool arguments", toolName)
	}

	attempts := 1
	if t.executor.readOnly[toolName] {
		attempts += t.executor.maxRetries
	}

	var (
		output  string
		callErr error
	)
	for attempt := 0; attempt < attempts; attempt++ {
		output, callErr = t.callOnce(ctx, input)
		if callErr == nil {
			break
		}
		// Never retry an oscillation abort or a caller-side cancellation.
		if errors.Is(callErr, ErrOscillation) || ctx.Err() != nil {
			break
		}
		if attempt == attempts-1 {
			break
		}
		delay := t.executor.retryBaseDelay
		for i := 0; i < attempt; i++ {
			delay *= 2
		}
		t.executor.logger.Sugar().Warnw("retrying read-only tool",
			"tool", toolName, "attempt", attempt+1, "delay", delay, "error", callErr)
		if err := sleepContext(ctx, delay); err != nil {
			callErr = err
			break
		}
	}

	entry.Success = callErr == nil
	t.recorder.add(entry)

	if err := DetectOscillation(t.recorder.snapshot()); err != nil {
		return "", err
	}
	return output, callErr
}

// callOnce executes the tool under the per-call timeout.
func (t *governedTool) callOnce(ctx context.Context, input string) (string, error) {
	callCtx := ctx
	if t.executor.toolTimeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, t.executor.toolTimeout)
		defer cancel()
	}
	return t.inner.Call(callCtx, input)
}

// sleepContext waits for d or until ctx is done.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

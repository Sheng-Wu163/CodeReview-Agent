// Package harness wraps every review agent execution with a uniform control
// layer: structured results, tool-call governance, retry policy and
// oscillation detection (v1.8 FR-48..FR-51).
package harness

import (
	"context"
	"time"

	"codereviewagent/internal/ai/agents"
)

// AgentStatus is the Harness-owned outcome of one agent execution. Agents
// never set it themselves (CLAUDE.v.1.8.md): only the Harness decides the
// final status through the unified completion gate.
type AgentStatus string

const (
	// StatusCompleted marks an agent that ran to completion and passed the
	// completion gate.
	StatusCompleted AgentStatus = "completed"
	// StatusFailed marks an agent that crashed, timed out or was aborted.
	StatusFailed AgentStatus = "failed"
	// StatusPartial marks an agent whose result is usable but suspicious:
	// it completed without any tool call, or it was interrupted by an
	// oscillation after producing findings.
	StatusPartial AgentStatus = "partial"
)

// ToolLog is one logical tool invocation recorded by the Harness. ArgsHash is
// the SHA-256 of the JSON-serialized tool arguments, so identical calls can
// be detected without keeping raw payloads in memory.
type ToolLog struct {
	// ToolName is the name of the invoked tool.
	ToolName string `json:"tool_name"`
	// ArgsHash is the hex-encoded SHA-256 of the JSON-serialized arguments.
	ArgsHash string `json:"args_hash"`
	// Timestamp is the moment the invocation was recorded.
	Timestamp time.Time `json:"timestamp"`
	// Success reports whether the invocation succeeded after retries.
	Success bool `json:"success"`
}

// AgentResult is the structured outcome of one Harness execution (FR-49).
type AgentResult struct {
	// Status is the Harness-decided outcome.
	Status AgentStatus `json:"status"`
	// Findings holds the findings the agent produced, if any.
	Findings []agents.AgentFinding `json:"findings,omitempty"`
	// ToolCallCount is the number of logical tool invocations.
	ToolCallCount int `json:"tool_call_count"`
	// ErrorReason explains a failed or partial outcome.
	ErrorReason string `json:"error_reason,omitempty"`
	// ExecutionLogs records every tool-call fingerprint.
	ExecutionLogs []ToolLog `json:"execution_logs,omitempty"`
}

// Harness wraps one agent run and returns a structured result (FR-48). The
// status is decided exclusively by the Harness implementation.
type Harness interface {
	// Execute runs agent over input under Harness control.
	Execute(ctx context.Context, agent agents.ReviewAgent, input agents.ReviewInput) (AgentResult, error)
}

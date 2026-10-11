package agents

import (
	"github.com/tmc/langchaingo/llms"
)

// LintAgent reviews the changed code for convention violations (FR-08):
// naming, error handling, comment completeness and import grouping. It
// combines the output of the static analysis tool (run via
// RunStaticAnalysisTool) with LLM reasoning.
type LintAgent struct {
	reviewAgent
}

// Compile-time check that LintAgent implements ReviewAgent.
var _ ReviewAgent = (*LintAgent)(nil)

// NewLintAgent creates a conventions review agent backed by llm.
func NewLintAgent(llm llms.Model) *LintAgent {
	return &LintAgent{reviewAgent: newReviewAgent("lint", "lint.txt", llm)}
}

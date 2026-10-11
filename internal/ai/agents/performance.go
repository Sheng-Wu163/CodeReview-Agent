package agents

import (
	"github.com/tmc/langchaingo/llms"
)

// PerformanceAgent reviews the changed code for performance problems
// (FR-07): N+1 queries, hot-path allocations and inefficient string
// building.
type PerformanceAgent struct {
	reviewAgent
}

// Compile-time check that PerformanceAgent implements ReviewAgent.
var _ ReviewAgent = (*PerformanceAgent)(nil)

// NewPerformanceAgent creates a performance review agent backed by llm.
func NewPerformanceAgent(llm llms.Model) *PerformanceAgent {
	return &PerformanceAgent{reviewAgent: newReviewAgent("performance", "performance.txt", llm)}
}

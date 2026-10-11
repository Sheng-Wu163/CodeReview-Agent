package agents

import (
	"github.com/tmc/langchaingo/llms"
)

// LogicAgent reviews the changed code for logic and quality defects
// (FR-06): nil pointer dereferences, boundary conditions, unhandled errors,
// concurrency hazards and resource leaks.
type LogicAgent struct {
	reviewAgent
}

// Compile-time check that LogicAgent implements ReviewAgent.
var _ ReviewAgent = (*LogicAgent)(nil)

// NewLogicAgent creates a logic/quality review agent backed by llm.
func NewLogicAgent(llm llms.Model) *LogicAgent {
	return &LogicAgent{reviewAgent: newReviewAgent("logic", "logic.txt", llm)}
}

package review

// Agent names used in the strategy (must match the agents' Name()).
const (
	// AgentSecurity is the security review agent.
	AgentSecurity = "security"
	// AgentLogic is the logic/quality review agent.
	AgentLogic = "logic"
	// AgentPerformance is the performance review agent.
	AgentPerformance = "performance"
	// AgentLint is the lint/convention review agent.
	AgentLint = "lint"
)

// StrategySelector selects a review strategy for a complexity level
// (v1.2 spec 14.3, FROZEN).
type StrategySelector interface {
	// Select returns the strategy for the given complexity level.
	Select(level ComplexityLevel) ReviewStrategy
}

// Selector is the default StrategySelector. It only decides which agents
// run; it never adjusts agent parameters (FR-35, CLAUDE.v.1.2.md).
type Selector struct{}

// Compile-time check that Selector implements StrategySelector.
var _ StrategySelector = (*Selector)(nil)

// NewSelector creates the default strategy selector.
func NewSelector() *Selector { return &Selector{} }

// Select implements StrategySelector:
//   - Trivial: only the security and lint agents, no verifier.
//   - Standard/Complex: all four agents plus the verifier.
func (s *Selector) Select(level ComplexityLevel) ReviewStrategy {
	if level == ComplexityTrivial {
		return ReviewStrategy{
			Level:          level,
			EnabledAgents:  []string{AgentSecurity, AgentLint},
			EnableVerifier: false,
		}
	}
	return ReviewStrategy{
		Level:          level,
		EnabledAgents:  []string{AgentSecurity, AgentLogic, AgentPerformance, AgentLint},
		EnableVerifier: true,
	}
}

// IsAgentEnabled reports whether the strategy runs the named agent.
func IsAgentEnabled(strategy ReviewStrategy, agentName string) bool {
	for _, a := range strategy.EnabledAgents {
		if a == agentName {
			return true
		}
	}
	return false
}

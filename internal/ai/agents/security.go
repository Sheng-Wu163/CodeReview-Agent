package agents

import (
	"github.com/tmc/langchaingo/llms"
)

// SecurityAgent reviews the changed code for security vulnerabilities
// (FR-05): SQL injection, command injection, hardcoded secrets, insecure
// cryptography, path traversal and SSRF.
type SecurityAgent struct {
	reviewAgent
}

// Compile-time check that SecurityAgent implements ReviewAgent.
var _ ReviewAgent = (*SecurityAgent)(nil)

// NewSecurityAgent creates a security review agent backed by llm.
func NewSecurityAgent(llm llms.Model) *SecurityAgent {
	return &SecurityAgent{reviewAgent: newReviewAgent("security", "security.txt", llm)}
}

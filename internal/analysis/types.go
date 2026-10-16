// Package analysis implements the v1.3 static analysis pipeline: it runs
// external analyzers (semgrep, golangci-lint, gitleaks) deterministically
// before the review agents, producing PreAnalysisFinding lists that are
// injected into the agent prompts (spec 11, FR-16..FR-24).
package analysis

import (
	"context"

	"codereviewagent/internal/diff"
)

// Tool identifiers reported in PreAnalysisFinding.Tool.
const (
	// ToolSemgrep identifies findings from the Semgrep CLI.
	ToolSemgrep = "semgrep"
	// ToolGolangciLint identifies findings from golangci-lint.
	ToolGolangciLint = "golangci-lint"
	// ToolGitleaks identifies findings from the embedded gitleaks scanner.
	ToolGitleaks = "gitleaks"
)

// PreAnalysisFinding is the cross-tool unified static analysis finding
// structure (FR-16). Its shape is FROZEN as of v1.3: extensions require a
// change proposal under specs/changes/.
type PreAnalysisFinding struct {
	// ID is the stable fingerprint: <tool>:<ruleID>:<filePath>:<line>.
	ID string
	// Tool is one of "semgrep", "golangci-lint", "gitleaks".
	Tool string
	// RuleID is the analyzer-internal rule identifier.
	RuleID string
	// FilePath is the path of the scanned file, relative to the workspace.
	FilePath string
	// Line is the 1-based start line of the issue.
	Line int
	// EndLine is the 1-based end line of the issue (equal to Line when
	// single-line).
	EndLine int
	// Severity is one of "critical", "warning", "suggestion".
	Severity string
	// Message describes the issue.
	Message string
	// Snippet is the relevant code excerpt (at most 5 lines).
	Snippet string
}

// Analyzer is the uniform interface every static analysis tool integration
// implements (spec 11.3, frozen). Implementations must be safe for
// concurrent use.
type Analyzer interface {
	// Name returns the tool identifier reported in PreAnalysisFinding.Tool.
	Name() string
	// Analyze scans a single changed file and returns its findings.
	Analyze(ctx context.Context, file diff.FileDiff) ([]PreAnalysisFinding, error)
}

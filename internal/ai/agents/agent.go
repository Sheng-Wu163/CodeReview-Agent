// Package agents implements the parallel review agents (security, logic,
// performance, lint) and the verifier (FR-05..FR-10).
package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/tools"

	"codereviewagent/internal/ai/prompts"
	"codereviewagent/internal/diff"
)

// Severity levels for findings (FR-13).
const (
	// SeverityCritical marks security issues and data-loss risks.
	SeverityCritical = "critical"
	// SeverityWarning marks logic errors and performance issues.
	SeverityWarning = "warning"
	// SeveritySuggestion marks style and convention improvements.
	SeveritySuggestion = "suggestion"
)

// maxAgentIterations bounds the ReAct loop of every review agent.
const maxAgentIterations = 8

// AgentFinding is a single issue reported by a review agent. The JSON tags
// define the schema the agents must emit in their Final Answer.
type AgentFinding struct {
	// Agent is the name of the reporting agent.
	Agent string `json:"agent"`
	// Severity is one of critical, warning, suggestion.
	Severity string `json:"severity"`
	// FilePath is the path of the affected file.
	FilePath string `json:"file_path"`
	// LineNumber is the 1-based line number of the issue in the new file.
	LineNumber int `json:"line_number"`
	// Message describes the problem.
	Message string `json:"message"`
	// CodeSnippet is the exact offending code cited by the agent.
	CodeSnippet string `json:"code_snippet"`
	// Suggestion describes how to fix the problem.
	Suggestion string `json:"suggestion"`
	// Confidence is the agent's self-assessed confidence in [0,1].
	Confidence float64 `json:"confidence"`
}

// ReviewAgent is the uniform interface implemented by every review agent
// (see CLAUDE.md).
type ReviewAgent interface {
	// Name returns the agent identifier, e.g. "security".
	Name() string
	// Review runs the agent over the given file diffs, using the supplied
	// context-collection tools.
	Review(ctx context.Context, diffs []diff.FileDiff, agentTools []tools.Tool) ([]AgentFinding, error)
}

// reviewAgent is the shared ReAct-based implementation used by all review
// agents. Each concrete agent only picks a name and a system prompt.
type reviewAgent struct {
	name       string
	promptFile string
	llm        llms.Model
}

// newReviewAgent creates a review agent identified by name that reasons with
// the system prompt stored in promptFile.
func newReviewAgent(name, promptFile string, llm llms.Model) reviewAgent {
	return reviewAgent{name: name, promptFile: promptFile, llm: llm}
}

// Name implements ReviewAgent.
func (a *reviewAgent) Name() string { return a.name }

// Review implements ReviewAgent. It renders the diff into the agent's
// question, runs the ReAct loop and parses the Final Answer into findings.
func (a *reviewAgent) Review(ctx context.Context, diffs []diff.FileDiff, agentTools []tools.Tool) ([]AgentFinding, error) {
	if len(diffs) == 0 {
		return nil, nil
	}

	systemPrompt, err := loadPrompt(a.promptFile)
	if err != nil {
		return nil, err
	}

	react := agents.NewOneShotAgent(a.llm, agentTools)
	executor := agents.NewExecutor(react, agents.WithMaxIterations(maxAgentIterations))

	question := buildReviewQuestion(systemPrompt, diffs)
	result, err := executor.Call(ctx, map[string]any{"input": question})
	if err != nil {
		return nil, fmt.Errorf("%s agent execution: %w", a.name, err)
	}

	output, ok := result["output"].(string)
	if !ok {
		return nil, fmt.Errorf("%s agent: missing output in executor result", a.name)
	}
	findings, err := parseFindings(output)
	if err != nil {
		return nil, fmt.Errorf("%s agent: %w", a.name, err)
	}
	// Stamp the reporting agent and normalize the fields.
	for i := range findings {
		findings[i].Agent = a.name
		findings[i].Severity = strings.ToLower(strings.TrimSpace(findings[i].Severity))
		if findings[i].Confidence < 0 {
			findings[i].Confidence = 0
		}
		if findings[i].Confidence > 1 {
			findings[i].Confidence = 1
		}
	}
	return findings, nil
}

// loadPrompt reads a system prompt from the embedded prompts package.
func loadPrompt(name string) (string, error) {
	system, err := prompts.Get(name)
	if err != nil {
		return "", fmt.Errorf("load prompt: %w", err)
	}
	return system, nil
}

// buildReviewQuestion assembles the full question fed to the ReAct loop:
// the externalized system prompt followed by the rendered diff.
func buildReviewQuestion(systemPrompt string, diffs []diff.FileDiff) string {
	var b strings.Builder
	b.WriteString(systemPrompt)
	b.WriteString("\n\n=== PULL REQUEST DIFF ===\n")
	b.WriteString(renderDiff(diffs))
	return b.String()
}

// renderDiff converts the parsed file diffs into a compact unified-diff-like
// text the agent can reason over. Added and context lines carry their
// new-file line numbers so findings can reference exact lines.
func renderDiff(diffs []diff.FileDiff) string {
	var b strings.Builder
	for _, f := range diffs {
		path := f.Path
		if path == "" {
			path = f.OldPath
		}
		fmt.Fprintf(&b, "File: %s (%s)\n", path, f.ChangeType)
		if f.IsBinary {
			b.WriteString("  [binary file, no content]\n")
			continue
		}
		for _, h := range f.Hunks {
			fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStartLine, h.OldLines, h.NewStartLine, h.NewLines)
			for _, l := range h.Lines {
				switch l.Type {
				case diff.LineAdded:
					fmt.Fprintf(&b, "+%d\t+%s\n", l.NewNumber, l.Content)
				case diff.LineDeleted:
					fmt.Fprintf(&b, "-%d\t-%s\n", l.OldNumber, l.Content)
				default:
					fmt.Fprintf(&b, " %d\t %s\n", l.NewNumber, l.Content)
				}
			}
		}
	}
	return b.String()
}

// parseFindings extracts the JSON array of findings from the agent's Final
// Answer, tolerating markdown fences and surrounding prose.
func parseFindings(output string) ([]AgentFinding, error) {
	cleaned := strings.TrimSpace(output)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	// Locate the outermost JSON array anywhere in the output.
	start := strings.Index(cleaned, "[")
	if start < 0 {
		return nil, fmt.Errorf("no JSON array in final answer: %q", truncate(output, 200))
	}
	decoder := json.NewDecoder(strings.NewReader(cleaned[start:]))
	var findings []AgentFinding
	if err := decoder.Decode(&findings); err != nil {
		return nil, fmt.Errorf("decode findings JSON: %w", err)
	}
	return findings, nil
}

// truncate shortens s to at most n characters for error messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

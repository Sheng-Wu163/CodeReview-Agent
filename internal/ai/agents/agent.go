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
	"codereviewagent/internal/analysis"
	ctxcontext "codereviewagent/internal/context"
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

// ReviewInput bundles everything a review agent needs for one run
// (FR-21). The struct shape is FROZEN as of v1.3 (spec 11.3).
type ReviewInput struct {
	// Diffs are the changed files under review.
	Diffs []diff.FileDiff
	// PreFindings are the deterministic static analysis findings produced
	// by the pre-analysis pipeline (FR-17), injected into the prompt
	// (FR-22).
	PreFindings []analysis.PreAnalysisFinding
	// Context is the local context bundle (structural changes + callers +
	// callees) injected into the prompt (v1.1 FR-25..FR-28).
	Context ctxcontext.ContextBundle
	// Tools are the context-collection tools available to the agent
	// (FR-11).
	Tools []tools.Tool
}

// ReviewAgent is the uniform interface implemented by every review agent
// (see CLAUDE.md).
type ReviewAgent interface {
	// Name returns the agent identifier, e.g. "security".
	Name() string
	// Review runs the agent over the given review input.
	Review(ctx context.Context, input ReviewInput) ([]AgentFinding, error)
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

// Review implements ReviewAgent. It renders the diff and the static
// analysis preFindings into the agent's question, runs the ReAct loop and
// parses the Final Answer into findings.
func (a *reviewAgent) Review(ctx context.Context, input ReviewInput) ([]AgentFinding, error) {
	if len(input.Diffs) == 0 {
		return nil, nil
	}

	systemPrompt, err := loadPrompt(a.promptFile)
	if err != nil {
		return nil, err
	}

	react := agents.NewOneShotAgent(a.llm, input.Tools)
	executor := agents.NewExecutor(react, agents.WithMaxIterations(maxAgentIterations))

	question := buildReviewQuestion(systemPrompt, input)
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
// the externalized system prompt followed by the three mandatory sections
// (CLAUDE.v1.1.md): "## 代码变更", "## 静态分析发现" and, only when present,
// "## 补充上下文".
func buildReviewQuestion(systemPrompt string, input ReviewInput) string {
	var b strings.Builder
	b.WriteString(systemPrompt)
	b.WriteString("\n\n## 代码变更\n")
	b.WriteString(renderDiff(input.Diffs))
	b.WriteString("\n")
	b.WriteString(renderPreFindings(input.PreFindings))
	if !input.Context.IsEmpty() {
		b.WriteString("\n")
		b.WriteString(renderContext(input.Context))
	}
	return b.String()
}

// renderContext renders the local context bundle under the "## 补充上下文"
// section. It is only called for a non-empty bundle: an empty bundle must
// not render the section at all (CLAUDE.v1.1.md token-saving rule).
func renderContext(bundle ctxcontext.ContextBundle) string {
	var b strings.Builder
	b.WriteString("## 补充上下文\n")
	b.WriteString("Retrieved by static analysis for reference only (may be incomplete).\n")

	if len(bundle.Changes) > 0 {
		b.WriteString("### 结构性变更\n")
		for _, c := range bundle.Changes {
			fmt.Fprintf(&b, "- %s: %s (%s)\n", c.ChangeType, c.FunctionName, c.FilePath)
		}
	}
	if len(bundle.Callers) > 0 {
		b.WriteString("### 调用点\n")
		for _, c := range bundle.Callers {
			fmt.Fprintf(&b, "- %s at %s:%d: %s\n", c.CallerFunc, c.FilePath, c.Line, c.Snippet)
		}
	}
	if len(bundle.Callees) > 0 {
		b.WriteString("### 被调用函数\n")
		for _, c := range bundle.Callees {
			fmt.Fprintf(&b, "- %s.%s%s\n", c.Package, c.Name, c.Signature)
			if c.Doc != "" {
				fmt.Fprintf(&b, "  doc: %s\n", c.Doc)
			}
		}
	}
	return b.String()
}

// renderPreFindings renders the static analysis preFindings as a structured
// list under the mandatory "## 静态分析发现" section header (FR-22, CLAUDE.md
// v1.3 prompt template rule).
func renderPreFindings(preFindings []analysis.PreAnalysisFinding) string {
	var b strings.Builder
	b.WriteString("## 静态分析发现\n")
	if len(preFindings) == 0 {
		b.WriteString("(none - the static analysis pipeline reported no issues for these files)\n")
		return b.String()
	}
	b.WriteString("Findings below were produced by deterministic tools. Verify each one against the code, then add issues the tools cannot see.\n")
	for _, pf := range preFindings {
		fmt.Fprintf(&b, "- ID: %s\n", pf.ID)
		fmt.Fprintf(&b, "  severity: %s | tool: %s | rule: %s\n", pf.Severity, pf.Tool, pf.RuleID)
		fmt.Fprintf(&b, "  location: %s:%d\n", pf.FilePath, pf.Line)
		fmt.Fprintf(&b, "  message: %s\n", pf.Message)
		if pf.Snippet != "" {
			fmt.Fprintf(&b, "  snippet: %s\n", pf.Snippet)
		}
	}
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

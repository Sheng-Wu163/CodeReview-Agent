// Package agents implements the parallel review agents (security, logic,
// performance, lint) and the verifier (FR-05..FR-10).
package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/chains"
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

// maxCompletionTokens bounds each LLM completion. OpenAI-compatible
// reasoning endpoints count hidden reasoning tokens against the budget, so
// the final content is truncated to empty (finish_reason=length) when the
// cap is too tight; a generous budget keeps the reasoning from starving the
// agent's Final Answer.
const maxCompletionTokens = 16384

// PriorFindingVerdict is the agent's judgement of a PRIOR_FINDING
// (v1.10 FR-63).
type PriorFindingVerdict string

const (
	// VerdictStillPresent means the issue still exists in the current code.
	VerdictStillPresent PriorFindingVerdict = "still_present"
	// VerdictFixed means the issue was fixed; the agent must cite the
	// current-code evidence it read.
	VerdictFixed PriorFindingVerdict = "fixed"
	// VerdictUnverifiable means the agent could not validate the issue.
	VerdictUnverifiable PriorFindingVerdict = "unverifiable"
)

// PriorFinding is one unresolved finding from the previous review anchor
// (v1.10 FR-63). Fingerprint follows v1.11 FR-66.
type PriorFinding struct {
	Fingerprint string `json:"fingerprint"`
	// Agent is the producing agent (v1.10 D3); each agent only receives
	// its own PRIOR_FINDINGS.
	Agent    string `json:"agent"`
	FilePath string `json:"file_path"`
	LineNumber  int    `json:"line_number"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
}

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

	// PriorFingerprint identifies the PRIOR_FINDING this entry judges
	// (v1.10 FR-63); empty for ordinary new findings.
	PriorFingerprint string `json:"prior_fingerprint,omitempty"`
	// Verdict is still_present/fixed/unverifiable for PRIOR_FINDING entries.
	Verdict PriorFindingVerdict `json:"verdict,omitempty"`
	// Evidence is the current code the agent read to justify "fixed".
	Evidence string `json:"evidence,omitempty"`
}

// ReviewInput bundles everything a review agent needs for one run
// (FR-21). The v1.3 shape was extended in v1.10 with PriorFindings and Scope
// via specs/changes/v1.10-prior-findings/proposal.md (approved).
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
	// PriorFindings are unresolved findings from the previous anchor
	// (v1.10 FR-63). Empty means the PRIOR_FINDINGS section is omitted.
	PriorFindings []PriorFinding
	// Scope is "full" or "incremental" (v1.10 FR-61); it is used only to
	// declare the review range in the prompt.
	Scope string
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
	// modelWindow and compression configure the v1.9 in-loop context
	// compression (FR-53/FR-54). Zero values use the spec defaults.
	modelWindow int
	compression ctxcontext.CompressionConfig
}

// SetCompression configures v1.9 in-loop memory compression for this agent.
func (a *reviewAgent) SetCompression(modelWindow int, cfg ctxcontext.CompressionConfig) {
	a.modelWindow = modelWindow
	a.compression = cfg
}

// newReviewAgent creates a review agent identified by name that reasons with
// the system prompt stored in promptFile.
func newReviewAgent(name, promptFile string, llm llms.Model) reviewAgent {
	return reviewAgent{name: name, promptFile: promptFile, llm: llm}
}

// Name implements ReviewAgent.
func (a *reviewAgent) Name() string { return a.name }

// PromptHash returns the SHA-256 of this agent's prompt file, used as the
// v1.10 checkpoint prompt_hash.
func (a *reviewAgent) PromptHash() string {
	hash, err := prompts.Hash(a.promptFile)
	if err != nil {
		return ""
	}
	return hash
}

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
	// v1.9: compress older tool observations before every planning step.
	planner := &compressingAgent{
		inner:       react,
		modelWindow: a.modelWindow,
		cfg:         a.compression,
	}
	executor := agents.NewExecutor(planner, agents.WithMaxIterations(maxAgentIterations))

	question := buildReviewQuestion(systemPrompt, input)
	result, err := executor.Call(ctx, map[string]any{"input": question},
		chains.WithMaxTokens(maxCompletionTokens))
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
		enforcePriorVerdict(&findings[i])
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

// enforcePriorVerdict applies the v1.10 parse-layer rule: a "fixed" verdict
// is only accepted when the agent cites evidence read from the current code;
// otherwise it is downgraded to "unverifiable". Unknown/missing verdicts on a
// PRIOR_FINDING are also treated as unverifiable.
func enforcePriorVerdict(f *AgentFinding) {
	if f.PriorFingerprint == "" {
		return
	}
	switch f.Verdict {
	case VerdictStillPresent, VerdictUnverifiable:
		return
	case VerdictFixed:
		if strings.TrimSpace(f.Evidence) == "" {
			f.Verdict = VerdictUnverifiable
		}
	default:
		f.Verdict = VerdictUnverifiable
	}
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
	if input.Scope == "incremental" {
		b.WriteString("\n## 审查范围\n")
		b.WriteString("本轮为增量审查：仅审查下方 diff 中的变更，diff 之外的代码不在本轮范围内。\n")
	}
	if len(input.PriorFindings) > 0 {
		b.WriteString("\n")
		b.WriteString(renderPriorFindings(input.PriorFindings))
	}
	if !input.Context.IsEmpty() {
		b.WriteString("\n")
		b.WriteString(renderContext(input.Context))
	}
	return b.String()
}

// renderPriorFindings renders the previous review's unresolved findings and
// the mandatory verdict output contract (v1.10 FR-63).
func renderPriorFindings(prior []PriorFinding) string {
	var b strings.Builder
	b.WriteString("## 上一轮遗留问题（PRIOR_FINDINGS）\n")
	b.WriteString("逐条验证下列问题在当前代码中是否仍然存在。每个条目输出一个 JSON 对象（与普通 finding 放在同一数组中）：\n")
	b.WriteString("{\"prior_fingerprint\":\"<原指纹>\",\"verdict\":\"still_present|fixed|unverifiable\",\"evidence\":\"<当前代码证据>\",\"severity\":\"...\",\"file_path\":\"...\",\"line_number\":1,\"message\":\"...\"}\n")
	b.WriteString("规则：\n")
	b.WriteString("- still_present：问题仍在，继续作为 finding 输出。\n")
	b.WriteString("- fixed：问题已修复；evidence 必须是你通过工具读取到的当前代码，不能只凭 diff 未出现该行。\n")
	b.WriteString("- unverifiable：文件无法读取或无法确认；不得判定为 fixed。\n")
	b.WriteString("- 判为 fixed 的条目不要作为新 finding 输出。\n")
	for _, p := range prior {
		fmt.Fprintf(&b, "- fingerprint: %s\n", p.Fingerprint)
		fmt.Fprintf(&b, "  location: %s:%d | severity: %s\n", p.FilePath, p.LineNumber, p.Severity)
		fmt.Fprintf(&b, "  message: %s\n", p.Message)
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

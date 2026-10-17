package eval

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codereviewagent/internal/ai/agents"
)

func TestRenderMarkdownNoUBV(t *testing.T) {
	report := BuildReport("v1.6", "v1.1", EvalMetrics{
		TruePositives: 2, FalseNegatives: 0, UnreviewedDiscovery: 0,
		Precision: 1, Recall: 1,
	}, &EvalMetrics{Precision: 0.5, Recall: 0.5}, nil)

	md := RenderMarkdown(report)

	assert.Contains(t, md, "# 评估报告")
	assert.Contains(t, md, "v1.6")
	assert.Contains(t, md, "v1.1")
	assert.Contains(t, md, "无待复核发现")
	assert.Contains(t, md, "改善") // 1.0 vs 0.5 baseline
}

func TestRenderMarkdownUBVItems(t *testing.T) {
	report := BuildReport("v1.6", "", EvalMetrics{
		TruePositives: 1, FalseNegatives: 1, UnreviewedDiscovery: 2,
		Precision: 0.3333, Recall: 0.5,
	}, nil, []agents.AgentFinding{
		{
			Agent: "logic", Severity: "warning", FilePath: "db.go", LineNumber: 16,
			Message: "unhandled error", CodeSnippet: "json.Unmarshal(data, &result)",
			Suggestion: "check the error",
		},
		{
			Agent: "lint", Severity: "suggestion", FilePath: "a.go", LineNumber: 3,
			Message: "bad naming", CodeSnippet: "func get_user() {}",
		},
	})

	md := RenderMarkdown(report)

	// Detailed list with code-block highlighting (CLAUDE.v.1.6.md).
	assert.Contains(t, md, "### 1. logic @ db.go:16")
	assert.Contains(t, md, "### 2. lint @ a.go:3")
	assert.Contains(t, md, "```go\njson.Unmarshal(data, &result)\n```")
	assert.Contains(t, md, "check the error")
	// Copy-paste JSON promotion snippets (AC-65).
	assert.Contains(t, md, "```json")
	assert.Contains(t, md, `"FilePath":"db.go"`)
	assert.Contains(t, md, `"Line":16`)
	assert.Contains(t, md, `"Category":"logic"`)
	// The lint agent maps to the "style" category.
	assert.Contains(t, md, `"Category":"style"`)
	assert.Contains(t, md, "ground_truth.json")
}

func TestRenderMarkdownTruncatesAt50(t *testing.T) {
	items := make([]agents.AgentFinding, 55)
	for i := range items {
		items[i] = agents.AgentFinding{
			Agent: "logic", Severity: "warning", FilePath: "f.go",
			LineNumber: i + 1, Message: "m",
		}
	}
	report := BuildReport("v1.6", "", EvalMetrics{UnreviewedDiscovery: 55}, nil, items)

	md := RenderMarkdown(report)

	// Truncation warning, verbatim (CLAUDE.v.1.6.md).
	require.Contains(t, md, "⚠️ 警告：发现超过 50 条未匹配项，请检查 Agent 是否发生幻觉。")
	require.Contains(t, md, "仅展示前 50 条")
	// Items 1..50 rendered, 51+ not.
	assert.Contains(t, md, "### 50. logic @ f.go:50")
	assert.NotContains(t, md, "### 51. logic @ f.go:51")
}

func TestRenderMarkdownFirstRun(t *testing.T) {
	report := BuildReport("", "", EvalMetrics{}, nil, nil)
	md := RenderMarkdown(report)
	assert.Contains(t, md, "首次运行")
	assert.Contains(t, md, "-")
}

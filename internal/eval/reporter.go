package eval

import (
	"encoding/json"
	"fmt"
	"strings"

	"codereviewagent/internal/ai/agents"
)

// maxUBVReport caps the UBV items rendered into the markdown report; beyond
// it the list is truncated and a prominent warning is emitted (spec 13.4 /
// CLAUDE.v.1.6.md).
const maxUBVReport = 50

// RenderMarkdown renders the evaluation report as markdown (FR-32/FR-33):
// a metrics summary with trend deltas followed by the UBV review queue,
// where each entry carries a copy-paste JSON snippet for promoting it into
// ground_truth.json after human confirmation.
func RenderMarkdown(report EvalReport) string {
	var b strings.Builder
	b.WriteString("# 评估报告\n\n")

	fmt.Fprintf(&b, "- 评估版本: **%s**\n", orDash(report.Version))
	if report.BaselineVersion == "" {
		b.WriteString("- 基线版本: _未提供（首次运行，Delta 为 0）_\n")
	} else {
		fmt.Fprintf(&b, "- 基线版本: **%s**\n", report.BaselineVersion)
	}
	b.WriteString("\n")

	b.WriteString("## 指标（相对趋势）\n\n")
	b.WriteString("| 指标 | 当前值 | 相对基线 |\n")
	b.WriteString("|------|--------|----------|\n")
	m := report.Metrics
	fmt.Fprintf(&b, "| True Positives (tp) | %d | - |\n", m.TruePositives)
	fmt.Fprintf(&b, "| False Negatives (fn) | %d | - |\n", m.FalseNegatives)
	fmt.Fprintf(&b, "| Unreviewed Discovery (ubv) | %d | - |\n", m.UnreviewedDiscovery)
	fmt.Fprintf(&b, "| Precision | %.1f%% | %s |\n", m.Precision*100, trend(report.PrecisionDelta, report.BaselineVersion))
	fmt.Fprintf(&b, "| Recall | %.1f%% | %s |\n", m.Recall*100, trend(report.RecallDelta, report.BaselineVersion))
	b.WriteString("\n")
	if report.BaselineVersion != "" {
		fmt.Fprintf(&b, "- Precision Delta: **%+.3f**（%.1f%% → %.1f%%）\n",
			report.PrecisionDelta, (m.Precision-report.PrecisionDelta)*100, m.Precision*100)
		fmt.Fprintf(&b, "- Recall Delta: **%+.3f**（%.1f%% → %.1f%%）\n",
			report.RecallDelta, (m.Recall-report.RecallDelta)*100, m.Recall*100)
		b.WriteString("\n")
	}

	b.WriteString("## 复核队列（UBV - Unexpected But Valid）\n\n")
	if len(report.UBVItems) == 0 {
		b.WriteString("无待复核发现：Agent 报告的全部发现均已匹配 Ground Truth。\n")
		return b.String()
	}

	if len(report.UBVItems) > maxUBVReport {
		b.WriteString("⚠️ 警告：发现超过 50 条未匹配项，请检查 Agent 是否发生幻觉。\n\n")
		fmt.Fprintf(&b, "共 %d 条，以下仅展示前 %d 条。\n\n", len(report.UBVItems), maxUBVReport)
	} else {
		fmt.Fprintf(&b, "共 %d 条待复核发现。以下发现未匹配 Ground Truth，**不代表误报**：请人工确认后，将对应 JSON 片段追加到该样本的 `ground_truth.json`，下次评估将自动修正指标。\n\n", len(report.UBVItems))
	}

	for i, item := range report.UBVItems {
		if i >= maxUBVReport {
			break
		}
		renderUBVItem(&b, i+1, item)
	}
	return b.String()
}

// renderUBVItem renders one UBV entry with highlighted code and the
// promotion JSON snippet.
func renderUBVItem(b *strings.Builder, seq int, item agents.AgentFinding) {
	fmt.Fprintf(b, "### %d. %s @ %s:%d\n\n", seq, item.Agent, item.FilePath, item.LineNumber)
	fmt.Fprintf(b, "- 严重级别: %s\n", item.Severity)
	fmt.Fprintf(b, "- Agent 消息: %s\n", item.Message)
	if item.Suggestion != "" {
		fmt.Fprintf(b, "- 修改建议: %s\n", item.Suggestion)
	}
	b.WriteString("\n```go\n")
	if item.CodeSnippet != "" {
		b.WriteString(item.CodeSnippet)
	} else {
		b.WriteString("// (agent 未提供代码片段)")
	}
	b.WriteString("\n```\n\n")

	gt := GroundTruthItem{
		FilePath: item.FilePath,
		Line:     item.LineNumber,
		Severity: item.Severity,
		Category: categoryOf(item.Agent),
		Message:  item.Message,
	}
	data, err := json.Marshal(gt)
	if err != nil {
		data = []byte("{}")
	}
	b.WriteString("人工确认有效后，将以下 JSON 片段追加到对应样本的 `ground_truth.json`：\n\n")
	b.WriteString("```json\n")
	b.Write(data)
	b.WriteString("\n```\n\n")
}

// trend renders the relative-trend cell for a delta.
func trend(delta float64, baselineVersion string) string {
	if baselineVersion == "" {
		return "首次运行"
	}
	switch {
	case delta > 0:
		return fmt.Sprintf("改善 (+%.1f%%)", delta*100)
	case delta < 0:
		return fmt.Sprintf("下降 (%.1f%%)", delta*100)
	default:
		return "持平"
	}
}

// orDash replaces an empty string with a dash.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

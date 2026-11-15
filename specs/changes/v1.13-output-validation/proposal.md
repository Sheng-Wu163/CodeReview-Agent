# 变更提案：AgentResult.DroppedFindings 与 eval injection_success_count（v1.13）

- 状态：✅ 已审批（2026-10-23，用户批准冻结结构变更）
- 关联：spec.md §24 FR-75/FR-76；tasks.md T80/T81

## 1. 改动原因
v1.13 要求在 Verifier 之前对 Agent 输出做结构校验，并统计被丢弃的 finding 数；同时评估报告需要量化注入样本的成功次数。

## 2. 改动内容
```go
type AgentResult struct {
    Status          AgentStatus
    Findings        []agents.AgentFinding
    ToolCallCount   int
    DroppedFindings int // 新增：结构校验丢弃的 finding 数
    ErrorReason     string
    ExecutionLogs   []ToolLog
}

type EvalMetrics struct {
    // ...既有字段
    InjectionSuccessCount int `json:"injection_success_count"` // 新增
}
```

## 3. 兼容性
- 两个字段均为零值兼容；Checkpoint 的 `result_json` 自动包含 `dropped_findings`。
- `injection_success_count` 在报告中显示，不影响既有 TP/FN/UBV 计算。

## 4. 影响范围
- `internal/harness/types.go`、`internal/harness/harness_impl.go`
- `internal/ai/agents/validate.go`
- `internal/eval/types.go`、`internal/eval/evaluator.go`、`internal/eval/reporter.go`

## 5. 回滚
移除新增字段与统计逻辑即可回到 v1.12 行为。

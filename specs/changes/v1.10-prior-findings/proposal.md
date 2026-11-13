# 变更提案：ReviewInput 新增 PriorFindings（v1.10 FR-63）

- 状态：✅ 已审批（2026-10-23，用户批准 D1-D4 推荐选项）
- 关联：spec.md §20 FR-63 / §20.3；tasks.md T65；CLAUDE.v.1.3 接口冻结规则

## 1. 改动原因
v1.10 需要把上一轮未解决的 finding 注入本轮 Prompt，由 Agent 判定 `still_present` / `fixed` / `unverifiable`。
当前 `agents.ReviewInput`（v1.3 冻结）只包含 `Diffs`、`PreFindings`、`Context`、`Tools`，没有承载跨回合状态的位置。

## 2. 改动内容（已批准）
```go
type ReviewInput struct {
    Diffs       []diff.FileDiff
    PreFindings []analysis.PreAnalysisFinding
    Context     ctxcontext.ContextBundle
    Tools       []tools.Tool
    // 上一轮锚点中仍未解决的 finding；为空表示首次审查，不注入 PRIOR_FINDINGS 段。
    PriorFindings []PriorFinding
    // full | incremental，用于 Prompt 声明审查范围（FR-61）。
    Scope string
}

type PriorFinding struct {
    Fingerprint string // v1.11 FR-66 指纹；D2 已批准前移到 v1.10 实现
    Agent       string // 产出该 finding 的 Agent；D3 每个 Agent 只收到自己的 PRIOR
    FilePath    string
    LineNumber  int
    Severity    string
    Message     string
}
```
`ReviewAgent` 接口签名不变。

## 3. 影响范围
- `internal/ai/agents/agent.go`：`buildReviewQuestion` 在 TASK_STATE 段追加 `PRIOR_FINDINGS`（仅当非空）。
- `internal/ai/agents/verifier.go`：解析层强制 `fixed` 需证据，否则降级为 `unverifiable`（实际落在 `agent.go` 的 `enforcePriorVerdict`）。
- `internal/orchestrator/orchestrator.go`：从 Pipeline 接收 PriorFindings 并按 Agent 过滤后填入 `ReviewInput`。

## 4. 兼容性
- 新字段为零值时行为与 v1.9 完全一致（不注入、不改变输出），既有测试无需修改。
- 数据库结构变化由 007/008 迁移承担。

## 5. 回滚
移除 `PriorFindings` / `Scope` 字段与 TASK_STATE 注入分支即可回到 v1.9 行为。

## 6. 已确认决策

| 编号 | 决策 | 结果 |
|------|------|------|
| D1 | v1.10 / v1.11 规格进入实现 | 批准 |
| D2 | 指纹依赖顺序 | 将 FR-66 指纹前移到 v1.10 实现 |
| D3 | PRIOR_FINDINGS 判定主体 | 由产出该 finding 的同名 Agent 判定，每个 Agent 只收到自己的 PRIOR |
| D4 | 人工已 resolve 的线程 | 不注入，视为人工已处理 |

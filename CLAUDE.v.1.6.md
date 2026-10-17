# v1.6 评估体系 - 专项指令

## 模块专属规范

- **包路径**：所有新代码放在 `internal/eval/` 下，入口程序放在 `cmd/eval/main.go`。
- **类型定义**：必须严格遵循 `specs/code-review-agent/spec.md` 13.3 节的 Interface Freeze Declaration。
- **匹配容差**：在比对 Agent 发现与 Ground Truth 时，行号匹配必须允许 `+/- 2` 行的容差，防止因为空行差异导致误判。
- **Mock 隔离**：`evaluator_test.go` 中绝对不能发起真实的 LLM 请求。必须定义 `MockOrchestrator` 返回预设的 `[]VerifiedFinding`。

## 报告生成规范

- `report.md` 的格式必须清晰，UBV 部分必须包含代码块高亮，方便人工快速复核。
- 如果 UBV 数量超过 50 条，必须截断并在顶部输出显眼的警告：“⚠️ 警告：发现超过 50 条未匹配项，请检查 Agent 是否发生幻觉。”

## Token 节省策略（执行任务时必须遵守）

1. **T32 构造数据时**：不要让 Agent 去全仓库找代码，手工编写一个极简的 10 行 Go 文件的 diff 即可，重点是跑通流程，不要在测试集上耗费大量 token。
2. **测试执行**：写完评估代码后，先跑 `go test -run TestEvaluator ./internal/eval/`，通过后再去运行 `make eval` 触发真实 LLM 审查。
3. **失败 2 次即停止**：如果 JSON 解析或行号匹配测试失败超过 2 次，立刻标记任务为 `🚫 需人工介入`，把错误日志贴出来。
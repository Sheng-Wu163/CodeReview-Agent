# v1.4 隐式反馈追踪 - 专项指令

## 模块专属规范

- **包路径**：新代码放在 `internal/feedback/` 下。
- **纯文本比对**：`diff_compare.go` 中的代码相似度计算必须是纯函数，禁止在内部发起网络请求。输入为 `oldSnippet string, newSnippet string`，输出为 `float64` (0.0-1.0)。
- **相似度阈值**：相似度 < 0.8 视为被修改，通过配置文件读取，默认 0.8。
- **GitHub API 调用边界**：获取旧代码使用 `github/client.go` 封装的方法，不要在 Tracker 里直接写 HTTP 请求。

## 反馈状态机（严格遵循）

- `pending` -> `accepted_implicit`：代码被修改或文件被删除。
- `pending` -> `ignored_implicit`：PR merged 且代码未变。
- 不允许修改已经处于 `accepted_implicit` 或 `ignored_implicit` 的记录（幂等保护）。

## Token 节省策略（执行任务时必须遵守）

1. **T39-T42 极其轻量**：这一轮迭代完全不需要使用 LLM。这是一个纯粹的工程数据处理任务，切勿在代码中引入 `llm.Call` 或任何 Agent 逻辑。
2. **测试执行**：测试时不要调用真实的 GitHub API。请 Mock `github.Client`，返回硬编码的代码片段用于相似度比对测试。
3. **T43 集成测试**：E2E 测试也使用 Mock，不要真实发起 PR。构造两个假的 Commit 数据和 Diff 数据传入 Tracker 即可验证整个闭环。
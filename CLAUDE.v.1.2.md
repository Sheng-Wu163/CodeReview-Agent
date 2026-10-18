# v1.2 自适应审查策略 - 专项指令

## 模块专属规范

- **包路径**：新代码放在 `internal/review/` 下。
- **绝对确定性**：`ComplexityAssessor` 中**绝对不能**调用 LLM 或任何网络请求。只能基于 `diff.FileDiff` 的字符串长度、行数、文件路径进行数学统计。
- **配置读取**：敏感路径（`sensitive_paths`）从 `config.StaticAnalysis.Adaptive.SensitivePaths` 读取，如果为空则使用内置默认值。

## 策略开关边界

- **禁止动态调参**：不要修改 Agent 的 `max_iterations`，不要修改 Prompt 的温度。你只负责决定“哪些 Agent 启动，哪些不启动”。
- **Verifier 跳过逻辑**：如果 `EnableVerifier` 为 false，直接将各审查 Agent 的 `AgentFinding` 转换为 `VerifiedFinding`（置信度直接设为 1.0），不要调用大模型验证。

## Token 节省策略（执行任务时必须遵守）

1. **T34/T35 极为轻量**：写这两个任务的代码时，不要引入任何外部依赖。只使用标准库的 `strings` 和 `path/filepath` 即可。
2. **测试执行**：测试时使用 mock 的 `[]diff.FileDiff`，不要真的去解析真实的复杂 diff。
3. **T37 构造数据**：在 `eval/dataset/sample-trivial/` 放一个只有 5 行变更的 diff，在 `sample-complex/` 放一个 300 行的假 diff。不要把真实项目代码复制进去。
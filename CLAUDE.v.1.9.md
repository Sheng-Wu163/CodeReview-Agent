# v1.9 上下文装配与记忆压缩 - 专项指令

## 模块专属规范
- **包路径**：新代码放在 `internal/context/` 下。
- **纯函数优先**：`Compressor` 和 `Tokenizer` 必须是纯函数，禁止引入外部网络依赖。
- **双轨阈值**：在 `ShouldCompress` 中，任何满足 `Tokens > TriggerRatio * Window` 或 `Tokens > AbsoluteLimit` 的条件都必须返回 true。
- **滑动窗口**：压缩时必须依据 `Sequence` 字段识别最新块，绝不能用数组索引，防止乱序导致错误保留。

## 熔断拦截
- 修改工具代码时，**必须在最外层加截断**。对 `search_code`，截断 Match 结果数量；对 `read_file`，截断返回文本的字符数。

## Token 节省策略（执行任务时必须遵守）

1. **不使用复杂分词器**：继续使用 v1.9 早期确定的轻量字符估算，不要引入 `tiktoken-go`。
2. **测试执行**：测试压缩时构造假的 `ContextBlock`，并手动注入 `Tokens` 值，不要真实调用估算函数去算大文本。
3. **修改 Harness 时克制**：只修改 `preparePrompt`，切勿破坏 v1.8 的震荡检测和门禁逻辑。
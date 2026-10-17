# v1.1 局部上下文注入 - 专项指令

## 模块专属规范

- **包路径**：所有新代码放在 `internal/context/` 下。
- **类型定义**：必须严格遵循 `specs/code-review-agent/spec.md` 12.3 节的 Interface Freeze Declaration。禁止增删字段。
- **AST 解析**：必须使用 `golang.org/x/tools/go/packages` 和 `go/ast`。不要自己写正则去匹配代码。
- **超时控制**：`go/packages` 加载必须包裹 `context.WithTimeout`，超时时间设为 10 秒。
- **纯函数约束**：检测器和提取器尽量写成纯函数，方便单元测试传入固定的 AST。

## Prompt 模板规范

- **User message 结构**：必须分为 `## 代码变更`、`## 静态分析发现`、`## 补充上下文` 三段。
- **防幻觉声明**：必须放在 System prompt 的最后一句，原文粘贴，不可修改。
- **空上下文处理**：如果 `ContextBundle` 为空，`## 补充上下文` 段落必须**完全不渲染**，不要输出 "无补充上下文" 之类的话，节省 token。

## Token 节省策略（执行任务时必须遵守）

1. **先编译后测试**：写完代码先跑 `go build ./...`，通过后再跑 `go test -run TestXxx ./internal/context/...`，严禁直接跑全量测试。
2. **分批读取**：如果需要阅读多个关联不强的文件，分多次调用 `read_file`，不要一次性全读。
3. **失败 2 次即停止**：如果某个单元测试连续修改 2 次仍然失败，立即停止并在任务状态中标记 `🚫 需人工介入`，说明错误原因，不要无限重试。
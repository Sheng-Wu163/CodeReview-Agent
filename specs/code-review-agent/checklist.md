# 验收清单

## 功能验收

### Webhook 与事件处理

- [ ] AC-01: Given 携带有效 HMAC-SHA256 签名的 GitHub Webhook 请求, When POST /webhook, Then 返回 200
- [ ] AC-02: Given 签名无效或缺失的请求, When POST /webhook, Then 返回 401
- [ ] AC-03: Given `pull_request` 事件的 `opened` 动作, When 事件被处理, Then 提取 PR 编号、仓库全名、head SHA，并触发审查流程

### Diff 解析与过滤

- [ ] AC-04: Given 包含 3 个文件变更的 PR, When Diff 被解析, Then 正确识别每个文件的变更类型和变更行号范围
- [ ] AC-05: Given PR 包含 `vendor/github.com/foo/bar.go` 和 `main.go`, When 文件过滤执行, Then 只有 `main.go` 进入审查队列
- [ ] AC-05b: Given 变更文件为 `*_generated.go`, `*.pb.go`, `go.sum`, `README.md`, When 文件过滤执行, Then 这些文件被全部排除

### Agent 审查能力

- [ ] AC-06: Given 变更中包含 `db.Query("SELECT * FROM users WHERE id = " + userID)`, When 安全 Agent 执行, Then 报告 1 条 critical 级别的 SQL 注入发现
- [ ] AC-06b: Given 变更中包含硬编码的 `const apiKey = "sk-xxxx"`, When 安全 Agent 执行, Then 报告 1 条 critical 级别的硬编码密钥发现
- [ ] AC-07: Given 变更中包含 `json.Unmarshal(data, &result)` 且错误被忽略, When 逻辑 Agent 执行, Then 报告 1 条 warning 级别的未处理错误发现
- [ ] AC-07b: Given 变更中包含 `go func() { ch <- val }()` 且无退出条件, When 逻辑 Agent 执行, Then 报告 1 条 warning 级别的 goroutine 泄漏风险
- [ ] AC-08: Given 变更中包含循环内字符串拼接 `s += item`, When 性能 Agent 执行, Then 报告 1 条 warning 级别的性能问题
- [ ] AC-09: Given 变更中包含不符合 Go 命名规范的函数名, When 规范 Agent 执行, Then 报告 1 条 suggestion 级别的命名建议

### 验证与过滤

- [ ] AC-10: Given 安全 Agent 报告了一条发现但引用的代码片段在源文件中不存在, When 验证 Agent 执行, Then 该条发现被过滤
- [ ] AC-11: Given 安全 Agent 和逻辑 Agent 报告了同一行同一类型的问题, When 验证 Agent 执行去重, Then 最终输出中只保留 1 条
- [ ] AC-12: Given 所有 Agent 的原始发现, When 验证 Agent 执行, Then 每条最终发现都附带 0.0-1.0 的置信度分数

### 并行执行

- [ ] AC-13: Given 4 个审查 Agent 被启动, When 编排器执行, Then 4 个 Agent 并行运行（总耗时接近最慢的单个 Agent，而非累加）
- [ ] AC-14: Given 性能 Agent 因 LLM API 超时失败, When 编排器执行, Then 其他 3 个 Agent 正常完成，最终结果中不包含性能 Agent 的发现，且日志记录了失败原因

### GitHub 回写

- [ ] AC-15: Given 审查完成且存在 2 条 warning 和 1 条 suggestion, When Review 回写执行, Then GitHub PR 出现 1 条 Review（含 summary）和 3 条 inline comments
- [ ] AC-16: Given 审查完成且存在至少 1 条 critical 发现, When Commit Status 更新执行, Then PR 的 commit status 被设置为 `failure`
- [ ] AC-17: Given 审查完成且无任何发现, When Review 回写执行, Then 提交一条 "LGTM ✅" 评论，commit status 设为 success

### 持久化与仪表盘

- [ ] AC-18: Given 一次审查完成, When 审查记录写入数据库, Then 可通过 `GET /api/reviews/{id}` 查询到完整记录
- [ ] AC-19: Given 系统已执行 3 次审查, When 访问 `/dashboard`, Then 页面展示 3 条记录（按时间倒序）
- [ ] AC-20: Given 点击某条审查记录, When 进入详情页, Then 展示该次审查的所有发现（含严重度、置信度、修复建议）

## 边界验收

- [ ] AC-21: Given PR diff 仅包含注释修改（无实质性代码变更）, When 审查流程执行, Then 跳过 LLM 调用，直接提交 "No meaningful changes" 评论
- [ ] AC-22: Given LLM API 调用超过 60 秒未返回, When Agent 执行, Then 该 Agent 被标记为超时失败，其他 Agent 不受影响
- [ ] AC-23: Given Semgrep CLI 未安装, When 安全 Agent 执行, Then 降级为纯 LLM 审查，日志记录 "semgrep not available, falling back to LLM-only"
- [ ] AC-24: Given PR 包含二进制文件（如图片）, When 文件过滤执行, Then 该文件被跳过
- [ ] AC-25: Given 同一 PR 在 30 秒内被重复触发 Webhook, When 第二次事件到达, Then 基于 head SHA 去重，第二次被忽略

## 安全验收

- [ ] AC-26: Given 项目代码库, When 执行 `grep -r "sk-\|password\|secret" --include="*.go"`，Then 不出现任何硬编码的密钥
- [ ] AC-27: Given 环境变量未设置 `WEBHOOK_SECRET`, When 服务启动, Then 启动失败并输出明确的错误信息
- [ ] AC-28: Given 一个不存在的 PR 编号, When 尝试获取 PR 信息, Then 返回 404 而非 panic

## 代码质量验收

- [ ] AC-29: 执行 `golangci-lint run ./...` 无 error 级别问题
- [ ] AC-30: 执行 `go test ./... -race` 全部通过
- [ ] AC-31: 执行 `go vet ./...` 无输出
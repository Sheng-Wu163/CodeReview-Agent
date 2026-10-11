# 任务清单

## 任务依赖图

T1 → T2 → T3 → T4
T1 → T5
T4 → T6 → T7 → T8
T5 → T9 → T10
T9 → T11
T3 → T12
T10 + T11 + T12 → T13 → T14

## 阶段一：基础设施（T1-T3）

### T1: 项目脚手架与配置管理
- **描述**：初始化 Go module（module name: `codereviewagent`），创建项目目录结构，实现基于 Viper 的配置加载（支持环境变量和 YAML 配置文件）
- **文件路径**：
  - `go.mod`
  - `cmd/server/main.go`
  - `internal/config/config.go`
  - `configs/config.yaml.example`
  - `Makefile`
- **完成标准**：`go build ./...` 通过；`make run` 启动服务并输出 "server started on :8080"
- **依赖**：无
- **预估**：1h
- **状态**：✅ 已完成

### T2: HTTP 服务骨架与 Webhook 端点
- **描述**：使用 chi 路由创建 HTTP 服务，实现 `POST /webhook` 端点，包含 HMAC-SHA256 签名验证中间件。验证失败返回 401，验证通过返回 200 并打印事件类型
- **文件路径**：
  - `internal/handler/webhook.go`
  - `internal/middleware/hmac.go`
  - `internal/handler/webhook_test.go`
  - `internal/middleware/hmac_test.go`
- **完成标准**：单元测试通过；使用 `curl` 发送带有效签名的请求返回 200，无效签名返回 401
- **依赖**：T1
- **预估**：2h
- **状态**：✅ 已完成

### T3: 数据库连接与迁移
- **描述**：使用 pgx 连接 PostgreSQL，创建 `reviews` 和 `findings` 两张表的迁移文件。实现基本的 Repository 接口（CreateReview, GetReview, ListReviews, CreateFinding）
- **文件路径**：
  - `internal/db/db.go`
  - `internal/db/migrations/001_create_tables.sql`
  - `internal/repository/review_repo.go`
  - `internal/repository/review_repo_test.go`
- **完成标准**：迁移可执行；Repository 的增删查测试通过（使用 testcontainers 或本地 PG）
- **依赖**：T1
- **预估**：3h
- **状态**：✅ 已完成

## 阶段二：Diff 解析与文件过滤（T4-T5）

### T4: Diff 解析器
- **描述**：实现 PR diff 解析器，输入为 GitHub PR 的 diff 文本，输出为结构化的 `FileDiff` 列表。使用 go-git 的 diff 包解析 unified diff 格式。每个 `FileDiff` 包含：文件路径、变更类型、变更块列表（每个块包含：起始行号、行数、变更内容、上下文行）
- **文件路径**：
  - `internal/diff/parser.go`
  - `internal/diff/types.go`
  - `internal/diff/parser_test.go`
- **完成标准**：对标准 unified diff 输入的解析测试通过（覆盖新增/修改/删除三种文件，以及多变更块场景）
- **依赖**：T1
- **预估**：3h
- **状态**：✅ 已完成

### T5: 文件过滤器
- **描述**：实现文件过滤逻辑，根据文件路径和扩展名排除不需要审查的文件。过滤规则可配置（从 config.yaml 读取）
- **文件路径**：
  - `internal/diff/filter.go`
  - `internal/diff/filter_test.go`
- **完成标准**：对 `vendor/`、`*_generated.go`、`*.md`、`go.sum`、`*.pb.go` 的过滤测试通过
- **依赖**：T1
- **预估**：1h
- **状态**：✅ 已完成

## 阶段三：Agent 实现（T6-T12）

### T6: LLM 客户端封装与工具接口
- **描述**：封装 LangChainGo 的 LLM 初始化（支持 OpenAI 和 Anthropic，通过配置切换），实现 `ReviewTool` 接口和四个核心工具：`ReadFileTool`、`SearchCodeTool`、`GetFunctionDefTool`、`RunStaticAnalysisTool`。每个工具实现 `tools.Tool` 接口（Name、Description、Call）
- **文件路径**：
  - `internal/ai/llm.go`
  - `internal/ai/tools/read_file.go`
  - `internal/ai/tools/search_code.go`
  - `internal/ai/tools/get_function.go`
  - `internal/ai/tools/static_analysis.go`
  - `internal/ai/tools/tools_test.go`
- **完成标准**：`var _ tools.Tool = ReadFileTool{}` 编译通过；每个工具单元测试通过
- **依赖**：T1
- **预估**：4h
- **状态**：✅ 已完成

### T7: 安全审查 Agent
- **描述**：实现安全 Agent。输入为 FileDiff 列表，输出为 `AgentFinding` 列表。Agent 使用 LangChainGo 的 AgentExecutor（ReAct 模式），配备 ReadFile 和 RunStaticAnalysis 工具。System prompt 要求检查 SQL 注入、命令注入、硬编码密钥、不安全加密
- **文件路径**：
  - `internal/ai/agents/security.go`
  - `internal/ai/agents/security_test.go`
  - `internal/ai/prompts/security.txt`
- **完成标准**：对包含 SQL 注入的测试 diff，返回至少 1 条 critical 发现；使用 mock LLM 的单元测试通过
- **依赖**：T6
- **预估**：4h
- **状态**：✅ 已完成

### T8: 逻辑/质量审查 Agent
- **描述**：实现逻辑 Agent。ReAct 模式，工具配置同安全 Agent。System prompt 要求检查空指针、边界条件、未处理错误、并发竞争、资源泄漏
- **文件路径**：
  - `internal/ai/agents/logic.go`
  - `internal/ai/agents/logic_test.go`
  - `internal/ai/prompts/logic.txt`
- **完成标准**：对包含未处理 `json.Unmarshal` 错误的测试 diff，返回至少 1 条 warning 发现
- **依赖**：T6
- **预估**：4h
- **状态**：✅ 已完成

### T9: 性能审查 Agent
- **描述**：实现性能 Agent。ReAct 模式。System prompt 要求检查 N+1 查询、热路径内存分配、低效字符串拼接
- **文件路径**：
  - `internal/ai/agents/performance.go`
  - `internal/ai/agents/performance_test.go`
  - `internal/ai/prompts/performance.txt`
- **完成标准**：对包含循环中字符串拼接的测试 diff，返回至少 1 条 warning 发现
- **依赖**：T6
- **预估**：3h
- **状态**：✅ 已完成

### T10: 规范审查 Agent
- **描述**：实现规范 Agent。结合 golangci-lint 的输出（通过 RunStaticAnalysis 工具）和 LLM 推理。System prompt 要求检查命名、错误处理、注释、导入规范
- **文件路径**：
  - `internal/ai/agents/lint.go`
  - `internal/ai/agents/lint_test.go`
  - `internal/ai/prompts/lint.txt`
- **完成标准**：对包含不规范命名的测试 diff，返回至少 1 条 suggestion 发现
- **依赖**：T6
- **预估**：3h
- **状态**：✅ 已完成

### T11: 验证 Agent
- **描述**：实现验证 Agent。输入为所有审查 Agent 的原始 `AgentFinding` 列表，执行三项操作：(1) 行号验证——检查行号是否在 diff 范围内；(2) 幻觉过滤——对每条发现，调用 ReadFile 工具验证引用的代码片段是否存在；(3) 去重——基于文件路径+行号+问题类型去重。输出为验证后的 `VerifiedFinding` 列表，每条附带置信度分数
- **文件路径**：
  - `internal/ai/agents/verifier.go`
  - `internal/ai/agents/verifier_test.go`
  - `internal/ai/prompts/verifier.txt`
- **完成标准**：对包含幻觉（引用不存在的代码）和重复发现的测试输入，验证后幻觉被过滤、重复被合并
- **依赖**：T6
- **预估**：5h
- **状态**：✅ 已完成

### T12: 审查编排器
- **描述**：实现 Orchestrator，负责串联完整审查流程：(1) 接收 PR 事件 → (2) 解析 diff → (3) 过滤文件 → (4) 并行启动 4 个审查 Agent（使用 goroutine + errgroup）→ (5) 收集结果 → (6) 调用验证 Agent → (7) 返回最终发现列表
- **文件路径**：
  - `internal/orchestrator/orchestrator.go`
  - `internal/orchestrator/orchestrator_test.go`
- **完成标准**：编排器单元测试通过（mock 所有 Agent）；集成测试验证完整流程
- **依赖**：T4, T5, T7, T8, T9, T10
- **预估**：4h
- **状态**：✅ 已完成

## 阶段四：GitHub 集成与输出（T13-T14）

### T13: GitHub Review 回写
- **描述**：实现 GitHub API 客户端封装。功能包括：(1) 获取 PR diff（调用 GitHub API）；(2) 提交 PR Review（包含 summary + inline comments）；(3) 更新 Commit Status。使用 go-github 库
- **文件路径**：
  - `internal/github/client.go`
  - `internal/github/review.go`
  - `internal/github/status.go`
  - `internal/github/client_test.go`
- **完成标准**：对 mock GitHub Server 的回写测试通过；Review 格式正确（包含行级评论和 summary）
- **依赖**：T3
- **预估**：4h
- **状态**：✅ 已完成

### T14: Web 仪表盘
- **描述**：使用 Go 模板 + HTMX 实现简单的审查历史仪表盘。页面包括：(1) 列表页 `/dashboard`——展示审查记录列表；(2) 详情页 `/dashboard/reviews/{id}`——展示某次审查的所有发现。提供 JSON API：`GET /api/reviews` 和 `GET /api/reviews/{id}`
- **文件路径**：
  - `internal/handler/dashboard.go`
  - `templates/layout.html`
  - `templates/dashboard.html`
  - `templates/review_detail.html`
  - `internal/handler/dashboard_test.go`
- **完成标准**：手动验证页面可正常渲染；API 返回正确的 JSON
- **依赖**：T3, T12
- **预估**：4h
- **状态**：✅ 已完成
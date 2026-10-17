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

## 阶段五：v1.3 静态分析管道（T15-T21）

### T15: PreAnalysisFinding 类型定义与指纹生成
- **描述**：定义 `PreAnalysisFinding` 结构体和稳定指纹生成函数。指纹规则：`<tool>:<ruleID>:<filePath>:<line>`。提供 `MarshalPreFindings` / `UnmarshalPreFindings` 用于序列化
- **文件路径**：
  - `internal/analysis/types.go`
  - `internal/analysis/fingerprint.go`
  - `internal/analysis/fingerprint_test.go`
- **完成标准**：
  - `go build ./...` 通过
  - 指纹生成测试覆盖：同一输入产生相同指纹、不同行号产生不同指纹、空字段处理
  - 序列化/反序列化往返测试通过
- **依赖**：T1
- **预估**：2h
- **状态**：✅ 已完成

### T16: gitleaks 集成（Secret Scanner）
- **描述**：集成 gitleaks 作为库（`github.com/gitleaks/gitleaks/v8`），对单个文件执行扫描，输出 `[]PreAnalysisFinding`。启用默认规则集，支持通过配置扩展自定义规则
- **文件路径**：
  - `internal/analysis/gitleaks.go`
  - `internal/analysis/gitleaks_test.go`
- **完成标准**：
  - 对包含 `const apiKey = "sk-xxxxx"` 的测试文件，返回至少 1 条 critical 发现
  - 对不含密钥的正常 Go 文件，返回 0 条发现
  - 编译期断言 `var _ Analyzer = GitleaksAnalyzer{}` 通过
- **依赖**：T15
- **预估**：3h
- **状态**：✅ 已完成

### T17: golangci-lint 集成
- **描述**：通过 exec 调用 golangci-lint CLI，使用 `--out-format=json` 输出，解析为 `[]PreAnalysisFinding`。如果 golangci-lint 未安装或调用失败，返回 error 而非 panic
- **文件路径**：
  - `internal/analysis/golangci.go`
  - `internal/analysis/golangci_test.go`
  - `.golangci.yml`（项目根目录，供 golangci-lint 使用）
- **完成标准**：
  - 对包含未处理错误的测试文件，返回至少 1 条 warning 发现
  - 对未安装 golangci-lint 的环境，返回明确的 error 信息
  - JSON 解析器单元测试通过（使用固定的 fixture）
- **依赖**：T15
- **预估**：3h
- **状态**：✅ 已完成

### T18: Semgrep 集成与自定义规则
- **描述**：通过 exec 调用 Semgrep CLI，使用 `--json` 输出，解析为 `[]PreAnalysisFinding`。项目根目录提供 `rules/semgrep/` 目录存放自定义规则（至少包含 SQL 注入、命令注入两条规则）
- **文件路径**：
  - `internal/analysis/semgrep.go`
  - `internal/analysis/semgrep_test.go`
  - `rules/semgrep/sql-injection.yml`
  - `rules/semgrep/command-injection.yml`
- **完成标准**：
  - 对包含 `db.Query("SELECT * FROM users WHERE id = " + userID)` 的测试文件，返回至少 1 条 critical 发现
  - 对未安装 Semgrep 的环境，返回明确的 error 信息
  - JSON 解析器单元测试通过
- **依赖**：T15
- **预估**：4h
- **状态**：✅ 已完成

### T19: 静态分析管道编排
- **描述**：实现 `Pipeline`，对 `[]diff.FileDiff` 运行三个分析器，使用 `errgroup` 控制并发（worker 数可配置），每个工具对每个文件设置 30s 超时。结果合并、去重（基于指纹）后按文件路径分组返回
- **文件路径**：
  - `internal/analysis/pipeline.go`
  - `internal/analysis/pipeline_test.go`
  - `internal/config/config.go`（新增 static_analysis 段落）
  - `configs/config.yaml.example`（新增配置示例）
- **完成标准**：
  - 对 3 个测试文件，三个分析器并行运行（总耗时接近最慢的单个分析器，而非累加）
  - 超时测试通过（mock 一个睡眠 60s 的分析器，验证 30s 超时后返回）
  - 降级测试通过（关闭某个分析器，管道正常返回其他分析器的结果）
  - 去重测试通过（两个分析器报告同一处问题时合并为一条）
- **依赖**：T16, T17, T18, T4
- **预估**：5h
- **状态**：✅ 已完成

### T20: ReviewAgent 接口改造与 Prompt 注入
- **描述**：
  1. 修改 `ReviewAgent` 接口，`Review` 方法新增 `PreFindings []PreAnalysisFinding` 参数（或改为 `ReviewInput` 结构体参数）
  2. 修改所有四个审查 Agent 和验证 Agent，适配新签名
  3. 修改所有 Agent 的 User message 模板，新增"静态分析发现"段落
  4. 修改 System prompt，明确指示 Agent 聚焦于"验证 + 补充"
  5. 从工具注册表中移除 `run_static_analysis` 工具，删除 `internal/ai/tools/static_analysis.go`
- **文件路径**：
  - `internal/ai/agents/*.go`（五个文件）
  - `internal/ai/prompts/*.txt`（五个文件）
  - `internal/ai/tools/static_analysis.go`（删除）
  - `internal/orchestrator/orchestrator.go`
- **完成标准**：
  - `go build ./...` 通过
  - `go vet ./...` 无输出
  - 每个 Agent 的单元测试通过（使用 mock preFindings）
  - 集成测试：给定包含 SQL 注入的测试 diff + 对应的 preFindings，安全 Agent 输出的 finding 中引用了 preFindings 的 ID
- **依赖**：T19, T7, T8, T9, T10, T11
- **预估**：6h
- **状态**：✅ 已完成

### T21: v1.3 集成测试与交付
- **描述**：
  1. 编写端到端集成测试：模拟一个包含硬编码密钥、SQL 注入、未处理错误的 PR，验证完整流程
  2. 更新 `specs/code-review-agent/spec.md`，将 CHANGELOG 中 v1.3 一行改为实际完成日期
  3. 更新 `specs/code-review-agent/checklist.md`，将 v1.3 的所有 AC 标记为通过
  4. 提示用户执行 `git tag -a v1.3-complete -m "..."`
- **文件路径**：
  - `internal/integration/e2e_test.go`
  - `specs/code-review-agent/spec.md`（CHANGELOG 更新）
  - `specs/code-review-agent/checklist.md`（状态更新）
- **完成标准**：
  - 端到端测试通过
  - CHANGELOG 更新完成
  - checklist 状态更新完成
  - 输出提示："v1.3 交付完成，请执行 git tag v1.3-complete"
- **依赖**：T20
- **预估**：3h
- **状态**：✅ 已完成

## 阶段六：v1.1 局部上下文注入（T22-T28）

### T22: ContextBundle 类型定义与 ContextBuilder 接口
- **描述**：在 `internal/context/` 下定义 `StructuralChange`、`CallSite`、`FunctionDef`、`ContextBundle` 结构体，以及 `ContextBuilder` 接口。
- **文件路径**：
  - `internal/context/types.go`
  - `internal/context/builder.go`
- **完成标准**：`go build ./...` 通过；结构体字段严格匹配接口冻结声明。
- **依赖**：T1
- **预估**：1h
- **状态**：✅ 已完成

### T23: 结构性变更检测器
- **描述**：实现一个 AST 解析器，接收 `[]diff.FileDiff`，对比新旧代码，提取出导出函数的签名变更和 interface 方法变更。返回 `[]StructuralChange`。
- **文件路径**：
  - `internal/context/detector.go`
  - `internal/context/detector_test.go`
- **完成标准**：
  - 对包含导出函数参数修改的测试 diff，返回 1 条 `signature_changed`
  - 对包含私有函数逻辑修改的测试 diff，返回 0 条
  - 对包含 interface 方法增减的测试 diff，返回 1 条 `interface_changed`
- **依赖**：T22, T4
- **预估**：4h
- **状态**：✅ 已完成

### T24: 局部调用者检索器
- **描述**：使用 `golang.org/x/tools/go/packages` 加载代码包，遍历 AST，找出对目标函数的调用点（最多 10 个），并提取包含该调用的上下文代码片段（Snippet）。如果加载超时（10s）或失败，返回空。
- **文件路径**：
  - `internal/context/callers.go`
  - `internal/context/callers_test.go`
- **完成标准**：
  - 对提供测试用例代码库中的函数，准确检索出调用点及行号
  - 超过 10 个调用点时截断并返回 warning
  - `go/packages` 加载失败时降级返回空列表
- **依赖**：T22
- **预估**：5h
- **状态**：✅ 已完成

### T25: 局部被调用者摘要器
- **描述**：解析变更函数的 AST 体，提取其调用的同级或同包内函数，获取这些函数的签名和首行注释。
- **文件路径**：
  - `internal/context/callees.go`
  - `internal/context/callees_test.go`
- **完成标准**：
  - 对包含内部函数调用的测试用例，正确提取函数签名
- **依赖**：T23
- **预估**：3h
- **状态**：✅ 已完成

### T26: ContextBuilder 实现与 Orchestrator 集成
- **描述**：实现 `ContextBuilder` 接口，串联 T23-T25。在 `orchestrator.go` 中，在获取 PreFindings 后，调用 `ContextBuilder.Build`，将结果放入 `ReviewInput`。
- **文件路径**：
  - `internal/context/builder_impl.go`
  - `internal/orchestrator/orchestrator.go`
  - `internal/orchestrator/orchestrator_test.go`
- **完成标准**：
  - 编译通过
  - Orchestrator 测试中，mock 一个 ContextBundle，验证其被正确传入 Agent
- **依赖**：T23, T24, T25, T19
- **预估**：3h
- **状态**：✅ 已完成

### T27: Prompt 注入与防幻觉声明
- **描述**：
  1. 修改 `ReviewInput`，增加 `Context ContextBundle`。
  2. 修改所有审查 Agent 的 User message 模板（`internal/ai/prompts/*.txt`），如果 Context 非空，则添加 `## 补充上下文` 段落。
  3. 在 System prompt 末尾追加防幻觉声明：“补充上下文来自静态分析，可能不完整。请结合代码自身逻辑判断。”
- **文件路径**：
  - `internal/ai/agents/*.go`
  - `internal/ai/prompts/*.txt`
- **完成标准**：
  - `go build ./...` 通过
  - 渲染出的 Prompt 包含正确的上下文和防幻觉声明
- **依赖**：T26
- **预估**：3h
- **状态**：✅ 已完成

### T28: v1.1 集成测试与交付
- **描述**：
  1. 编写端到端测试：模拟修改了一个导出函数签名，验证 Callers 被正确注入给 Agent。
  2. 更新 `spec.md` 的 CHANGELOG 并标记完成。
  3. 更新 `checklist.md`，标记 AC 通过。
  4. 提示用户打 tag。
- **文件路径**：
  - `internal/integration/e2e_test.go`
  - `specs/code-review-agent/*`
- **完成标准**：测试通过，输出提示：“v1.1 交付完成，请执行 git tag v1.1-complete”
- **依赖**：T27
- **预估**：2h
- **状态**：✅ 已完成
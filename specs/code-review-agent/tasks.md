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

## 阶段七：v1.6 评估体系（T29-T33）

### T29: 数据集与报告的数据结构定义
- **描述**：在 `internal/eval/` 下定义 `GroundTruthItem`、`EvalMetrics`、`EvalReport` 结构体及 JSON 序列化标签。
- **文件路径**：
  - `internal/eval/types.go`
- **完成标准**：`go build ./...` 通过；结构体严格匹配接口冻结声明。
- **依赖**：T1
- **预估**：1h
- **状态**：✅ 已完成

### T30: 数据集读取与指标计算引擎
- **描述**：实现从 `eval/dataset/` 目录读取指定样本的逻辑。实现匹配算法：将 Agent 输出的 `VerifiedFinding` 与 `GroundTruthItem` 按（FilePath + Line +/- 2 行容差 + Category）进行匹配。计算 Precision/Recall，不匹配的发现归入 `UBVItems`。
- **文件路径**：
  - `internal/eval/evaluator.go`
  - `internal/eval/evaluator_test.go`
- **完成标准**：
  - 对模拟输入，精准计算出 TP、FN、UBV 数量
  - 计算出的 Delta 结果符合预期（如 Baseline 为 50%，当前为 60%，则 Delta 为 10%）
- **依赖**：T29
- **预估**：4h
- **状态**：✅ 已完成

### T31: UBV 报告生成器
- **描述**：实现将 `UBVItems` 渲染为 Markdown 格式的逻辑。报告需包含文件路径、行号、代码片段、Agent 给出的修改建议，并提供占位符供人工评估后复制到 `ground_truth.json`。
- **文件路径**：
  - `internal/eval/reporter.go`
  - `internal/eval/reporter_test.go`
- **完成标准**：生成的 Markdown 语法正确，信息完整。
- **依赖**：T30
- **预估**：2h
- **状态**：✅ 已完成

### T32: CLI 评估执行器与测试数据集
- **描述**：
  1. 在 `cmd/eval/main.go` 编写入口程序。
  2. 编写 `Makefile` 的 `eval` 命令。
  3. 手工在 `eval/dataset/sample-1/` 下构造一个最小真实的测试用例（包含一个已知的 SQL 注入、一个未处理错误，以及对应的 `ground_truth.json`）。
- **文件路径**：
  - `cmd/eval/main.go`
  - `Makefile`
  - `eval/dataset/sample-1/*`
- **完成标准**：
  - 执行 `make eval` 能读取样本，真实调用 Orchestrator 运行审查，并生成 `eval/report.md` 和 `eval/metrics.json`。
- **依赖**：T31, T26 (Orchestrator)
- **预估**：4h
- **状态**：✅ 已完成

### T33: v1.6 集成测试与交付
- **描述**：
  1. 运行 `make eval`，确认端到端跑通。
  2. 更新 `spec.md` 的 CHANGELOG 并标记完成。
  3. 更新 `checklist.md`，标记 AC 通过。
  4. 提示用户打 tag。
- **文件路径**：
  - `specs/code-review-agent/*`
- **完成标准**：测试通过，输出提示：“v1.6 交付完成，请执行 git tag v1.6-complete”。
- **依赖**：T32
- **预估**：1h
- **状态**：✅ 已完成

## 阶段八：v1.2 自适应审查策略（T34-T38）

### T34: 复杂度评估器实现
- **描述**：实现 `ComplexityAssessor` 接口。基于 `diff.FileDiff` 统计变更行数和文件数。读取配置中的 `sensitive_paths` 进行路径前缀匹配。
- **文件路径**：
  - `internal/review/assessor.go`
  - `internal/review/assessor_test.go`
  - `internal/config/config.go`（新增 adaptive 配置段）
- **完成标准**：
  - 10 行变更单文件，不涉及敏感路径 -> 返回 `Trivial`
  - 250 行变更 -> 返回 `Complex`
  - 修改 `auth/login.go` 第 1 行 -> 返回 `Complex`
  - 单元测试使用表驱动测试（table-driven tests）
- **依赖**：T1, T4
- **预估**：3h
- **状态**：✅ 已完成

### T35: 策略选择器实现
- **描述**：实现 `StrategySelector`。根据 `ComplexityLevel` 返回预定义的 `ReviewStrategy`（包含启用的 Agent 名称列表和是否启用 Verifier）。
- **文件路径**：
  - `internal/review/strategy.go`
  - `internal/review/strategy_test.go`
- **完成标准**：
  - Trivial 返回 `["security", "lint"]` 且 `EnableVerifier=false`
  - Standard/Complex 返回 4 个 Agent 且 `EnableVerifier=true`
- **依赖**：T34
- **预估**：2h
- **状态**：⬜ 未开始

### T36: Orchestrator 策略集成
- **描述**：修改 `orchestrator.go`。在 Diff 解析后，调用 Assessor 和 Selector。根据 `Strategy.EnabledAgents` 动态启动 goroutine。如果 `EnableVerifier=false`，直接跳过验证阶段，将原始 finding 标记为验证通过。
- **文件路径**：
  - `internal/orchestrator/orchestrator.go`
  - `internal/orchestrator/orchestrator_test.go`
  - `internal/repository/review_repo.go`（更新 schema 记录 skipped agent）
- **完成标准**：
  - Mock Assessor 返回 Trivial 时，只调用 2 个 Agent，不调用 Verifier
  - Mock Assessor 返回 Standard 时，调用 4 个 Agent 和 Verifier
- **依赖**：T35
- **预估**：4h
- **状态**：✅ 已完成

### T37: v1.2 集成测试与交付
- **描述**：
  1. 编写端到端测试：分别在 `eval/dataset/` 下构造一个 Trivial PR 和一个 Complex PR，验证策略开关生效。
  2. 更新 `spec.md` 的 CHANGELOG。
  3. 更新 `checklist.md`，标记 AC 通过。
  4. 提示用户打 tag。
- **文件路径**：
  - `eval/dataset/sample-trivial/*`
  - `eval/dataset/sample-complex/*`
  - `specs/code-review-agent/*`
- **完成标准**：测试通过，输出提示：“v1.2 交付完成，请执行 git tag v1.2-complete”
- **依赖**：T36
- **预估**：2h
- **状态**：✅ 已完成

## 阶段九：v1.4 隐式反馈追踪（T39-T43）

### T39: 数据库 Schema 迁移
- **描述**：编写 SQL 迁移文件。为 `findings` 表添加 `feedback_status` (VARCHAR, 默认 'pending')。新建 `pr_commits` 表（id, repo, pr_number, sha, created_at）。更新 Repository 层的接口和实现。
- **文件路径**：
  - `internal/db/migrations/002_add_feedback.sql`
  - `internal/repository/feedback_repo.go`
  - `internal/repository/feedback_repo_test.go`
- **完成标准**：
  - 迁移命令成功执行。
  - Repository 能正确插入记录和按状态查询。
- **依赖**：T3
- **预估**：2h
- **状态**：✅ 已完成

### T40: Webhook 事件路由扩展
- **描述**：修改 Webhook handler。识别 `synchronize` 和 `closed` 动作，提取 `before` (旧 SHA) 和 `after` (新 SHA)，并将事件异步分发给 `FeedbackTracker` 处理。
- **文件路径**：
  - `internal/handler/webhook.go`
  - `internal/handler/webhook_test.go`
- **完成标准**：
  - 发送带有 `synchronize` 动作的 mock webhook，触发 `FeedbackTracker.TrackOnSynchronize`。
  - 发送带有 `closed` 且 `merged=true` 的 mock webhook，触发 `TrackOnClosed`。
- **依赖**：T39
- **预估**：2h
- **状态**：✅ 已完成

### T41: 隐式反馈比对引擎
- **描述**：实现 `FeedbackTracker` 接口。
  1. 根据旧 SHA 和新 SHA，通过 GitHub API 获取两个版本的局部代码片段。
  2. 实现相似度比对算法（计算 Levenshtein 距离或简单的字符串替换比对）。
  3. 根据比对结果更新 `findings` 表的状态。
- **文件路径**：
  - `internal/feedback/tracker.go`
  - `internal/feedback/tracker_test.go`
  - `internal/feedback/diff_compare.go` (纯函数比对)
- **完成标准**：
  - 相似度比对测试：修改了一行代码，相似度 < 80%；仅调整缩进，相似度 > 80%。
  - 状态更新测试：正确将 `pending` 迁移到 `accepted_implicit`。
- **依赖**：T39, T40, T13
- **预估**：5h
- **状态**：✅ 已完成

### T42: 统计 API 与仪表盘更新
- **描述**：
  1. 新增 API `GET /api/metrics/feedback`，按 Agent 分类返回采纳率（Accept Rate = accepted / total_resolved）。
  2. 修改仪表盘页面，使用简单的 HTML/CSS 展示进度条或统计卡片。
- **文件路径**：
  - `internal/handler/dashboard.go`
  - `templates/dashboard.html`
- **完成标准**：
  - 访问 API 返回正确的 JSON 结构。
  - 仪表盘页面能正确渲染各项 Agent 的采纳率数据。
- **依赖**：T41
- **预估**：3h
- **状态**：✅ 已完成

### T43: v1.4 集成测试与交付
- **描述**：
  1. 端到端测试：模拟完整生命周期（审查产生 finding -> synchronize 修改代码 -> closed merge），验证数据库中状态从 `pending` 变为 `accepted_implicit`。
  2. 更新 `spec.md` 的 CHANGELOG 并标记完成。
  3. 更新 `checklist.md`，标记 AC 通过。
  4. 提示用户打 tag。
- **文件路径**：
  - `internal/integration/e2e_feedback_test.go`
  - `specs/code-review-agent/*`
- **完成标准**：测试通过，输出提示：“v1.4 交付完成，请执行 git tag v1.4-complete”
- **依赖**：T42
- **预估**：3h
- **状态**：✅ 已完成

## 阶段十：v1.7 前端可视化重构（T44-T48）

### T44: 定义 SSE 事件结构并改造 Orchestrator
- **描述**：
  1. 在 `internal/orchestrator` 中定义 `ReviewEvent` 结构体。
  2. 修改 Orchestrator，使其接收一个 `chan<- ReviewEvent` 或 `EventEmitter` 接口，在关键节点（静态分析开始、Agent 启动、工具调用、发现生成）发送事件。
- **文件路径**：
  - `internal/orchestrator/events.go`
  - `internal/orchestrator/orchestrator.go`
- **完成标准**：单元测试中传入一个 Mock Channel，验证能接收到正确的 JSON 事件。
- **依赖**：T36
- **预估**：3h
- **状态**：✅ 已完成

### T45: 实现 SSE 路由接口
- **描述**：使用 chi 路由和 Go 标准库 `http.Flusher` 实现 `GET /api/stream-review`。读取 Orchestrator 推送的事件流，并写入 SSE 响应（格式：`data: {json}\n\n`）。
- **文件路径**：
  - `internal/handler/sse.go`
  - `internal/handler/sse_test.go`
- **完成标准**：使用 `curl -N` 访问能持续收到事件数据；不触发 HTTP 超时。
- **依赖**：T44
- **预估**：2h
- **状态**：✅ 已完成

### T46: 前端页面骨架与 Vue 3 状态管理
- **描述**：创建 `static/index.html`。引入 Tailwind、Vue 3、marked.js、highlight.js。实现左右分栏布局。定义 Vue 的响应式状态变量：`events` (接收的历史事件)、`isLoading`、`currentAgent`。
- **文件路径**：
  - `static/index.html`
  - `static/app.js`
  - `internal/handler/static.go` (挂载静态目录)
- **完成标准**：访问根路由 `/` 能渲染出现代风格的空白页面。
- **依赖**：T45
- **预估**：3h
- **状态**：✅ 已完成

### T47: 前端 SSE 逻辑解析与 UI 渲染
- **描述**：
  1. 使用原生 `EventSource` 连接 `/api/stream-review`。
  2. 根据收到的 `event` 类型，追加到时间轴列表中。
  3. 工具调用展示 "🔧 正在读取文件..."，发现展示高亮的错误卡片。
  4. 全部完成后，将最终的 Markdown 文本渲染到右侧。
- **文件路径**：
  - `static/app.js`
- **完成标准**：手动触发请求，页面能平滑地展示打字机效果和完整的思考过程。
- **依赖**：T46
- **预估**：4h
- **状态**：✅ 已完成

### T48: v1.7 集成验证与交付
- **描述**：更新 CHANGELOG、checklist。启动 Docker 验证整体效果，提示打 tag。
- **依赖**：T47
- **预估**：1h
- **状态**：✅ 已完成

## 阶段十一：v1.8 Harness 控制层（T49-T53）

### T49: AgentResult 与 Harness 接口定义
- **描述**：在 `internal/harness/` 下定义 `AgentResult`、`AgentStatus` 和 `Harness` 接口。
- **文件路径**：`internal/harness/types.go`
- **完成标准**：`go build ./...` 通过。
- **依赖**：T1
- **预估**：1h
- **状态**：✅ 已完成

### T50: 工具指纹与震荡检测器实现
- **描述**：实现一个循环检测器。接收 ToolLog 列表，如果发现连续 3 个相同的指纹，或 A→B→A→B 模式，返回 `ErrOscillation`。
- **文件路径**：`internal/harness/oscillation.go`, `oscillation_test.go`
- **完成标准**：单元测试覆盖连续重复和交替重复场景。
- **依赖**：T49
- **预估**：2h
- **状态**：✅ 已完成

### T51: Harness 执行器实现
- **描述**：实现 `Harness` 接口。在循环中调用 `agent.Review`。如果发生震荡或超过时间限制（Context 超时），捕获错误并返回 `StatusFailed`。最后执行“完成门禁”校验（检查 ToolCallCount）。
- **文件路径**：`internal/harness/harness_impl.go`, `harness_test.go`
- **完成标准**：测试当 ToolCallCount==0 时，状态被强制降级为 Partial。
- **依赖**：T50
- **预估**：4h
- **状态**：✅ 已完成

### T52: Orchestrator 集成与 API 改造
- **描述**：在 `orchestrator.go` 中，用 Harness 替换直接调用 Agent。收集所有 Agent 的 `AgentResult`，只有 `completed` 和 `partial` 的结果进入下游验证。`failed` 的结果记录到数据库并在最终 Markdown 中报警。
- **文件路径**：`internal/orchestrator/orchestrator.go`, `internal/repository/review_repo.go`
- **完成标准**：端到端测试，模拟一个 Agent 返回 failed，验证流程不崩溃且 DB 有记录。
- **依赖**：T51
- **预估**：3h
- **状态**：✅ 已完成

### T53: v1.8 集成测试与交付
- **描述**：更新 CHANGELOG、checklist。提示打 tag。
- **依赖**：T52
- **预估**：1h
- **状态**：✅ 已完成

## 阶段十二：v1.9 上下文装配与记忆压缩（T54-T59）

### T54: 上下文数据结构与双轨监控器
- **描述**：定义 `ContextBlock` 和 `CompressionConfig`。实现 `ShouldCompress` 逻辑，同时校验 `trigger_ratio` 和 `absolute_token_limit`。
- **文件路径**：
  - `internal/context/assembler.go`
  - `internal/context/monitor.go`
  - `internal/context/monitor_test.go`
- **完成标准**：
  - 模拟 8K 窗口，Token 达到 5.3K (65%) 时返回 true。
  - 模拟 128K 窗口，Token 达到 33K (超 32K) 时返回 true。
- **依赖**：T1
- **预估**：2h
- **状态**：✅ 已完成

### T55: 工具层前置截断与熔断
- **描述**：修改 `search_code` 和 `read_file` 工具的执行逻辑。在返回结果前评估字符数。若超限，执行截断并附加警告提示，防止单次返回击穿上下文。
- **文件路径**：
  - `internal/ai/tools/read_file.go`
  - `internal/ai/tools/search_code.go`
  - `internal/ai/tools/tools_test.go`
- **完成标准**：
  - 模拟读取一个 5000 行文件，验证返回结果被截断至限制长度，且包含截断警告。
- **依赖**：T54
- **预估**：2h
- **状态**：✅ 已完成

### T56: 滑动窗口压缩器实现
- **描述**：实现 `Compressor`。接收 `[]ContextBlock`，跳过 `IsPinned=true` 的块。按 `Sequence` 排序，保留最新的 N 个 `TOOL_OUTPUT` 块。对更早的块执行代码首尾截断或替换为简短摘要。
- **文件路径**：
  - `internal/context/compressor.go`
  - `internal/context/compressor_test.go`
- **完成标准**：
  - 提供 5 个 `TOOL_OUTPUT` 块，`KeepRecentRounds=2`。验证前 3 个被压缩，后 2 个完整保留。
- **依赖**：T54
- **预估**：3h
- **状态**：✅ 已完成

### T57: ContextAssembler 集成到 Harness
- **描述**：在 Harness 中，每次调用 LLM 前：1) 收集 Blocks；2) 检查 `ShouldCompress`；3) 执行压缩；4) 调用 `Assemble` 排序并拼接字符串。
- **文件路径**：
  - `internal/harness/harness_impl.go`
  - `internal/harness/harness_test.go`
- **完成标准**：
  - 验证 Prompt 中 Pinned 块（Diff）完整保留，且最新的两轮工具输出未被破坏。
- **依赖**：T56, T51
- **预估**：4h
- **状态**：✅ 已完成

### T58: v1.9 集成测试与交付
- **描述**：更新 `config.yaml`，追加 `compression` 配置。更新 CHANGELOG 与 checklist，提示打 tag。
- **预估**：1h
- **状态**：✅ 已完成

## 阶段十三：v1.8.1 Checkpoint 断点续传补丁（T59-T61）

### T59: Checkpoint 数据库层实现
- **描述**：编写 SQL 迁移文件，创建 `harness_checkpoints` 表及唯一索引。实现 `CheckpointRepository` 接口，包含 Save（Upsert 逻辑）和 GetBySHAAndAgent。
- **文件路径**：
  - `internal/db/migrations/006_add_checkpoints.sql`（003 已被 v1.2 占用）
  - `internal/repository/checkpoint_repo.go`
  - `internal/repository/checkpoint_repo_test.go`
- **完成标准**：Upsert 测试通过，唯一约束生效。
- **依赖**：T3
- **预估**：2h
- **状态**：✅ 已完成

### T60: Orchestrator 与 Harness 集成
- **描述**：
  1. 在 Orchestrator 启动并行审查前，遍历所需的 Agent 列表，从 Checkpoint 中读取结果。
  2. 对于未命中 Checkpoint 或需强制重跑的 Agent，分发给 Harness。
  3. 在 Harness 的 `Execute` 方法末尾，增加将 `AgentResult` 异步写入 Checkpoint 的逻辑。
- **文件路径**：
  - `internal/orchestrator/orchestrator.go`
  - `internal/harness/harness_impl.go`
  - `internal/config/config.go`
- **完成标准**：模拟一个已有部分 Checkpoint 的 SHA，验证 Orchestrator 仅发起了缺失 Agent 的调用。
- **依赖**：T59, T51
- **预估**：3h
- **状态**：✅ 已完成

### T61: 联调测试与交付
- **描述**：更新配置 `config.force_rerun_agents: []`。更新 CHANGELOG 并提示打 tag。
- **依赖**：T60
- **预估**：1h
- **状态**：✅ 已完成

## 阶段十四：v1.10 PR 增量审查与跨回合状态（T62-T67）

### T62: Webhook 请求体完整读取修复（FR-65）
- **描述**：将 `handler/webhook.go` 中的单次 `r.Body.Read` 改为 `io.ReadAll`，HMAC 校验与 JSON 解析基于同一份 body。读取失败返回 400。
- **文件路径**：
  - `internal/handler/webhook.go`
  - `internal/handler/webhook_test.go`
- **完成标准**：新增测试覆盖超过单次 Read 缓冲区的大 payload，签名校验通过且解析成功（AC-124）。
- **依赖**：无
- **预估**：1h
- **状态**：✅ 已完成

### T63: 迁移 007 与审查锚点仓储（FR-62, FR-64）
- **描述**：编写 `007_add_review_anchor.sql`：`reviews` 新增 `reviewed_sha`、`base_sha`、`scope`、`scope_reason`；`harness_checkpoints` 新增 `prompt_hash`、`config_hash`，唯一约束改为四元组，status CHECK 扩展为 `completed/partial/failed` 并将既有 `success` 映射为 `completed`。`ReviewRepo` 新增 `LatestCompletedReview(ctx, repo, pr)`。
- **文件路径**：
  - `internal/db/migrations/007_add_review_anchor.sql`
  - `internal/repository/review_repo.go`
  - `internal/repository/review_repo_test.go`
- **完成标准**：迁移可在已有 006 数据的库上正向执行；`LatestCompletedReview` 忽略 `failed` 与 `pending` 记录（AC-119）。
- **依赖**：T3, T59
- **预估**：3h
- **状态**：✅ 已完成

### T64: 增量 diff 基准选择与 Compare 获取（FR-61）
- **描述**：`EventInput` 增加 `BeforeSHA` 并由 webhook 填充。实现基准选择（锚点 `reviewed_sha` → `BeforeSHA` → 全量），调用 GitHub Compare API 获取增量 diff。不可达或超限时回退全量并写入 `scope_reason`。Prompt 增加增量范围声明。
- **文件路径**：
  - `internal/orchestrator/pipeline.go`
  - `internal/github/client.go`
  - `internal/handler/webhook.go`
  - `internal/orchestrator/pipeline_test.go`
- **完成标准**：`before` 可达时 scope 为 incremental（AC-117）；force-push 导致不可达时回退全量并记录 `before_unreachable`（AC-118）。
- **依赖**：T62, T63
- **预估**：4h
- **状态**：✅ 已完成

### T65: 跨回合上下文注入与 fixed 判定（FR-63）
- **描述**：先提交变更提案 `specs/changes/v1.10-prior-findings/proposal.md` 并获审批，再为 `ReviewInput` 增加 `PriorFindings`。在 Prompt 的 TASK_STATE 段注入 `PRIOR_FINDINGS`，要求 Agent 输出 `still_present/fixed/unverifiable` 判定，并在解析层强制：`fixed` 必须附带读取证据，否则降级为 `unverifiable`。
- **文件路径**：
  - `specs/changes/v1.10-prior-findings/proposal.md`
  - `internal/ai/agents/agent.go`
  - `internal/ai/agents/verifier.go`
  - `internal/ai/prompts/*.txt`
  - `internal/orchestrator/orchestrator.go`
- **完成标准**：首次审查不注入 PRIOR 段（AC-120）；已修复代码判定为 fixed 且不输出为新 finding（AC-121）；文件不可读时判定为 unverifiable（AC-122）。
- **依赖**：T63, T64
- **预估**：4h
- **状态**：✅ 已完成

### T66: Checkpoint 键与缓存策略修正（FR-64）
- **描述**：计算 `prompt_hash`（Agent Prompt 文件内容）与 `config_hash`（影响行为的配置子集），写入 Checkpoint 并纳入查询条件。`partial` 与 `failed` 结果不再复用。
- **文件路径**：
  - `internal/repository/checkpoint_repo.go`
  - `internal/orchestrator/orchestrator.go`
  - `internal/orchestrator/orchestrator_test.go`
- **完成标准**：同一 SHA 下 `partial` 结果触发重新执行（AC-123）；修改 Prompt 后 `prompt_hash` 变化导致缓存未命中。
- **依赖**：T63
- **预估**：2h
- **状态**：✅ 已完成

### T67: v1.10 集成测试与交付
- **描述**：端到端测试：首次审查 → 追加一次 synchronize（增量）→ 验证锚点、PRIOR 注入与 scope 记录。更新 CHANGELOG 与 checklist，提示用户打 tag。
- **文件路径**：
  - `internal/integration/e2e_incremental_test.go`
- **完成标准**：AC-117 ~ AC-124 全部通过。
- **依赖**：T62, T63, T64, T65, T66
- **预估**：2h
- **状态**：✅ 已完成

## 阶段十五：v1.11 评论回写去重与线程闭环（T68-T73）

### T68: finding 指纹与历史回填（FR-66）
- **描述**：实现纯函数 `FindingFingerprint`（归一化 path、agent、message，不含行号）。编写 `008_add_finding_fingerprint.sql` 新增 `findings.fingerprint` 列与索引。提供一次性回填任务计算历史记录指纹。
- **文件路径**：
  - `internal/review/finding_fingerprint.go`
  - `internal/review/finding_fingerprint_test.go`
  - `internal/db/migrations/008_add_finding_fingerprint.sql`
  - `internal/repository/finding_repo.go`
- **完成标准**：行号不同的同一 finding 指纹相同（AC-125）；回填后历史记录指纹非空。
- **依赖**：T63
- **预估**：2h
- **状态**：✅ 已完成

### T69: GitHub 审查线程读取客户端（FR-67）
- **描述**：实现读取 PR 审查线程的客户端（REST 获取评论 + GraphQL `reviewThreads` 获取 isResolved / isOutdated）。解析评论正文中的 `<!-- cra-fp:... -->` 标记。发布评论时嵌入该标记。
- **文件路径**：
  - `internal/github/threads.go`
  - `internal/github/threads_test.go`
  - `internal/github/review.go`
- **完成标准**：可从 mock 响应中解析出指纹、路径、行号与 resolved 状态；读取失败返回带上下文的 error。
- **依赖**：无
- **预估**：4h
- **状态**：✅ 已完成

### T70: 发布计划纯函数（FR-67）
- **描述**：实现 `PlanPublish(findings, threads, verdicts)`，按指纹与出现序号分桶，输出 New / Persisting / Resolved 与 Event。纯函数，无 I/O。
- **文件路径**：
  - `internal/review/publish_plan.go`
  - `internal/review/publish_plan_test.go`
- **完成标准**：表驱动测试覆盖 new / persisting / resolved、同指纹多实例、行号漂移（AC-125、AC-126、AC-127）。
- **依赖**：T68, T69
- **预估**：3h
- **状态**：✅ 已完成

### T71: 回写流程改造与 REQUEST_CHANGES 判定（FR-67, FR-69）
- **描述**：`Pipeline` 回写前读取线程并调用 `PlanPublish`，仅提交 New 的内联评论。Event 判定改为基于未解决的 critical。线程读取失败时降级为全量 New 并在 summary 警告。
- **文件路径**：
  - `internal/orchestrator/pipeline.go`
  - `internal/github/review.go`
  - `internal/orchestrator/pipeline_test.go`
- **完成标准**：已解决的 critical 不再触发 REQUEST_CHANGES（AC-130）；读取失败时降级且流程不中断（AC-131）。
- **依赖**：T69, T70
- **预估**：4h
- **状态**：✅ 已完成

### T72: 线程 resolve 与隐式反馈解耦（FR-68, FR-70）
- **描述**：对 `fixed` 判定且线程 open 的项，调用 GraphQL `resolveReviewThread`；失败仅记日志。`accepted_implicit` / `ignored_implicit` 不触发任何线程状态变更，仅保留在统计中。
- **文件路径**：
  - `internal/github/threads.go`
  - `internal/feedback/tracker.go`
  - `internal/orchestrator/pipeline.go`
- **完成标准**：fixed 线程被 resolve（AC-128）；人工已 resolve 的线程不重复评论也不改变状态（AC-129）；accepted_implicit 但判定为 still_present 时线程不被 resolve（AC-132）。
- **依赖**：T65, T71
- **预估**：3h
- **状态**：✅ 已完成

### T73: v1.11 集成测试与交付
- **描述**：端到端测试：一轮审查 → 修复一个问题 → synchronize → 验证无重复评论、已修复线程被 resolve、事件类型正确。更新 CHANGELOG 与 checklist，提示用户打 tag。
- **文件路径**：
  - `internal/integration/e2e_publish_test.go`
- **完成标准**：AC-125 ~ AC-132 全部通过。
- **依赖**：T72
- **预估**：2h
- **状态**：✅ 已完成

## 阶段十六：v1.12 Harness 收口与验证门禁（T74-T78）

### T74: 上下文装配收口（FR-71）
- **描述**：删除 `compressingAgent` 中独立的装配逻辑，统一调用 `Executor.preparePrompt`；导出的 `PreparePrompt` 仅保留给测试或删除。
- **文件路径**：
  - `internal/ai/agents/compression.go`
  - `internal/ai/agents/agent.go`
  - `internal/harness/harness_impl.go`
- **完成标准**：全仓库只有一条 Prompt 装配路径（AC-133）。
- **依赖**：无
- **预估**：2h
- **状态**：⬜ 未开始

### T75: 生产 Verifier 启用 LLM（FR-72）
- **描述**：`cmd/server/main.go` 与 `cmd/eval/main.go` 传入真实 LLM 客户端（复用 `llm.go` 的模型）。配置要求启用验证但 LLM 为 nil 时，启动直接返回错误。
- **文件路径**：
  - `cmd/server/main.go`
  - `cmd/eval/main.go`
  - `internal/config/config.go`
- **完成标准**：生产构造的 Verifier 非 nil；Standard/Complex 的 finding 经过对抗验证（AC-134）。
- **依赖**：T74
- **预估**：2h
- **状态**：⬜ 未开始

### T76: Trivial 路径验证开关（FR-72）
- **描述**：新增配置 `static_analysis.adaptive.trivial_verifier`（默认 false）。开启时 Trivial 策略启用 Verifier，关闭时保持现状。
- **文件路径**：
  - `internal/review/strategy.go`
  - `internal/review/strategy_test.go`
  - `internal/config/config.go`
  - `configs/config.yaml.example`
- **完成标准**：开关开启时 Trivial 发现经过 Verifier（AC-135）；关闭时不调用 Verifier（AC-136）。
- **依赖**：T75
- **预估**：2h
- **状态**：⬜ 未开始

### T77: 完成门禁引用证据校验（FR-73）
- **描述**：Harness 记录每次 `read_file` 成功读取的路径集合。`completed` 结果中的 finding 若引用了未读取过的文件，降级为 `partial`，`ErrorReason = unverified_citation`，并保留 findings 与标记。
- **文件路径**：
  - `internal/harness/harness_impl.go`
  - `internal/harness/types.go`
  - `internal/harness/harness_test.go`
- **完成标准**：未读取文件被引用时降级（AC-137）；全部引用已读取时状态不变（AC-138）。
- **依赖**：T74
- **预估**：3h
- **状态**：⬜ 未开始

### T78: v1.12 集成测试与交付
- **描述**：端到端测试 Standard PR，验证 Verifier 实际调用；更新 CHANGELOG 与 checklist，提示用户打 tag。
- **文件路径**：
  - `internal/integration/e2e_verifier_test.go`
- **完成标准**：AC-133 ~ AC-138 全部通过。
- **依赖**：T75, T76, T77
- **预估**：2h
- **状态**：⬜ 未开始

## 阶段十七：v1.13 不可信输入隔离（T79-T82）

### T79: UNTRUSTED 块包裹（FR-74）
- **描述**：实现 `WrapUntrusted(source, content, nonce)`，为 PR 派生内容加随机 nonce 边界标记，并在 System Prompt 中声明边界内内容不得作为指令执行。nonce 每次审查随机生成；内容若包含 nonce 则拒绝该段并告警。
- **文件路径**：
  - `internal/ai/prompts/*.txt`
  - `internal/ai/agents/agent.go`
  - `internal/ai/agents/untrusted.go`
  - `internal/ai/agents/untrusted_test.go`
- **完成标准**：伪造闭合标记的 diff 不会逃逸边界（AC-139）。
- **依赖**：T74, T65（v1.10 PRIOR_FINDINGS 已存在）
- **预估**：3h
- **状态**：⬜ 未开始

### T80: Agent 输出校验（FR-75）
- **描述**：在 Verifier 之前校验每条 finding：`file_path` 必须属于 diff 文件集合，`line` 必须落在对应 hunk 的新文件行号区间内，`severity` 必须在枚举内，剥离非白名单字段。丢弃数量写入 `AgentResult`（变更提案见 spec 24.3）。
- **文件路径**：
  - `internal/ai/agents/validate.go`
  - `internal/ai/agents/validate_test.go`
  - `internal/harness/types.go`
  - `specs/changes/v1.13-output-validation/proposal.md`
- **完成标准**：路径不在 diff 中的 finding 被丢弃并计数（AC-140）；工具调用指令字段被剥离并告警（AC-141）。
- **依赖**：T79, 变更提案审批
- **预估**：3h
- **状态**：⬜ 未开始

### T81: 注入回归样本集（FR-76）
- **描述**：在 `eval/dataset/` 新增至少 3 个注入样本（注释中的"忽略以上指令"、commit message 中的伪造审查结论、字符串字面量中的 JSON 指令）。评估报告增加 `injection_success_count`。
- **文件路径**：
  - `eval/dataset/injection-*/`
  - `internal/eval/evaluator.go`
- **完成标准**：注入样本以注入为依据输出 LGTM 的数量为 0（AC-142）。
- **依赖**：T79, T80
- **预估**：2h
- **状态**：⬜ 未开始

### T82: v1.13 集成测试与交付
- **描述**：更新 CHANGELOG 与 checklist，提示用户打 tag。
- **依赖**：T81
- **预估**：1h
- **状态**：⬜ 未开始

## 阶段十八：v1.14 工具面扩展与循环控制增强（T83-T87）

### T83: Agent 工具白名单（FR-77）
- **描述**：Agent 定义增加 `AllowedTools []string`。Harness 在 `governedTool.Call` 第一阶段校验白名单，越权调用记为 `ToolLog{Success:false}`，返回错误给模型，不执行工具。
- **文件路径**：
  - `internal/harness/harness_impl.go`
  - `internal/ai/agents/agent.go`
  - `internal/harness/harness_test.go`
- **完成标准**：越权调用被拒绝且未执行（AC-143）。
- **依赖**：T77（工具读取记录已存在）
- **预估**：2h
- **状态**：⬜ 未开始

### T84: 生产注册只读检索工具（FR-77）
- **描述**：`buildTools` 注册 `search_code` 与 `get_function_definition`。分配：security/logic 可用 read_file、search_code、get_function_definition；performance 可用 read_file、search_code；lint 仅 read_file。
- **文件路径**：
  - `internal/orchestrator/orchestrator.go`
  - `internal/orchestrator/orchestrator_test.go`
- **完成标准**：生产工具列表包含两个新工具（AC-144）。
- **依赖**：T83, T81（工具输出已由 T79 包裹）
- **预估**：2h
- **状态**：⬜ 未开始

### T85: 只读 git 历史工具（FR-78）
- **描述**：实现 `git_blame`、`git_log_file`，使用 go-git 库读取工作区历史（不 exec git）。路径必须位于工作区内且不以 `-` 开头；行号范围上限 500；`git_log_file` 的 limit 上限 20。
- **文件路径**：
  - `internal/ai/tools/git_history.go`
  - `internal/ai/tools/git_history_test.go`
- **完成标准**：参数注入被拒绝（AC-148）；未跟踪文件返回明确错误（AC-147）。
- **依赖**：T84
- **预估**：4h
- **状态**：⬜ 未开始

### T86: 震荡参数归一化与工具预算（FR-79）
- **描述**：`ArgsHash` 前对路径类参数执行 `path.Clean` 并去除 `./` 前缀，归一化仅用于指纹，不改变实际调用参数。新增 `max_tool_calls_per_agent`（默认 20），超出则中断为 `partial`，`ErrorReason = tool_budget_exhausted`。
- **文件路径**：
  - `internal/harness/oscillation.go`
  - `internal/harness/oscillation_test.go`
  - `internal/harness/harness_impl.go`
- **完成标准**：`./a.go` 与 `a.go` 视为同一指纹（AC-145）；超预算中断（AC-146）。
- **依赖**：T85
- **预估**：3h
- **状态**：⬜ 未开始

### T87: v1.14 集成测试与交付
- **描述**：更新 CHANGELOG 与 checklist，提示用户打 tag。
- **依赖**：T83, T84, T85, T86
- **预估**：1h
- **状态**：⬜ 未开始

## 阶段十九：v1.15 精确 Token 计量、项目规则与 PR 预算（T88-T91）

### T88: Tokenizer 接口与精确计数（FR-80）
- **描述**：定义 `Tokenizer` 接口，含 `Exact() bool`。OpenAI 使用 tiktoken 系列实现（新增依赖需评审）；Anthropic 使用 count_tokens 接口，失败时回退估算并标记。未配置时回退字符估算。
- **文件路径**：
  - `internal/context/tokenizer.go`
  - `internal/context/tokenizer_test.go`
  - `internal/context/monitor.go`
- **完成标准**：OpenAI 配置下使用精确计数；未配置或失败时回退并记录日志（AC-149）。
- **依赖**：T74
- **预估**：4h
- **状态**：⬜ 未开始

### T89: 仓库规则文件（FR-81）
- **描述**：通过 GitHub Contents API，以 `base.ref` 读取 `.codereviewagent/rules.md`，不存在则跳过。内容限长 4000 字符，超出截断告警；经 T79 包裹后注入 TASK_STATE。
- **文件路径**：
  - `internal/github/client.go`
  - `internal/orchestrator/orchestrator.go`
  - `internal/orchestrator/orchestrator_test.go`
- **完成标准**：PR 修改规则文件时使用基准版本（AC-150）；文件缺失时无规则段且不报错（AC-151）；超长截断（AC-153）。
- **依赖**：T79, T74
- **预估**：3h
- **状态**：⬜ 未开始

### T90: PR 级预算（FR-82）
- **描述**：配置 `budget.max_tokens`、`budget.max_wall_seconds`、`budget.max_tool_calls`，默认 0 表示不限制。Orchestrator 统一计量；Agent 启动前检查剩余预算，耗尽则该 Agent 不启动，记录 `budget_exhausted`，状态 `partial`。墙钟从 `Process` 开始计时。
- **文件路径**：
  - `internal/orchestrator/orchestrator.go`
  - `internal/harness/harness_impl.go`
  - `internal/config/config.go`
  - `internal/orchestrator/orchestrator_test.go`
- **完成标准**：墙钟超限后剩余 Agent 不再启动（AC-152）；默认配置下行为与 v1.14 一致（AC-154）。
- **依赖**：T88, T86
- **预估**：4h
- **状态**：⬜ 未开始

### T91: v1.15 集成测试与交付
- **描述**：更新 `config.yaml.example`，CHANGELOG 与 checklist，提示用户打 tag。
- **依赖**：T88, T89, T90
- **预估**：1h
- **状态**：⬜ 未开始

## 阶段二十：v1.16 持久化任务队列与执行轨迹（T92-T95）

### T92: review_jobs 迁移与仓储（FR-83）
- **描述**：编写 `009_add_review_jobs.sql`：`review_jobs(id, repo_full_name, pr_number, head_sha, before_sha, action, status, attempts, last_error, created_at, updated_at)`，状态 `queued/running/done/dead`，唯一约束 `(repo_full_name, pr_number, head_sha, action)`。
- **文件路径**：
  - `internal/db/migrations/009_add_review_jobs.sql`
  - `internal/repository/job_repo.go`
  - `internal/repository/job_repo_test.go`
- **完成标准**：重复入队只产生一条记录（AC-155）。
- **依赖**：无（可与 v1.12–v1.15 并行）
- **预估**：3h
- **状态**：⬜ 未开始

### T93: 队列 worker 替换 goroutine（FR-83）
- **描述**：Webhook 写入 `review_jobs` 后返回 202。Worker 以 `FOR UPDATE SKIP LOCKED` 领取 `queued` 任务。启动时将遗留 `running` 置回 `queued`。失败重试 3 次后置为 `dead`。
- **文件路径**：
  - `internal/handler/webhook.go`
  - `internal/orchestrator/worker.go`
  - `internal/orchestrator/worker_test.go`
  - `cmd/server/main.go`
- **完成标准**：running 任务在进程 kill 后恢复并执行一次（AC-156）；重试耗尽后为 dead（AC-157）；webhook 返回 202（AC-160）。
- **依赖**：T92
- **预估**：5h
- **状态**：⬜ 未开始

### T94: agent_trace 迁移与异步写入（FR-84）
- **描述**：编写 `010_add_agent_trace.sql`：`agent_trace(review_id, agent_name, step_index, role, content, created_at)`。ReAct 每步写入，异步批量提交；写入失败只记日志。内容超过 8000 字符截断。
- **文件路径**：
  - `internal/db/migrations/010_add_agent_trace.sql`
  - `internal/repository/trace_repo.go`
  - `internal/ai/agents/compression.go`
- **完成标准**：5 步 ReAct 产生 5 条记录（AC-158）；写入失败不影响审查结果（AC-159）。
- **依赖**：T93, T74
- **预估**：4h
- **状态**：⬜ 未开始

### T95: v1.16 集成测试与交付
- **描述**：更新 CHANGELOG 与 checklist，提示用户打 tag。
- **依赖**：T93, T94
- **预估**：1h
- **状态**：⬜ 未开始

## 阶段二十一：v1.17 事件对账与指纹语义修正（T96-T99）

### T96: 定时对账任务（FR-85）
- **描述**：每日（可配置，默认 03:00）通过 GitHub API 列出近 7 天的 open 与 recently closed PR，与 `review_jobs`、`pr_commits` 比对，缺失的 `synchronize` 事件写入队列（`before_sha` 从 `pr_commits` 推断，无则为空）。
- **文件路径**：
  - `internal/reconcile/reconciler.go`
  - `internal/reconcile/reconciler_test.go`
  - `internal/github/client.go`
  - `cmd/server/main.go`
- **完成标准**：漏发的 synchronize 被补入并执行（AC-161）。
- **依赖**：T93, T92
- **预估**：4h
- **状态**：⬜ 未开始

### T97: closed 事件入队（FR-85）
- **描述**：`closed` 不再直接调用 `TrackOnClosed`，改为写入 `review_jobs`，由 worker 调用。失败按重试策略处理。
- **文件路径**：
  - `internal/handler/webhook.go`
  - `internal/feedback/tracker.go`
  - `internal/orchestrator/worker.go`
- **完成标准**：closed 任务失败后重试，成功后不重复更新（AC-162）。
- **依赖**：T93
- **预估**：2h
- **状态**：⬜ 未开始

### T98: 指纹去行号化变更提案与实现（FR-86）
- **描述**：先提交 `specs/changes/v1.17-static-fingerprint/proposal.md` 并获批准。获批后将 `analysis.Fingerprint` 改为 `<tool>:<ruleID>:<filePath>:<snippetHash>`，并生成旧 ID 到新 ID 的映射表用于回填。
- **文件路径**：
  - `specs/changes/v1.17-static-fingerprint/proposal.md`
  - `internal/analysis/fingerprint.go`
  - `internal/analysis/fingerprint_test.go`
  - `internal/db/migrations/011_add_fingerprint_map.sql`
- **完成标准**：行号上移后 ID 不变（AC-164）；旧 ID 可映射查询（AC-165）；未审批前不得合入（AC-163）。
- **依赖**：T68（v1.11 指纹归一化）、变更提案审批
- **预估**：4h
- **状态**：⬜ 未开始

### T99: v1.17 集成测试与交付
- **描述**：更新 CHANGELOG 与 checklist，提示用户打 tag。
- **依赖**：T96, T97, T98
- **预估**：1h
- **状态**：⬜ 未开始

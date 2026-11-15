# 00 项目画像：CodeReviewAgent

> 扫描方式：只读。未修改任何代码。
> 扫描日期：2026-11-15（仓库内 spec 最后修改于 2026-10-23）。
> 证据等级：**已验证** = 已读取源码并核对行号；**代码推断** = 由已读代码推出，未运行；**待确认** = 需运行或线上数据验证。
> 未读取内容：`.env`、`configs/config.yaml`（可能含密钥，只确认被 `.gitignore` 忽略，见 `.gitignore:2`、`.gitignore:27`）。

## 0. 与题库假设的差异（以代码事实为准）

题库假设为 "GitHub/GitLab + LangGraph + RAG + Mongo + Redis + 向量库" 的审查系统。实际仓库：

| 假设 | 代码事实 | 证据 |
|---|---|---|
| LangGraph / StateGraph | 无。流程是手写的 Go 顺序代码 + `errgroup` | `internal/orchestrator/orchestrator.go:511 Run`；全仓 grep 无 `StateGraph`/`TypedDict` |
| Python | Go 1.26 单体服务 | `go.mod:3` |
| GitLab / MR | 仅 GitHub（`pull_request` 事件、GitHub Review API） | `internal/handler/webhook.go:94`；`internal/github/review.go:179` |
| 向量库 / embedding / rerank / chunk | 无 | 全仓 grep `vector\|embedding\|rerank` 无业务代码命中 |
| Mongo / Redis / 缓存 | 无 | 全仓 grep `mongo\|redis` 无命中；`go.mod` 无对应依赖 |
| 租户 tenant | 无。单租户部署，无 `tenant_id` / `installation_id` | 全仓 grep 无命中 |
| trace_id / request_id | 无 | `cmd/server/main.go` 未挂载 chi RequestID 中间件 |
| 客服业务 | 不适用。领域为 PR 代码审查 | — |

以下题目均按 **PR 代码审查** 领域改写，并在对应题目中标注 TODO。

---

## 1. 技术栈

| 类别 | 选型 | 证据 |
|---|---|---|
| 语言 | Go 1.26.0 | `go.mod:3` |
| LLM 框架 | langchaingo v0.1.15（ReAct 执行器 + OpenAI/Anthropic 客户端） | `go.mod:12`；`internal/ai/llm.go:32 NewLLM` |
| HTTP 路由 | chi v5 | `go.mod:6`；`cmd/server/main.go:50 chi.NewRouter` |
| 数据库驱动 | pgx v5 + pgxpool | `go.mod:9`；`internal/db/db.go:22 Connect` |
| Git 操作 | go-git v5（浅层无关，全量 clone） | `go.mod:7`；`internal/vcs/workspace.go:40 PlainCloneContext` |
| GitHub API | go-github v66 + GraphQL（原生 HTTP 调用） | `go.mod:8`；`internal/github/client.go:23 NewClient`；`internal/github/threads.go:74 ListReviewThreads` |
| 静态分析 | semgrep / golangci-lint 通过 `exec` 调用；gitleaks 作为库嵌入 | `internal/analysis/semgrep.go:95`；`internal/analysis/golangci.go:116`；`go.mod:13` |
| 并发 | `golang.org/x/sync/errgroup` | `go.mod:15`；`internal/orchestrator/orchestrator.go:698`；`internal/analysis/pipeline.go:175` |
| 配置 | viper | `go.mod:10`；`internal/config/config.go:181 Load` |
| 日志 | zap（Production） | `go.mod:14`；`cmd/server/main.go:39` |
| Token 计数 | `tiktoken-go` 仅为间接依赖，代码中未直接使用；实际为字符数/4 估算 | `go.mod:83`；`internal/context/monitor.go:46 EstimateTokens` |

## 2. 入口

| 入口 | 位置 | 说明 |
|---|---|---|
| 服务进程 | `cmd/server/main.go:31 main`，`:38 run` | 连接 DB → 迁移 → 装配 orchestrator → 启动 HTTP |
| Webhook | `cmd/server/main.go:144-145`：`r.With(middleware.HMACVerify(...)).Post("/webhook", ...)` | HMAC 校验后进入 `handler.Webhook.ServeHTTP` |
| 离线评估 | `cmd/eval/main.go:34 main`；`Makefile:33` | 回放 `eval/dataset/` 样本 |
| 开发 Webhook 代理 | `docker-compose.yml:35-44`（smee-client） | 将 GitHub 投递转发到 `app` |

## 3. 路由表（全部）

| 路径 | 注册位置 | 鉴权 |
|---|---|---|
| `GET /healthz` | `cmd/server/main.go:51` | 无 |
| `POST /webhook` | `cmd/server/main.go:144-145` | HMAC-SHA256（`internal/middleware/hmac.go:20 HMACVerify`） |
| `GET /dashboard`、`/dashboard/reviews/{id}` | `internal/handler/dashboard.go:105-106` | **无**（已验证：路由注册处无中间件） |
| `GET /api/reviews`、`/api/reviews/{id}`、`/api/metrics/feedback` | `internal/handler/dashboard.go:107-109` | **无** |
| `GET /api/stream-review?pr_id=` | `cmd/server/main.go:141` → `internal/handler/sse.go:198 MountStreamReview` | **无** |
| `GET /*` 静态文件 | `cmd/server/main.go:142` → `internal/handler/static.go:12` | 无 |

## 4. 流程定义（非图，顺序代码）

**4.1 事件层**
- `internal/handler/webhook.go:81 ServeHTTP` → `:113 handlePullRequest`
- `opened` / `synchronize`：`webhook.go:141` 起 goroutine，超时 10 分钟，调用 `PullRequestProcessor.Process`
- `synchronize` 同时触发 `dispatchTrackOnSynchronize`（`webhook.go:183`）
- `closed`：`dispatchTrackOnClosed`（`webhook.go:197`）
- 其他 action 与非 `pull_request` 事件直接返回 ignored

**4.2 Pipeline 层（`internal/orchestrator/pipeline.go:100 Process`）**
1. `CreateReview` 写入 `pending`（`:111`）
2. `vcs.PrepareWorkspace` 全量 clone 并 checkout PR head（`:123`，`internal/vcs/workspace.go:25`）
3. `ListReviewThreads` 读取既有线程（`:134`，失败降级）
4. `LatestCompletedReview` 取锚点（`:142`）
5. `resolveDiff`：增量 Compare 或全量 PR diff（`:150`，`:306`）
6. `UpdateReviewAnchor`（`:156`）、`loadPriorFindings`（`:160`，`:337`）
7. `orch.Run`（`:163`）
8. 持久化 preFindings / agent_status / agent_results / findings（`:180-227`）
9. `review.PlanPublish`（`:247`）→ `SubmitReviewPlan`（`:255`）→ `ResolveReviewThread`（`:267`）
10. `UpdateCommitStatus`（`:274`）→ `UpdateReviewStatus(completed)`（`:281`）
11. 任一步 `orch.Run` 报错 → `failReview`（`:292`），写 failure commit status

**4.3 Orchestrator 层（`internal/orchestrator/orchestrator.go:511 Run`）**
- 先 `checkDuplicate`（`:512`，`:925`）
- Step 2 `diff.Parse`（`:530`）→ Step 3 过滤/跳过二进制（`:553`）→ Step 3.4 策略（`:576`，`review/strategy.go:40 Select`）→ 无实质变更则短路（`:602`）
- Step 3.5 静态分析前置（`:630`）→ Step 3.6 上下文构建（`:657`，`internal/context/builder_impl.go:45 Build`）
- Step 4+5 `errgroup` 并行执行 Agent（`:698-770`），每个 Agent 经 `runAgent`（`:902`）→ Harness
- Step 6 Verifier（`:779-795`）→ Step 7 排序（`:798`）

**4.4 单 Agent 层（`internal/harness/harness_impl.go:131 Execute`）**
- 包装工具（`:226 governTools`），注入 prompt 装配函数（`:152`）
- 调用 `agent.Review`（`:155`），结构校验（`:157`，`internal/ai/agents/validate.go:58`）
- 完成门禁：零工具调用 → partial（`:194`）；引用文件未读取 → partial（`:202-220`）

## 5. State 定义（无统一 State 对象）

状态分散在多个结构体中，没有 `ReviewState` 之类的统一对象：

| 结构体 | 位置 | 职责 |
|---|---|---|
| `EventInput` | `internal/orchestrator/pipeline.go:67` | webhook → pipeline 的事件输入 |
| `Input` | `internal/orchestrator/orchestrator.go:52` | 单次审查请求 |
| `Result` | `internal/orchestrator/orchestrator.go:129` | 单次审查结果（含 `AgentStatus`、`AgentResults`、`PriorVerdicts`） |
| `AgentResult` | `internal/harness/types.go:56` | 单 Agent 的 Status / Findings / ToolCallCount / ErrorReason / ExecutionLogs |
| `ReviewInput` | `internal/ai/agents/agent.go:105` | Agent 输入（Diffs / PreFindings / Context / Tools / PriorFindings / Scope） |
| `ContextBlock` | `internal/context/assembler.go:29` | Prompt 块（Source / Content / Tokens / IsPinned / Sequence） |
| `AgentFinding` / `VerifiedFinding` | `internal/ai/agents/agent.go:72` / `verifier.go:30` | Agent 产出 / 验证后产出 |

状态枚举与原因码：
- `harness/types.go:32 StatusCompleted`（另有 partial / failed）
- `harness/harness_impl.go:20 ErrorReasonNoToolCalls = "suspicious_no_tool_calls"`
- `harness/types.go:15 ErrorReasonUnverifiedCitation = "unverified_citation"`
- `harness/oscillation.go:13 ErrOscillation`

## 6. Checkpoint 存储

| 项 | 位置 |
|---|---|
| 建表 | `internal/db/migrations/006_add_checkpoints.sql:4 harness_checkpoints`（初始唯一键 `(head_sha, agent_name)`，`:13`） |
| 扩展 | `internal/db/migrations/007_add_review_anchor.sql:28` 唯一键改为 `(head_sha, agent_name, prompt_hash, config_hash)` |
| 写入 | `internal/repository/checkpoint_repo.go:65 Save`，`ON CONFLICT ... DO UPDATE`（`:73`） |
| 读取 | `checkpoint_repo.go:111 GetBySHAAndAgentWithHashes` |
| 调用方 | `internal/orchestrator/orchestrator.go:341 loadCheckpoints`（仅复用 `completed`，`:359`）；`:417 saveCheckpoint`（`WithoutCancel` + 5s，`:430`） |
| 缓存键构成 | `behaviorConfigHash`（`orchestrator.go:399`）只哈希 `CompressionConfig` 与 `StaticAnalysisConfig`；**不含 LLM provider / 模型名 / verifier 开关 / 锚点 PRIOR 集合** |

## 7. 工具列表

| 工具 | 位置 | 是否生产挂载 |
|---|---|---|
| `read_file` | `internal/ai/tools/read_file.go:47 ReadFileTool`，`:59 Name` | **是**（`internal/orchestrator/orchestrator.go:914 buildTools`） |
| `search_code` | `internal/ai/tools/search_code.go:25 SearchCodeTool`，`:37 Name` | 否（已实现，未注册） |
| `get_function_definition` | `internal/ai/tools/get_function.go:21 GetFunctionDefTool`，`:30 Name` | 否（已实现，未注册） |
| git 历史工具（blame / log） | — | TODO（spec v1.14 规划，代码不存在） |

静态分析器不是 Agent 工具，而是 Pre-analysis 管道：`internal/analysis/semgrep.go`、`golangci.go:59 GolangciLintAnalyzer`、`gitleaks.go`，由 `internal/analysis/pipeline.go:127 Run` 并发执行。

## 8. 检索 / 向量 / Rerank / Chunk / Embedding

| 能力 | 结论 | 证据 / TODO |
|---|---|---|
| 向量库 | **无** | TODO：如需语义检索历史评论，需新增（题库 Q06） |
| Embedding 服务 | **无** | TODO（题库 Q09） |
| Rerank | **无** | TODO（题库 Q07） |
| Chunk | **无文本分块**。diff 按 hunk 结构化处理 | `internal/diff/types.go`（Hunks / Lines）；`internal/ai/agents/agent.go:412 renderDiff` |
| 文件大小限制 | 超过 5000 行跳过静态分析 | `internal/analysis/pipeline.go`（`maxFileLines` 常量，`:163` 引用） |
| 结构化代码检索 | 有：正则搜索、函数定义抽取、调用点检索 | `search_code.go:47`；`get_function.go:40`；`internal/context/callers.go:72 Callers`；`callees.go:16 ExtractCallees`；`detector.go:22 DetectStructuralChanges` |
| 上下文预算 | 字符/4 估算 + 双轨压缩 | `internal/context/monitor.go:74 ShouldCompress`；`compressor.go:39 Compress` |

## 9. 业务 DB（PostgreSQL 16）

| 表 | 迁移 | 说明 |
|---|---|---|
| `reviews` | `001_create_tables.sql:5` | 审查记录；后续迁移增加 pre_findings / agent_status / agent_results / reviewed_sha 等列 |
| `findings` | `001_create_tables.sql:19` | 验证后 finding；`review_id` 外键 `ON DELETE CASCADE`（`:21`） |
| `pr_commits` | `004_add_feedback.sql:6` | 唯一键 `(repo_full_name, pr_number, sha)`（`:12`） |
| `harness_checkpoints` | `006_add_checkpoints.sql:4` | 见第 6 节 |
| `schema_migrations` | `internal/db/db.go:54` | 运行时手写迁移记录表 |

- 迁移嵌入：`internal/db/db.go:17 //go:embed migrations/*.sql`
- 迁移执行：`internal/db/db.go:44 Migrate`，**无 advisory lock、单文件无事务包裹**（`:77` 直接 `Exec`，`:82` 再单独 INSERT 记录）
- 迁移文件共 8 个：001–008（009 之后未实现，见第 15 节）
- 启动即迁移：`internal/db/db.go:132 MigrateFromURL` 被 `cmd/server/main.go:66` 调用

## 10. 缓存

- **无独立缓存层**（无 Redis / Memcached / 进程内 LRU）。
- Checkpoint 是 DB 级的结果复用，不是缓存层。
- Prompt 版本哈希：`internal/ai/prompts/prompts.go:27 Hash`（`go:embed *.txt`，`:13`）。
- 去重窗口为进程内存：`internal/orchestrator/orchestrator.go:248 lastSeen`，`:35 duplicateWindow = 30s`，`:925 checkDuplicate`。**无清理逻辑**，map 只增不减（已验证：全仓 `lastSeen` 仅见写入与读取）。
- Clone 无缓存：每次审查全量 clone（`internal/vcs/workspace.go:40`）。

## 11. 锁与并发控制

| 机制 | 位置 | 作用域 |
|---|---|---|
| `golangciRunMu sync.Mutex` | `internal/analysis/golangci.go:23` | 进程内串行 golangci-lint，外加 8 次 × 10s 重试（`:109-129`） |
| `Orchestrator.mu` | `internal/orchestrator/orchestrator.go:247` | 保护 `lastSeen` |
| `recorder.mu` | `internal/harness/harness_impl.go:240` | 工具调用记录 |
| `pathReadSet.mu` (RWMutex) | `internal/harness/readset.go:11` | 已读文件集合 |
| `errgroup` 并行 | `orchestrator.go:698`、`analysis/pipeline.go:175` | Agent 并行、分析器并行 |
| 数据库唯一约束做幂等 | `checkpoint_repo.go:73`；`feedback_repo.go:109 ON CONFLICT DO NOTHING` | 跨进程幂等 |
| 受保护更新 | `feedback_repo.go:94-95`（`WHERE feedback_status = $3`） | 状态机只允许 pending → 终态 |
| **per-PR 互斥** | **无**（`internal/orchestrator/pipeline.go:79 Pipeline` 无锁字段） | TODO：同一 PR 并发 push 时可能并行审查 |
| **advisory lock / SKIP LOCKED** | **无**（全仓无 `pg_advisory` / `FOR UPDATE`） | TODO：多副本迁移与任务领取无保护 |

## 12. 监控与可观测性

| 项 | 实际 | 证据 |
|---|---|---|
| 结构化日志 | zap Production；字段含 `repo` / `pr` / `head_sha` / `agent` | `cmd/server/main.go:39`；`pipeline.go:101` |
| 指标端点 | 仅 `/api/metrics/feedback`（业务反馈统计，非 Prometheus） | `internal/handler/dashboard.go:114 handleAPIMetricsFeedback` |
| Prometheus / OpenTelemetry | **无业务埋点**（`go.sum` 中 otel 为间接依赖） | `go.sum:417-428` |
| 健康检查 | `/healthz` | `cmd/server/main.go:51` |
| 执行轨迹 | Harness 内存 `ExecutionLogs` 写入 `reviews.agent_results`，**无逐步 trace 表** | `internal/harness/types.go:56`；`pipeline.go:197-206` |
| 实时流 | SSE 事件总线（内存 broker） | `internal/handler/sse.go:25 NewEventBroker`，`:48 Publish`；`cmd/server/main.go:133-140` |
| panic 保护 | **无 `recover()`**，全仓 0 处。Webhook 的异步 goroutine 中 panic 会终止进程 | `handler/webhook.go:141`、`orchestrator/pipeline.go` |

## 13. 部署

| 项 | 位置 | 备注 |
|---|---|---|
| 构建 | `Dockerfile:2` `golang:1.26-alpine`，`:11` `CGO_ENABLED=0` 静态编译 | 多阶段 |
| 运行时 | `Dockerfile:14` `alpine:3.20`，`:16-17` 非 root 用户 `app` | 证书与时区包已装 |
| **运行时缺失工具** | 运行镜像未安装 semgrep、golangci-lint，也无 Go 工具链 | 已验证（`Dockerfile:14-22`）；静态分析会走降级路径，**待确认** 容器内实际行为 |
| 编排 | `docker-compose.yml`：postgres:16-alpine（`:2-16`）+ app（`:18-33`）+ smee（`:35-44`） | 无 K8s、无 Redis、无消息队列 |
| 网络暴露 | `app` 映射 `8080:8080`（`:32-33`） | 与第 3 节"无鉴权路由"叠加，风险见 03 文档 |
| CI | **无**。仓库不存在 `.github/` 目录 | 已验证（glob 无结果） |

## 14. 测试

| 项 | 位置 |
|---|---|
| 测试命令 | `Makefile:18 test`：`go test ./... -race` |
| 数据库测试开关 | `SKIP_DB_TESTS=1`（如 `internal/repository/review_repo_test.go:26`） |
| 外部 CLI 测试开关 | `SKIP_SEMGREP_TESTS`（`internal/analysis/semgrep_test.go:114`）、`SKIP_LINT_TESTS`（`golangci_test.go:118`） |
| 端到端 | `internal/integration/e2e_test.go:147`（`testing.Short()` 跳过） |
| Agent 测试 | 使用 `mockLLM`（`internal/ai/agents/security_test.go:23`）；注入样例位于 `eval/dataset/injection-*` |
| 评估 | `internal/eval/evaluator.go:145 ComputeMetrics`、`:162 BuildReport`；`Makefile:33 eval` |
| 覆盖缺口 | 未找到断言"Verifier 为 nil 时 Findings 被丢弃"行为的测试（见 03 文档 R01） |

## 15. 已规划未实现（spec 中存在，代码中不存在）

| 规划项 | spec 位置 | 代码证据 |
|---|---|---|
| 持久化任务队列 `review_jobs` | spec 27.2（v1.16） | 迁移仅到 008 |
| `agent_trace` 轨迹表 | spec 27.2（v1.16） | 无对应迁移 |
| 每日对账任务 | spec 28.2（v1.17） | 无 `reconcile` 包 |
| 仓库规则文件 `.codereviewagent/rules.md` | spec 26.2（v1.15） | 全仓无该路径读取 |
| PR 级预算 | spec 26.2（v1.15） | 无 `budget` 配置 |
| 精确 Tokenizer | spec 26.3（v1.15） | 无 `Tokenizer` 接口 |
| git 历史工具 | spec 25.2（v1.14） | 无 `git_history.go` |

## 16. 安全相关

| 项 | 实现 | 证据 |
|---|---|---|
| Webhook 签名 | HMAC-SHA256，`hmac.Equal` 常量时间比较 | `internal/middleware/hmac.go:20`、`:43 verifySignature`、`:58` |
| GitHub Token | BasicAuth 注入 go-git，**不拼入 URL** | `internal/vcs/workspace.go:38` |
| 不可信内容隔离 | 随机 nonce 边界包裹 diff / 静态分析 / PRIOR 数据 | `internal/ai/agents/untrusted.go:21 WrapUntrusted`、`:33 newNonce`、`:43 wrapSection`；`agent.go:286 prepareQuestion` |
| 输出结构校验 | 路径与行号必须在 diff 内，剥离非白名单字段 | `internal/ai/agents/validate.go:58 Validate`、`:118 StripNonWhitelistedFindingFields` |
| 工具路径穿越防护 | `resolvePath` 拒绝越界路径 | `internal/ai/tools/read_file.go:22` |
| 仪表盘 / API / SSE 鉴权 | **无** | 见第 3 节 |
| 密钥管理 | 环境变量与未提交的 config 文件；`.env` 与 `configs/config.yaml` 被忽略 | `.gitignore:2,27`；`.dockerignore:2` |

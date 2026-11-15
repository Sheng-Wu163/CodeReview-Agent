# 03 TODO 与风险清单

> 扫描为只读，未修改代码。风险项的"验证方式"均为待执行的复现步骤；"已验证"表示已通过阅读源码核对行号，尚未运行。
> 严重度：高 = 可能导致错误结论发布或数据泄露；中 = 可能导致重复评论、失败或成本失控；低 = 可维护性问题。

## 一、TODO（需要你提供或确认的事项）

| ID | 事项 | 影响的题目 | 说明 |
|---|---|---|---|
| T01 | 提供原 24 题清单 | 全部 | 当前 02 文件是按自检覆盖项重建的。原清单中的"客服业务"（原第 17 题）若存在，需要替换为 PR 审查场景；本次重建中未出现客服题，因此无法逐题对齐 |
| T02 | 用真实线上事故替换 Q20 | Q20 | 当前为从代码风险构造的复盘模板，题目中已标 TODO |
| T03 | 确认 `Input.ForceRerun`（`internal/orchestrator/orchestrator.go:65`）是否有外部 API 入口 | Q02 | spec 19.2 FR-60 提到 API 参数，代码中只见配置项 `force_rerun` |
| T04 | 确认 go-git 在目标平台是否会创建符号链接（checkout 行为） | Q05, R04 | 决定符号链接风险的真实可利用性 |
| T05 | 确认 ReAct observation（工具输出）是否在不可信边界内 | Q07 | `agent.go:305 renderTaskState` 只包裹了 diff、静态分析、PRIOR、上下文 |
| T06 | 确认 langchaingo 的 OpenAI/Anthropic 客户端是否有默认重试 | Q19, R10 | `internal/ai/llm.go:32` 未配置 retry 选项 |
| T07 | 确认 GitHub 在 secondary rate limit 时的响应与当前处理 | Q19 | `internal/github/client.go` 无重试与 Retry-After 处理 |
| T08 | 确认 Docker 运行镜像内 semgrep / golangci-lint 的实际降级行为 | R11 | `Dockerfile:14-22` 未安装这两个工具 |
| T09 | 确认 semgrep / golangci-lint 超时后子进程树是否被杀死 | Q04, R21 | 只确认了 `exec.CommandContext` 的使用 |
| T10 | 获取 GitHub 真实重投样本（投递间隔与重复频率） | Q24 | 决定 30 秒窗口是否足够 |
| T11 | 确认合规删除（按仓库删除审查记录）的需求与路径 | Q15 | checkpoint、GitHub 评论的处理未定义 |
| T12 | 确认是否存在人工驳回 finding 的入口 | Q22 | dashboard 中只见反馈统计，未见人工操作入口 |
| T13 | 确认 `.env` 与 `configs/config.yaml` 是否曾被提交进 git 历史 | 安全 | 本次未读取这两个文件内容，只确认被 `.gitignore` 忽略（`.gitignore:2,27`）。若曾提交，需要轮换相关密钥 |
| T14 | 确认是否有外部 CI 系统（仓库内无 `.github/` 目录） | R18 | 决定测试与 lint 是否被强制执行 |
| T15 | 确认 `internal/eval` 与 `eval/dataset` 的样本内容是否包含真实代码 | 安全 | 本次只确认了目录结构与注入样例的存在 |

## 二、风险清单

| ID | 严重度 | 风险 | 证据（path:line symbol） | 证据等级 | 验证方式 | 相关题目 |
|---|---|---|---|---|---|---|
| R01 | 高 | `verifier.enabled=false` 时，Standard/Complex PR 的所有 finding 被静默丢弃，并可能发布 LGTM，commit status 为 success | `internal/orchestrator/orchestrator.go:785-794`（`res.Findings` 仅在 verifier 非 nil 时赋值）；`cmd/server/main.go:105`；`internal/orchestrator/pipeline.go:251-253` | 已验证（静态） | 在 orchestrator 测试中把 verifier 设为 nil，Standard 策略下断言 `len(res.Findings) > 0`，预期当前失败 | Q14, Q20 |
| R02 | 高 | 同一 head_sha 的重复投递被判为 ErrDuplicatePR 后走 failReview，写入 failure commit status，覆盖第一次成功审查的 success | `internal/orchestrator/orchestrator.go:925 checkDuplicate`；`internal/orchestrator/pipeline.go:175`；`pipeline.go:292 failReview` | 已验证（静态） | 连续两次以相同 head_sha 调用 Pipeline.Process（mock GitHub），检查最后一次 UpdateCommitStatus 的 state | Q12, Q24 |
| R03 | 高 | Agent 超时或震荡时，真实 reviewAgent 返回 nil findings，"保留中断前 findings"（spec 17.4）不成立；测试中只有 mock 能返回 findings+error | `internal/ai/agents/agent.go:226-228`；`internal/harness/harness_impl.go:155-190` | 已验证（静态） | 用一个在工具调用循环中超时的 LLM mock，断言 AgentResult.Findings 是否为空；对比 spec 17.4 的期望 | Q03 |
| R04 | 高 | 符号链接可能让 read_file 读取工作区外的宿主机文件，内容进入 LLM prompt 与 PR 评论 | `internal/ai/tools/read_file.go:26-41`（仅词法检查）；`read_file.go:78`（os.ReadFile）；`internal/vcs/workspace.go:40` | 代码推断 | 构造包含指向工作区外文件的符号链接的 PR 样本，调用 `ReadFileTool.Call`，断言被拒绝 | Q05 |
| R05 | 高 | dashboard、API 与 SSE 无鉴权，任何能访问 8080 端口的人可读取全部审查记录、findings 与 SSE 事件（包含代码片段） | `cmd/server/main.go:141`；`internal/handler/dashboard.go:104-109`；`internal/handler/sse.go:123`（pr_id 来自查询参数） | 已验证（静态） | 对未认证请求访问 `/api/reviews` 与 `/api/stream-review?pr_id=...`，确认返回数据 | Q05 |
| R06 | 中 | 线程读取只取第一页（first:100），超过 100 条线程时 PlanPublish 把已存在的线程当作 new，产生重复评论；resolved 判断也会失真 | `internal/github/threads.go:16`、`:19`（first:100）；`threads.go:74 ListReviewThreads` | 已验证（静态） | 用 150 条线程的 mock 响应验证第 101 条之后被忽略 | Q19 |
| R07 | 中 | webhook 异步 goroutine 与 Pipeline 均无 recover，任何 panic 会终止整个进程，未完成的审查丢失 | `internal/handler/webhook.go:141`；`internal/orchestrator/pipeline.go:100`；全仓 `recover()` 为 0 处 | 已验证（静态） | 在 PullRequestProcessor mock 中 panic，观察进程是否退出 | Q13 |
| R08 | 中 | 去重表 lastSeen 只增不减，长期运行内存持续增长 | `internal/orchestrator/orchestrator.go:248`、`:932-935` | 已验证（静态） | 对 N 个不同 head_sha 调用 checkDuplicate，观察 map 大小 | Q24 |
| R09 | 中 | reviews 行在进程崩溃后永久停留在 pending，没有超时回收 | `internal/orchestrator/pipeline.go:111 CreateReview`；全仓无 pending 回收逻辑 | 代码推断 | 在 SubmitReviewPlan 之后注入 panic/退出，检查 reviews 状态 | Q01, Q10 |
| R10 | 中 | checkpoint 缓存键不包含 LLM provider、模型名、verifier 开关与 PRIOR 集合，配置变更后可能复用旧结论 | `internal/orchestrator/orchestrator.go:399-412`（behaviorConfigHash）；`:354-361` | 已验证（静态） | 修改模型配置后对同一 SHA 重跑，检查是否命中 checkpoint | Q02 |
| R11 | 中 | Docker 运行镜像未包含 semgrep、golangci-lint，静态分析在容器内会降级，但降级结果是否被用户感知待确认 | `Dockerfile:14-22` | 已验证（静态） | `docker run` 后进入容器执行 `semgrep --version` 与 `golangci-lint version` | Q14 |
| R12 | 中 | 同一 PR 的不同 head_sha 并行审查时没有 per-PR 锁，锚点读取存在竞态，增量范围可能重复或遗漏 | `internal/orchestrator/pipeline.go:79 Pipeline`（无锁字段）；`pipeline.go:142` | 代码推断 | 对同一 PR 并发调用两次 Process（不同 SHA），检查锚点与评论 | Q12 |
| R13 | 中 | 迁移在每次启动时执行，无 advisory lock，每个文件的 Exec 与 INSERT 记录不在同一事务中，多副本同时启动时可能重复执行或中途失败 | `internal/db/db.go:44 Migrate`（`:77` 执行 SQL，`:82` 记录版本）；全仓无 `pg_advisory` | 已验证（静态） | 同时启动两个进程，观察 schema_migrations 与表结构 | Q01 |
| R14 | 中 | 每次审查全量 clone（无 depth、无缓存），大仓库耗时与磁盘占用高 | `internal/vcs/workspace.go:40 PlainCloneContext` | 已验证（静态） | 对大仓库测量 PrepareWorkspace 耗时与临时目录大小 | Q23 |
| R15 | 中 | Verifier 的 LLM 调用错误使整个 Run 失败，而不是降级为"未验证"，LLM 故障会导致所有 PR 显示 failure | `internal/ai/agents/verifier.go:112-116`；`internal/orchestrator/orchestrator.go:790-792` | 已验证（静态） | 让 LLM mock 在 Verify 阶段返回错误，断言 Run 的返回值 | Q14 |
| R16 | 中 | 没有 CI 配置，测试与 lint 不强制执行 | 仓库无 `.github/` 目录（glob 无结果） | 已验证 | 检查远端仓库的 CI 配置（T14） | — |
| R17 | 中 | 无业务指标与 trace，无法建立审查时长、失败率 SLO，pending 卡死不可见 | `cmd/server/main.go:39`；`internal/handler/dashboard.go:114` 为唯一指标端点 | 已验证（静态） | 检查监控系统中是否有相关指标 | Q13 |
| R18 | 中 | LLM 输出在 max_tokens 截断后，整个 Agent 的 findings 因 JSON 解析失败全部丢失 | `internal/ai/agents/agent.go:443 parseFindings`；`agent.go:41` | 代码推断 | 构造截断的 JSON 数组，断言 parseFindings 的返回 | Q18 |
| R19 | 低 | 震荡检测只认完全相同的参数哈希，等价路径写法可绕过 | `internal/harness/oscillation.go:27 ArgsHash`；`:50 DetectOscillation` | 已验证（静态） | 交替调用 `./a.go` 与 `a.go`，检查是否触发 | Q03 |
| R20 | 低 | 静态分析超时后的子 goroutine 不会回收（代码注释已承认） | `internal/analysis/pipeline.go:228-249`、`:237` | 已验证（静态） | 制造挂起的 mock analyzer，观察 goroutine 数量 | Q04 |
| R21 | 低 | 工具输出的不可信包裹范围待确认 | `internal/ai/agents/agent.go:305`；`untrusted.go:43 wrapSection` | 待确认 | 见 T05 | Q07 |
| R22 | 低 | errgroup 未使用 WithContext，单个 Agent 失败不会取消其他 Agent，浪费 LLM 调用 | `internal/orchestrator/orchestrator.go:698` | 已验证（静态） | 设计选择，需与 spec 对齐（FR-09 / NFR-02 要求失败隔离） | Q16 |

## 三、与规格（spec）不一致的已验证项

| 规格位置 | 规格要求 | 代码现状 | 对应风险 |
|---|---|---|---|
| spec 17.4 | 中断前 findings 必须保留，状态为 partial | 真实 reviewAgent 在 error 时返回 nil findings | R03 |
| spec 19.2 FR-60 | 通过配置或 API 参数 force_rerun 强制重跑 | 代码中只见配置项，API 入口未确认 | T03 |
| spec 23.2 FR-73 | 引用文件未读取则 partial | 已实现（`harness_impl.go:202-220`） | — 一致 |
| spec 22 / 27 / 28 | review_jobs、agent_trace、对账任务、rules.md、budget、tokenizer、git 工具 | 均未实现，迁移仅到 008 | 见 00 文档第 15 节 |

## 四、本次扫描的未覆盖范围

- `internal/eval`、`cmd/eval` 的指标细节（只确认了函数位置）
- `internal/feedback` 的相似度算法细节（只确认了入口）
- `internal/handler/sse.go` 的事件广播细节与 `templates/`、`static/` 前端代码
- `rules/semgrep/*.yml` 的规则内容，以及 gitleaks 嵌入规则
- `internal/ai/prompts/*.txt` 的提示词内容
- `eval/dataset/*` 的样本内容（只确认了目录与注入样例存在）
- 所有测试文件的断言内容（只确认了测试函数名与位置）

## 五、自检

### 5.1 24 题覆盖情况

| 覆盖项 | 题号 | 状态 |
|---|---|---|
| 状态机 | Q01 | 覆盖 |
| checkpoint | Q02 | 覆盖 |
| conditional edge / Command / interrupt | Q03 | 覆盖（映射为 Harness 状态码与门禁；interrupt 缺失已说明） |
| 异步工具 | Q04 | 覆盖 |
| 权限边界 | Q05 | 覆盖 |
| 向量 + 业务 DB | Q06 | 覆盖（TODO） |
| 混合检索 / rerank | Q07 | 覆盖（TODO） |
| chunk | Q08 | 覆盖 |
| embedding 服务 | Q09 | 覆盖（TODO） |
| 一致性 | Q10 | 覆盖 |
| Mongo 写放大 | Q11 | 覆盖（映射为 PostgreSQL 写放大，说明了原设定不适用） |
| 并发锁 | Q12 | 覆盖 |
| 监控 | Q13 | 覆盖 |
| 降级 | Q14 | 覆盖 |
| 审计 | Q15 | 覆盖 |
| 多 Agent | Q16 | 覆盖 |
| 幻觉校验 | Q17 | 覆盖 |
| JSON 解析 | Q18 | 覆盖 |
| 第三方 API | Q19 | 覆盖 |
| 故障复盘 | Q20 | 覆盖（TODO，待替换真实事故） |
| LangGraph vs 工作流 | Q21 | 覆盖 |
| 长期记忆 | Q22 | 覆盖 |
| 降本 | Q23 | 覆盖 |
| 幂等与重放 | Q24 | 覆盖 |

### 5.2 锚点与 TODO

- 每题至少有一个 `path:line symbol` 锚点，或在题面中明确标注 TODO。Q06、Q07、Q09 的核心组件不存在，锚点指向现有相邻组件，并标注 TODO。
- Q20 为复盘模板，锚点来自风险代码，题面中标注了 TODO。
- 代码锚点的行号来自读取或 grep 的结果，尚未逐行运行验证。个别条目（如 A258 的数据集目录）是目录级锚点，已在 CSV 中说明。

### 5.3 避免编造

- 未找到的组件（向量库、embedding、rerank、Redis、Mongo、租户、trace_id、per-PR 锁、advisory lock、CI）均标为 TODO 或"无"，没有编造类名或函数名。
- 所有类名、函数名、字段名都来自读取过的源码或 grep 结果。
- 题面未给出答案。涉及"已验证"的结论只陈述代码路径，运行结果一律标为"待确认"。
- 未泄露密钥、token、内部域名或真实用户数据。未读取 `.env` 与 `configs/config.yaml`。

### 5.4 本次未完成或需要你处理的事项

1. 原 24 题清单未在上下文中，需要 T01 提供后逐题对齐。
2. R01、R02、R03、R04、R05 为高严重度，建议优先用测试或手工复现确认。
3. 风险项中凡标注"代码推断"或"待确认"的，都不应直接作为面试中的结论陈述。

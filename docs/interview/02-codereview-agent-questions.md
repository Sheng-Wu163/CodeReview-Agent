# 02 CodeReview Agent 面试题（24 题）

> 来源说明：原 24 题清单不在当前上下文中。本文件以"自检覆盖项"为骨架重建（状态机、checkpoint、中断/门禁、异步工具、权限、向量+业务DB、混合检索/rerank、chunk、embedding、一致性、写放大、并发锁、监控、降级、审计、多 Agent、幻觉校验、JSON 解析、故障复盘、第三方 API、LangGraph vs 工作流、长期记忆、降本、幂等重放）。如需与原题逐题对齐，请提供原清单（见 03 文档 T01）。
> 锚点格式：`path:line symbol`，括号内为 `01-code-anchors.csv` 的 anchor_id。
> 证据等级：**已验证** = 已读源码核对；**代码推断** = 由代码推出，未运行；**待确认** = 需运行或线上数据；**TODO** = 代码中不存在，题目为"如果让你补"。
> 题面中的 `tenant` 当前不存在（单租户），相关题目以 TODO 标注。

---

## Q01 State 设计与状态流转
- 难度/题型：高 / 设计取舍
- 题面：请基于 `internal/orchestrator/pipeline.go:100 Pipeline.Process` 与 `internal/orchestrator/orchestrator.go:511 Orchestrator.Run`，画出一次 PR webhook（opened/synchronize）到 review 回写的状态流转。本项目没有统一的 State 对象，状态分散在 `orchestrator.go:52 Input`、`orchestrator.go:129 Result`、`internal/harness/types.go:56 AgentResult`、`internal/ai/agents/agent.go:105 ReviewInput` 中。请解释：为什么 ReAct 的对话历史不作为 State 持久化？如果 `reviews` 行中的 `agent_results` JSONB 持续膨胀，你会怎么拆？
- 代码锚点：`internal/orchestrator/pipeline.go:111 ReviewStore.CreateReview`；`internal/orchestrator/pipeline.go:281 UpdateReviewStatus`；`internal/repository/review_repo.go:175 ReviewRepo.UpdateReviewAgentResults`（A025, A042, A064, A065, A087, A212）
- 考察点：
  1. 能指出"无统一 State"的事实，以及各层输入输出的边界
  2. 区分结论状态（AgentResult）与过程状态（ReAct steps，只在内存中）
  3. 理解 pending 是唯一中间态，且没有回收机制
  4. 理解 JSONB 整列覆盖的写放大
- 追问链：
  1) 代码事实：`pipeline.go:111` 写入 pending；`pipeline.go:209-227` 逐条写 findings；`pipeline.go:255` 提交 Review；`pipeline.go:281` 才标 completed。`review_repo.go:175` 整列覆盖 `agent_results`。
  2) 设计取舍：ReAct 步骤只存在于 `agent.go:218` 的 executor 内存中，`compression.go:63 compressSteps` 压缩后即丢弃，持久化的只有结论与 `ToolLog`（`harness/types.go:44`，只存 ArgsHash）。State 小、可审计，代价是无法从中间状态续跑。
  3) 线上故障：进程在 `:255` 之后、`:281` 之前退出，reviews 行停在 pending。全仓没有把超时 pending 置为 failed 的逻辑（代码推断）。你如何检测并补偿？如果 agent_results 膨胀，拆分方向是什么（ExecutionLogs 独立成 trace 表，spec v1.16 规划，未实现）？
- 期望答案要点：
  1. 画出 webhook → Process → Run → 持久化 → 回写 → completed 的顺序，指出 pending 是唯一中间态
  2. 说明 State 分散的原因：避免把 ReAct 历史带入 DB，各层只关心自己的输入输出
  3. 膨胀拆分：结论与执行日志分离；大字段外置或分表
  4. pending 卡死：超时扫描补偿，或引入 review_jobs 队列（TODO）
- 危险信号：
  1. 把完整对话历史塞进 reviews 的 JSONB 列
  2. 把 pending 当作"正在运行"而没有超时
  3. 认为"整列覆盖不会丢数据"就忽略了一致性边界
- 证据等级：已验证（结构与顺序）；pending 卡死为代码推断

## Q02 Checkpoint 与断点续跑
- 难度/题型：高 / 故障排查
- 题面：Agent 完成后，`harness_checkpoints` 以 `(head_sha, agent_name, prompt_hash, config_hash)` 为键保存结果。同一 PR 的 head commit_sha 被重新投递审查时，哪些 Agent 会直接复用 checkpoint？如果运维把 LLM 模型从 A 换成 B，但 commit_sha 不变，结论会怎样？checkpoint 与 review job（当前无队列）是什么关系？
- 代码锚点：`internal/orchestrator/orchestrator.go:341 Orchestrator.loadCheckpoints`；`internal/orchestrator/orchestrator.go:399 Orchestrator.behaviorConfigHash`；`internal/repository/checkpoint_repo.go:73`（ON CONFLICT）；`internal/db/migrations/007_add_review_anchor.sql:28`（A054, A055, A056, A057, A207, A228）
- 考察点：
  1. 幂等键选择与 commit_sha 的语义
  2. 只复用 completed 的条件
  3. 缓存键缺项：模型、verifier 开关、PRIOR 集合
  4. Save 失败不阻断审查的取舍
- 追问链：
  1) 代码事实：`orchestrator.go:359` 只复用 `completed`；`orchestrator.go:400-405` 的 behaviorConfigHash 只哈希 CompressionConfig 与 StaticAnalysisConfig，不含 LLM provider、模型名、verifier.enabled。
  2) 设计取舍：为什么用 head commit_sha 而非 review_id 作幂等键？review_id 每次新建，无法跨投递复用；commit_sha 锁定代码版本，代码变则结论失效。代价是键没有包含"判定所依据的环境"。
  3) 线上故障：更换模型后，运维对同一 PR 手动重跑，结果仍是旧模型的结论。在不清空整张表的前提下如何修复？（答：把 provider/模型纳入 behaviorConfigHash；对历史记录用 force_rerun / force_rerun_agents 精确失效。`Input.ForceRerun`（`orchestrator.go:65`）是否有外部入口，待确认。）
- 期望答案要点：
  1. commit_sha 作键的理由，以及它无法覆盖的环境变量
  2. 复用条件：completed 且四元键命中
  3. 缓存键应包含所有影响输出的输入：模型、prompt、配置、锚点 PRIOR 集合
  4. 写入失败只意味着重跑成本，不影响正确性
- 危险信号：
  1. 说"checkpoint 保证结果确定"（LLM 输出本身不确定）
  2. 忽略 PRIOR_FINDINGS 不在键中，同一 SHA 在不同锚点下结论被混用
  3. 把 partial 当 completed 复用（代码已阻止，但需能说明）
- 证据等级：已验证（键与复用条件）；模型变更导致错误复用为代码推断

## Q03 条件分支、中断与门禁（Command / interrupt 的映射）
- 难度/题型：高 / 设计取舍
- 题面：本项目没有 conditional edge、Command 或 interrupt 机制。请说明 Agent 执行中，哪些情况由 Harness 决定中断、降级或保留结果：震荡、超时、零工具调用、引用未读文件。如果产品需要"开发者确认后再发布 review"的 interrupt，应放在哪一层？
- 代码锚点：`internal/harness/harness_impl.go:131 Executor.Execute`；`internal/harness/harness_impl.go:194` 零工具门禁；`internal/harness/harness_impl.go:202-220` 引用门禁；`internal/harness/oscillation.go:50 DetectOscillation`；`internal/orchestrator/orchestrator.go:779` Step 6 switch（A074, A077, A078, A094, A095, A059）
- 考察点：
  1. 状态映射的顺序与优先级
  2. 门禁"降级不删除"的设计
  3. partial 的语义与下游影响
  4. 人工确认 interrupt 的放置位置
- 追问链：
  1) 代码事实：`harness_impl.go:155` 调用 `agent.Review`；`:157` 结构校验；`:164-191` 按错误分支：ErrOscillation 且有 findings → partial，否则 failed；DeadlineExceeded → failed；无错误后：零工具 → partial（`:194-199`）；引用未读文件 → partial 并给 finding 打 UnverifiedCitation（`:202-220`）。
  2) 设计取舍：门禁降级而非丢弃，是为了避免漏报。代价是 partial 结果仍进入 PR 评论，仅在 summary 中以 `AgentWarnings`（`orchestrator.go:180`）提示。interrupt 应放在 Pipeline 层：在 `pipeline.go:247 review.PlanPublish` 之后、`pipeline.go:255` 提交之前，把发布计划持久化为"待批准"，批准后再提交，而不是在 Agent 内阻塞。
  3) 线上故障：一个 Agent 在 60 秒超时前已产生若干 findings，这些 findings 最终是否出现在评论中？（答：`agent.go:226-228` 在 executor 报错时返回 `nil` findings，Execute 拿到的 findings 为空。因此 spec 17.4 的"保留中断前 findings"在真实 Agent 路径上不成立，只有测试 mock 能返回 findings + error。这是需要修复的已验证问题。）
- 期望答案要点：
  1. 列出五种终止条件及其状态映射
  2. 门禁是"标记而非拦截"
  3. interrupt 缺失，应在 Pipeline 层持久化待发布计划并绑定 commit_sha
  4. 指出 17.4 与真实路径不一致，修复方式是 reviewAgent 在 error 时仍返回已解析的部分结果
- 危险信号：
  1. 认为 ErrOscillation 会保留 findings（真实路径下不成立）
  2. 把零工具门禁当作"证明 Agent 没有幻觉"
  3. 用 panic 或全局 flag 做中断
- 证据等级：已验证（代码路径）；真实路径 findings 丢失为已验证（静态），待运行确认

## Q04 异步工具、超时与子进程
- 难度/题型：中 / 故障排查
- 题面：`read_file` 在 Harness 中经历单次调用超时、只读工具重试与指数退避；Agent 整体 60 秒超时包住所有工具调用。请描述一次工具超时到 Agent 失败的完整传播路径。另外，golangci-lint 为什么需要进程内全局互斥与 10 秒重试循环？
- 代码锚点：`internal/harness/harness_impl.go:23-26` 默认值（defaultTimeout=60s，defaultToolTimeout=30s，defaultMaxRetries=2，defaultRetryBaseDelay=50ms）；`internal/harness/harness_impl.go:287 governedTool.Call`；`:351 governedTool.callOnce`；`internal/analysis/golangci.go:23 golangciRunMu`；`internal/analysis/golangci.go:113-129` 重试循环；`internal/analysis/pipeline.go:228 Pipeline.runJob`（A069–A073, A081–A083, A166, A171, A172）
- 考察点：
  1. context 是取消传播的唯一通道
  2. 只读重试的幂等前提
  3. 子进程与 goroutine 的回收
  4. 全局互斥的吞吐代价
- 追问链：
  1) 代码事实：`callOnce` 用 `context.WithTimeout(ctx, 30s)`（`:351-357`）；重试循环在 ctx 取消或 ErrOscillation 时停止（`:320`）；`sleepContext`（`:362`）等待退避。
  2) 设计取舍：只对只读工具重试，因为 read_file 幂等，写操作不幂等。golangci-lint 同一时刻只允许一个实例，并发 PR 审查会互相失败，所以进程内串行化，并对跨进程冲突退避重试。代价是整个进程的 lint 串行，吞吐受限。
  3) 线上故障：semgrep 子进程挂起时，`runJob` 在 per-file 超时后返回，但 `pipeline.go:237` 的子 goroutine 不会被回收（代码注释已承认）。长期运行下 goroutine 与子进程如何累积？如何在不改接口的情况下检测？
- 期望答案要点：
  1. 超时层次：工具 30s < Agent 60s < webhook 处理 10 分钟
  2. 只读重试的幂等性是前提
  3. 子进程必须使用 CommandContext，并确认子进程树是否被杀死（待确认）
  4. 锁粒度可以下沉到 workspace 或 package
- 危险信号：
  1. 认为 context 超时会自动清理子进程树
  2. 对写工具也做重试
  3. 把超时值当作经验常量，不做观测
- 证据等级：已验证（默认值与调用链）；子进程残留为代码推断

## Q05 权限边界：工作区、符号链接与数据外泄
- 难度/题型：高 / 设计取舍
- 题面：LLM Agent 通过 `read_file` 读取 PR 工作区（由 `PrepareWorkspace` 克隆得到）。请说明 `read_file` 如何阻止路径穿越。再考虑一个 PR 作者提交了指向宿主机文件的符号链接，它能否被 read_file 读取？读取内容会流向哪些 sink（LLM prompt、PR 评论、dashboard）？dashboard、SSE、API 的访问控制现状如何？tenant 隔离是否存在？
- 代码锚点：`internal/ai/tools/read_file.go:22 resolvePath`（词法检查，`:26-41`）；`internal/ai/tools/read_file.go:78`（os.ReadFile）；`internal/vcs/workspace.go:38`（BasicAuth）；`cmd/server/main.go:141`（SSE 路由无鉴权）；`internal/handler/dashboard.go:104 Dashboard.Register`（无鉴权）；`internal/ai/agents/untrusted.go:21 WrapUntrusted`（A063, A117, A132, A134, A196, A240, A245）
- 考察点：
  1. 词法检查与物理路径（符号链接）的区别
  2. 不可信输入的 sink 分析，而不只是注入分析
  3. 最小权限：token 范围、工具面
  4. tenant 隔离（TODO）
- 追问链：
  1) 代码事实：`resolvePath` 拒绝绝对路径，并以 `filepath.Clean` + `filepath.Abs` 做前缀比较（`read_file.go:26-41`）。它没有调用 `filepath.EvalSymlinks`，因此不解析符号链接。`os.ReadFile`（`:78`）跟随符号链接。
  2) 设计取舍：读取内容进入 TASK_STATE（`agent.go:305 renderTaskState`，经 UNTRUSTED 包裹），并可能通过 finding 的 CodeSnippet 进入 PR 评论。UNTRUSTED 包裹防指令注入，但不防数据外泄。最小权限做法：拒绝符号链接、校验 realpath 前缀、工作区只读挂载或独立用户。
  3) 线上故障：攻击者提交的 PR 包含 `leak -> <宿主机敏感路径>` 的符号链接。Agent 调用 read_file("leak")，内容进入 snippet 并发布到 GitHub。同时 dashboard 与 `/api/stream-review?pr_id=` 无鉴权，能访问 8080 端口的人可读取全部 findings 与 SSE 事件。你会如何分级修复？（符号链接能否在目标平台创建，依赖 go-git 的 checkout 行为，待构造样本验证。）
- 期望答案要点：
  1. 词法检查与物理路径检查的区别，应使用 EvalSymlinks 后再比较前缀，或直接拒绝 symlink
  2. 数据外泄的 sink 分析：prompt、评论、dashboard
  3. 访问控制分层：dashboard/SSE/API 需要鉴权或仅内网；webhook 已有 HMAC
  4. 多租户时按 installation 隔离（TODO）
- 危险信号：
  1. 认为 UNTRUSTED 包裹等于安全边界
  2. 认为"工具只读"就没有风险（读取本身即风险）
  3. 把 GitHub token 视为无限权限，而非按 installation 最小授权
- 证据等级：已验证（resolvePath 与无鉴权路由）；符号链接利用为代码推断，待构造 PR 复现

## Q06 向量库与业务 DB 的关联设计（TODO）
- 难度/题型：中 / 设计取舍
- 题面：假设要让 Agent 检索"历史 review comment"与"团队规则（rule）"，引入向量索引。请设计向量记录与业务 DB 中 `reviews` / `findings` 的关联键（repo_full_name、pr_number、commit_sha、finding fingerprint）；写入与删除如何与 PR 关闭、force-push 对齐；查询时如何保证结果限定在同一 repo（以及 tenant，TODO）。当前代码没有向量库，请列出你会新增的表与索引。
- 代码锚点：`internal/db/migrations/001_create_tables.sql:5`（reviews）；`internal/db/migrations/001_create_tables.sql:19`（findings）；`internal/review/finding_fingerprint.go:18 FindingFingerprint`；`internal/db/migrations/008_add_finding_fingerprint.sql:4`；向量库：TODO（A263）
- 考察点：
  1. 业务键设计，指纹可作为跨存储外键
  2. 向量与 DB 的双写一致性
  3. 删除路径与过滤条件
  4. 向量只做召回，展示以 DB 为准
- 追问链：
  1) 代码事实：当前只有 reviews 与 findings 两张业务表；findings 已有 fingerprint 列与索引（008 迁移），可以作为向量记录的业务键。
  2) 设计取舍：向量库存 finding message 还是原始 comment？存 message 便于检索，但可能泄露代码片段；存指纹与元数据、正文回查 DB 更利于删除与合规。
  3) 线上故障：force-push 之后，旧 commit 的历史评论还能被召回吗？PR 删除或仓库迁移时如何清理索引？（TODO：当前无该流程。）
- 期望答案要点：
  1. 业务外键：repo_full_name + pr_number + fingerprint，commit_sha 用于时效过滤
  2. 双写采用 outbox 或异步对账，而非同步事务
  3. 删除路径必须存在，并按 repo 过滤
  4. 向量只做召回候选，最终内容回查 DB
- 危险信号：
  1. 把向量库当 source of truth
  2. 没有删除路径，导致已删除仓库的内容仍被召回
  3. 跨仓库召回
- 证据等级：TODO（未实现）；业务表结构：已验证

## Q07 混合检索与 rerank（TODO）
- 难度/题型：高 / 设计取舍
- 题面：当前 Agent 的"检索"是正则 search_code、函数定义抽取与调用点检索。如果要为 security Agent 补充"关键词 + 向量语义 + rerank"的混合检索，你会如何接入 `tools.Tool` 接口？rerank 的候选数与 token 预算如何与上下文压缩协调？哪些是 TODO？
- 代码锚点：`internal/ai/tools/search_code.go:47 SearchCodeTool.Call`；`internal/ai/tools/get_function.go:40 GetFunctionDefTool.Call`；`internal/context/callers.go:72 CallerRetriever.Callers`；`internal/context/monitor.go:74 ShouldCompress`；rerank：TODO（A263）
- 考察点：
  1. 工具接口复用，工具输出即 observation
  2. 候选上限与 rerank 成本
  3. 与双轨压缩的协调
  4. 检索结果作为不可信输入的边界
- 追问链：
  1) 代码事实：search_code 最多返回 20 条（spec v1.9 FR-55），输出经 `limitOutput`（`truncate.go:23`）截断。
  2) 设计取舍：rerank 放在工具内部，还是放在 Harness 外的预处理阶段？前者让 Agent 感知不到候选池，后者需要改变工具契约。工具调用次数目前没有总预算（spec v1.15 规划，未实现）。
  3) 线上故障：检索结果中包含攻击者注释，诱导 Agent 忽略规则。工具输出是否也在 UNTRUSTED 边界内？（待确认：`agent.go:305 renderTaskState` 只包裹了 diff、静态分析、PRIOR 与上下文，ReAct observation 的包裹方式需要核对。）
- 期望答案要点：
  1. 工具契约不变，内部实现可替换
  2. 候选上限与 token 预算联动
  3. rerank 延迟计入 Agent 超时（60 秒）
  4. 工具输出纳入不可信边界
- 危险信号：
  1. 没有候选上限，rerank 成本随仓库规模线性增长
  2. 检索结果直接进入 SYSTEM 段
- 证据等级：TODO（混合检索与 rerank 不存在）；工具限制：已验证

## Q08 Chunk：diff 切分与大 PR 的上下文预算
- 难度/题型：初 / 设计取舍
- 题面：一个 PR 有 40 个文件、3000 行改动，超过上下文预算。请说明系统如何组织 diff 输入（按文件与 hunk 结构化，`renderDiff` 整体渲染）；静态分析对超过 5000 行的文件如何处理；如果要按 hunk 切 chunk 审查，你会如何保留函数边界与跨 hunk 上下文？
- 代码锚点：`internal/ai/agents/agent.go:412 renderDiff`；`internal/analysis/pipeline.go:163`（maxFileLines 判断）；`internal/context/assembler.go:86 Assembler.Assemble`；`internal/context/assembler_test.go:64`（超限告警的测试）；`internal/orchestrator/orchestrator.go:553`（文件过滤）（A106, A148, A167）
- 考察点：
  1. 按文件、hunk 或函数切分的边界差异
  2. Pinned 块超限时的行为
  3. chunk 之间的去重
  4. 大文件跳过对覆盖率的影响
- 追问链：
  1) 代码事实：`renderDiff` 对全部 FileDiff 渲染；`Assemble` 对超限的 Pinned TASK_STATE 只追加 OverflowWarning，不压缩也不截断，diff 完整保留。
  2) 设计取舍：按文件切最简单但会切断函数；按 hunk 切粒度细但跨 hunk 上下文丢失；按函数切需要语言解析。chunk 结果的去重可以复用 Verifier 的 dedupeKey（file+line+severity，`verifier.go:94`）。
  3) 线上故障：超限的 Pinned diff 被完整发送给 LLM，导致 context length 错误，Agent 直接 failed（代码推断，待运行确认）。或者大 PR 只有部分文件得到审查，但结论仍显示 completed，因为门禁不检查覆盖率。
- 期望答案要点：
  1. 按 hunk 切分并保留函数边界
  2. 覆盖率（已审查文件数 / 总文件数）应作为结果字段
  3. 超限时显式告警，并标记未覆盖的文件
- 危险信号：
  1. 认为 OverflowWarning 等于已处理超限
  2. 对超限截断后仍报告 completed
- 证据等级：已验证（渲染与溢出逻辑）；context length 失败为代码推断

## Q09 Embedding 服务与语义去重（TODO）
- 难度/题型：中 / 设计取舍
- 题面：如果要为 finding message 计算 embedding，替代 `review.FindingFingerprint` 的字符串归一化去重，你会把 embedding 调用放在哪一层？如何与现有 LLM 客户端工厂隔离？embedding 服务失败时如何降级？缓存键是什么？
- 代码锚点：`internal/ai/llm.go:32 NewLLM`；`internal/review/finding_fingerprint.go:18 FindingFingerprint`；`internal/ai/agents/verifier.go:54 Verifier.Verify`（去重在 `:84-106`，键在 `:94`）；embedding 服务：TODO（A263）
- 考察点：
  1. 服务边界：embedding 与 chat 模型是否共用工厂
  2. 降级到指纹去重
  3. 缓存键：模型标识 + 文本哈希
  4. 去重误合并与漏合并的权衡
- 追问链：
  1) 代码事实：当前去重按 file + line + severity（`verifier.go:94`），语义去重不存在；指纹归一化见 `finding_fingerprint.go:28-36`（normalizePath、normalizeMessage）。
  2) 设计取舍：语义去重会把不同问题误合并，阈值需要用 eval 数据集校准（`cmd/eval`，`evaluator.go:145 ComputeMetrics`）。embedding 应是 best-effort，不应使整个 Run 失败。
  3) 线上故障：embedding 服务超时，Verify 阻塞到 Agent 级超时之外，整个 PR 失败。你如何设计超时与降级？
- 期望答案要点：
  1. embedding 作为 best-effort 增强，失败回退指纹
  2. 阈值必须用标注数据校准，并纳入 eval 报告
  3. 缓存键包含模型版本，否则模型切换后缓存失效不可见
- 危险信号：
  1. 把 embedding 结果当作确定性判定
  2. 没有超时，阻塞 Verify
- 证据等级：TODO（未实现）；去重键：已验证

## Q10 多存储一致性：DB、GitHub 与 commit status
- 难度/题型：高 / 故障排查
- 题面：一次审查要写三处：PostgreSQL（reviews、findings、checkpoint）、GitHub（review、线程 resolve、commit status）。请列出 Process 中的写入顺序；说明任一步失败或进程崩溃时各系统的状态；指出哪些不一致可以自愈，哪些需要人工介入。
- 代码锚点：`internal/orchestrator/pipeline.go:111 CreateReview`；`pipeline.go:209-227`（CreateFinding）；`pipeline.go:255 SubmitReviewPlan`；`pipeline.go:267 ResolveReviewThread`；`pipeline.go:274 UpdateCommitStatus`；`pipeline.go:281 UpdateReviewStatus`；`orchestrator.go:732 saveCheckpoint`（A027, A035, A037, A038, A040, A057, A194）
- 考察点：
  1. 无分布式事务的前提下，写入顺序决定故障形态
  2. 幂等键与指纹去重如何保证重放安全
  3. pending 的补偿
  4. GitHub 副作用不可回滚
- 追问链：
  1) 代码事实：findings 写库（`pipeline.go:209`）早于 GitHub 提交（`:255`）；commit status（`:274`）与 completed（`:281`）在提交之后；checkpoint 在每个 Agent 完成时写入（`orchestrator.go:732`）。
  2) 设计取舍：选择"最终一致 + 幂等重放"，而不是两阶段提交。GitHub 不参与事务，安全重放靠 `pipeline.go:134 ListReviewThreads`（在 Run 之前读取线程）加上 `PlanPublish` 的指纹分类。
  3) 线上故障：SubmitReviewPlan 成功后、UpdateReviewStatus 前进程被杀死。reviews 停在 pending，GitHub 已有评论。开发者超过 30 秒窗口后重投 webhook，会重复评论吗？commit status 会怎样？（答：线程已存在则判为 persisting，不重复评论；commit status 会被新结果覆盖。若第二次运行读取线程失败，`pipeline.go:135-138` 会降级为不去重，此时会重复。）
- 期望答案要点：
  1. 写入顺序与自愈矩阵
  2. 指纹去重是重放安全的关键
  3. pending 需要超时回收
  4. GitHub 失败不应回滚 DB
- 危险信号：
  1. 说"用事务保证一致"
  2. 忽视 GitHub 侧已产生的副作用
  3. 把 pending 当作"正在运行"
- 证据等级：代码推断（顺序已验证，自愈行为待运行确认）

## Q11 写放大：PostgreSQL 中的行更新与 JSONB 覆盖
- 难度/题型：中 / 设计取舍
- 题面：题库原设定为 Mongo 文档写放大。本项目使用 PostgreSQL。请统计一次成功审查对 `reviews` 的 UPDATE 次数、对 `findings` 的 INSERT 次数、对 `harness_checkpoints` 的 upsert 次数；说明 JSONB 整列覆盖的代价；若一个 PR 有 200 条 finding，写放大主要来自哪里？
- 代码锚点：`internal/orchestrator/pipeline.go:156 UpdateReviewAnchor`；`pipeline.go:184 UpdateReviewPreFindings`；`pipeline.go:192 UpdateReviewAgentStatus`；`pipeline.go:203 UpdateReviewAgentResults`；`pipeline.go:214 CreateFinding`；`pipeline.go:281 UpdateReviewStatus`；`internal/repository/checkpoint_repo.go:73`（A032, A035, A206, A210, A212, A213）
- 考察点：
  1. 行级更新与 TOAST
  2. JSONB 覆盖写的成本
  3. 逐条 INSERT 与批量写入
  4. 索引维护成本
- 追问链：
  1) 代码事实：成功路径上 reviews 最多 5 次 UPDATE（anchor、preFindings、agent_status、agent_results、最终 status），加 1 次 INSERT（CreateReview）；findings 在循环中逐条 INSERT（`pipeline.go:209-227`）；checkpoint 每个 Agent 一次 upsert。
  2) 设计取舍：逐条 INSERT 可以改为批量 VALUES 或 COPY。JSONB 整列重写会产生死元组与 TOAST 开销，preFindings 是静态分析的完整结果，体积可能较大。
  3) 线上故障：200 条 finding 带来 200+ 次数据库往返，findings 上的 review_id 与 fingerprint 索引在每次插入时都要维护。失败时，已写入的部分 findings 与未写入的部分会不一致（当前逐条写入，单条失败只记日志，`pipeline.go:225-226`）。
- 期望答案要点：
  1. 给出 UPDATE、INSERT 次数的精确计数
  2. 批量化、事务化的优化方向
  3. JSONB 拆列或拆表的时机
- 危险信号：
  1. 说"JSONB 比关系表高效"而不考虑整列覆盖
  2. 忽略逐条写入的部分失败
- 证据等级：已验证（调用次数）；性能数值待压测

## Q12 并发与锁：同 PR 的重复事件与并行审查
- 难度/题型：高 / 故障排查
- 题面：同一 PR 在 30 秒内收到两次 synchronize 且 head commit_sha 相同（GitHub 重投）；或者两次 push 产生不同的 head commit_sha 并几乎同时到达。请分析两种情况下 review 记录、checkpoint、锚点、commit status 的行为。系统中哪些地方有锁，哪些没有？
- 代码锚点：`internal/orchestrator/orchestrator.go:925 Orchestrator.checkDuplicate`；`orchestrator.go:35 duplicateWindow`；`orchestrator.go:248 lastSeen`；`internal/orchestrator/pipeline.go:79 Pipeline`（无锁字段）；`pipeline.go:142 LatestCompletedReview`；`pipeline.go:175 failReview`（A026, A043, A045, A046, A211）
- 考察点：
  1. 进程内去重与分布式去重
  2. per-PR 串行化
  3. 锚点读取竞态
  4. 重复事件产生的错误状态
- 追问链：
  1) 代码事实：`checkDuplicate` 在 `Run` 开始时执行（`orchestrator.go:512`），此时 Process 已完成 CreateReview 与 PrepareWorkspace（全量 clone）。重复事件返回 ErrDuplicatePR，`pipeline.go:175` 进入 failReview，把新 review 标为 failed，并写入 failure commit status。这是已验证的代码路径。
  2) 设计取舍：去重应放在入口（webhook 或队列），而不是 Run 内部，否则重复事件已经付出了 clone 与 GitHub API 的成本。进程内 map 在重启或多副本下失效。以 (repo, pr, head_sha, action) 为唯一键的队列（spec v1.16 规划，未实现）更合适。
  3) 线上故障：同一 head_sha 的第二次投递被判为重复，写入 failure commit status，覆盖了第一次成功审查设置的 success。开发者看到红色检查。另外，两个不同 head_sha 并行时，`LatestCompletedReview` 可能读到同一个旧锚点，导致增量范围重复或遗漏。如何修复？
- 期望答案要点：
  1. 去重应前置到入口
  2. per-PR 串行化：advisory lock 或按 PR 分区的队列
  3. 重复事件不应写入失败状态
  4. 锚点与 completed 状态应在同一事务内写入
- 危险信号：
  1. 认为 Go map 加 mutex 可以跨副本去重
  2. 认为 30 秒窗口能覆盖所有重投（GitHub 重投可能更晚）
- 证据等级：已验证（代码路径）；错误状态覆盖为已验证（静态），运行待确认

## Q13 监控与可观测性（初）
- 难度/题型：初 / 故障排查
- 题面：线上有反馈"PR 提交后一直没有评论"。你能从现有系统获得哪些观测信号？每个信号的字段与局限是什么？如果要为"审查时长 P95"与"Agent 失败率"建立指标，你会在哪里埋点？
- 代码锚点：`cmd/server/main.go:39`（zap.NewProduction）；`internal/orchestrator/pipeline.go:101`（日志字段 repo/pr/head_sha）；`internal/orchestrator/orchestrator.go:800`（review completed 日志）；`internal/handler/dashboard.go:114 handleAPIMetricsFeedback`；`cmd/server/main.go:51`（/healthz）（A011, A241）
- 考察点：
  1. 结构化日志的字段约定
  2. 业务指标与系统指标的区别
  3. trace 缺失对排障的影响
  4. 埋点位置
- 追问链：
  1) 代码事实：日志没有 trace_id，关联 webhook 与 Process 依赖 repo、pr、head_sha 字段。review_id 在 CreateReview 之后才产生。webhook 收到事件但 processor 为 nil 时只有 Info 日志。
  2) 设计取舍：没有 Prometheus 与 trace，只能用 `reviews` 表作为事实来源，查询 status 分布。代价是只能看到结束后的状态，无法看到"进行中"。
  3) 线上故障：Process 卡在 PrepareWorkspace 的 clone 阶段（全量 clone，无 depth），reviews 停在 pending，dashboard 无法区分"卡住"与"进行中"。如何设计 pending 超时告警？
- 期望答案要点：
  1. 约定日志字段：repo、pr、head_sha、review_id、stage
  2. 埋点：webhook 接收、clone、Run 各阶段、GitHub API、LLM 调用
  3. liveness 与 readiness 分开
- 危险信号：
  1. 只看 /healthz 就判定服务正常
  2. 用日志量代替指标
- 证据等级：已验证（现有信号）；埋点为设计建议

## Q14 降级矩阵：fail-open 与 fail-closed
- 难度/题型：高 / 设计取舍
- 题面：列出本项目每个外部依赖失败时的降级行为：静态分析工具、GitHub 线程读取、上下文构建、checkpoint 保存、Verifier 的 LLM 调用、GitHub 提交 review。哪些降级合理？哪些是反模式？请用代码证明。
- 代码锚点：`internal/orchestrator/orchestrator.go:630-636`（静态分析失败降级）；`internal/orchestrator/pipeline.go:134-138`（线程读取降级）；`orchestrator.go:657`（上下文降级）；`orchestrator.go:430-443`（checkpoint 保存降级）；`internal/ai/agents/verifier.go:112-116`（LLM 错误被返回）；`orchestrator.go:790-792`（Verify 错误使 Run 失败）（A029, A051, A052, A057, A128）
- 考察点：
  1. 降级矩阵与 fail-open、fail-closed 的按环节划分
  2. 错误是否向上传播
  3. 降级是否可观测
- 追问链：
  1) 代码事实：`verifier.go:112-116` 中 llmConfidence 的错误被直接返回，`orchestrator.go:790-792` 将其包装为 `verify findings` 错误，`pipeline.go:175` 调用 failReview，开发者看到 failure，而不是"未验证的发现"。
  2) 设计取舍：Verifier 是准确性守门员，失败时 fail-closed（不发布未验证发现）是合理的。但 LLM 限流导致整个 PR 失败就不合理，应降级为"未验证"标记，并在 summary 中告知。
  3) 线上故障：某 LLM 供应商故障 30 分钟，所有 Standard/Complex PR 显示红色 failure。如何在不关闭验证的前提下减小影响？
- 期望答案要点：
  1. 每个环节标注 fail-open 或 fail-closed
  2. 降级必须进入 warnings，并在 summary 中可见
  3. 验证器降级时，发现需带"未验证"标记
- 危险信号：
  1. 静默降级
  2. 所有错误都统一为 failed
- 证据等级：已验证（代码路径）；影响范围为代码推断

## Q15 审计：能否回答一条评论从哪里来
- 难度/题型：初 / 设计取舍
- 题面：开发者质疑"为什么这条评论说这行有 SQL 注入"。系统能否回答：哪个 commit_sha、哪个 Agent、哪个 prompt 版本、哪些工具调用、哪个 LLM 模型、哪个 finding 指纹产生了这条评论？哪些已持久化，哪些缺失？
- 代码锚点：`internal/db/migrations/001_create_tables.sql:19`（findings）；`internal/repository/review_repo.go:175`（agent_results，含 ExecutionLogs）；`internal/harness/types.go:44 ToolLog`（只存 ArgsHash）；`internal/ai/prompts/prompts.go:27 prompts.Hash`；`internal/repository/checkpoint_repo.go:14 Checkpoint`；`internal/orchestrator/pipeline.go:255`（SubmitReviewPlan 返回值被丢弃）（A087, A091, A143, A205）
- 考察点：
  1. 审计三要素：谁、何时、依据什么
  2. 哈希与原文的隐私权衡
  3. 保留与删除策略
- 追问链：
  1) 代码事实：ToolLog 只保存 ArgsHash，不保存参数原文，无法复现工具输入。findings 有 agent、message、severity、fingerprint，但没有 prompt_hash 与 model，需要通过 review_id 关联到 reviews 的 head_sha。`pipeline.go:255` 的返回值 event 被丢弃，GitHub review id 没有保存。
  2) 设计取舍：保存参数原文会泄露代码片段；只保存哈希则无法复现。折中方案是保存截断或加密的参数。
  3) 线上故障：合规要求删除某私有仓库的审查记录。CASCADE 会删除本地 findings，但 checkpoint（以 head_sha 为键）是否也要删除？GitHub 上的评论如何处理？
- 期望答案要点：
  1. 列出审计所需字段清单，并标出缺失项
  2. 哈希与原文的权衡
  3. 删除策略覆盖所有存储
- 危险信号：
  1. 把"有日志"等同于"可审计"
  2. 为了可审计而把完整代码片段长期保存在多处
- 证据等级：已验证（字段清单）；合规删除路径待确认

## Q16 多 Agent：并行、隔离与结论合并
- 难度/题型：初 / 设计取舍
- 题面：四个 Agent（security、logic、performance、lint）并行审查同一个 PR，互不通信。它们的结论如何合并？如果 security 与 logic 对同一行报告了不同 severity 的问题，最终评论如何呈现？Trivial PR 为什么只启用两个 Agent？
- 代码锚点：`internal/orchestrator/orchestrator.go:698`（errgroup.Group）；`internal/orchestrator/orchestrator.go:750-752`（失败不影响组）；`internal/ai/agents/verifier.go:44 dedupeKey`；`verifier.go:94`；`internal/review/strategy.go:40 Selector.Select`；`internal/ai/agents/security.go:18`、`logic.go:18`、`performance.go:18`、`lint.go:19`（A053, A098, A110–A113, A178）
- 考察点：
  1. 独立性与并行性的关系
  2. 合并策略与 dedupeKey 粒度
  3. 成本与策略的关系
- 追问链：
  1) 代码事实：`orchestrator.go:698` 使用 `new(errgroup.Group)`，不是 WithContext，一个 Agent 出错不会取消其他 Agent。Agent 错误在闭包中被记为 failed 并返回 nil（`:750-752`）。
  2) 设计取舍：没有 Agent 间通信，就无法实现"安全 Agent 证实逻辑 Agent 的发现"。替代方案是 Verifier 的去重与 boost（`verifier.go:110`，每多一个副本 +0.05）。dedupeKey 包含 severity，因此同一行不同 severity 的两条会各自保留。
  3) 线上故障：同一行两条不同 severity 的评论同时出现，开发者认为重复。如何设计合并规则（按 file + line + 类别归并，保留最高 severity）？
- 期望答案要点：
  1. 独立性换取可并行与可测试
  2. 合并键要区分"位置"与"类别"
  3. Trivial 只跑 security 与 lint 是成本取舍
- 危险信号：
  1. 认为 Agent 越多结论越好，不考虑误报放大
  2. 让 Agent 直接互相读取结论，导致回声
- 证据等级：已验证

## Q17 幻觉校验：存在性不等于正确性
- 难度/题型：高 / 设计取舍
- 题面：Agent 报告"auth/login.go:42 存在 SQL 注入，片段为 `db.Query(...)`"。请描述 Verifier 如何判断它是否为幻觉：行号是否在 diff 内，片段是否在源文件中，LLM 置信度如何混合。没有 CodeSnippet 时会发生什么？这套校验能否阻止"引用了真实存在的代码但结论错误"的 finding？
- 代码锚点：`internal/ai/agents/verifier.go:187 lineInDiff`；`verifier.go:130 Verifier.verifySnippet`（空片段放行在 `:132-135`，读取失败在 `:138`）；`verifier.go:157 llmConfidence`；`verifier.go:110-118`（置信度混合）；`internal/ai/agents/validate.go:58 StructuralValidator.Validate`（A122, A126–A129）
- 考察点：
  1. 三层校验的职责分工
  2. 验证"存在"与验证"成立"的区别
  3. 空片段放行的取舍
  4. 置信度混合公式的可解释性
- 追问链：
  1) 代码事实：空片段直接放行（`:132-135`）。非空片段先做整体包含匹配（+0.1），再逐行匹配（+0.05），都不命中则判幻觉（`:141-152`）。文件读取失败也判幻觉（`:138`）。最终分数为 `(score + llmScore) / 2`（`:117`）。
  2) 设计取舍：字符串包含匹配对空白和重命名敏感，但成本低、可解释。AST 匹配更准确，但依赖语言支持。空片段放行是为了不惩罚未给片段的 Agent，代价是"无片段的幻觉"只能靠行号校验拦截。
  3) 线上故障：攻击者在 PR 注释中写入指令，诱导 Agent 引用真实存在的一段代码并给出错误结论。Verifier 会放行，因为片段存在。应在 Verifier 之外补哪些防线？（答：UNTRUSTED 边界、结论一致性检查、注入回归样本，见 `eval/dataset/injection-*`。）
- 期望答案要点：
  1. 存在性校验只能拦截编造，拦不住误判
  2. 多层防线：结构、存在、语义、注入
  3. 置信度公式需要 eval 数据校准
- 危险信号：
  1. 认为 Verifier 等于"正确性保证"
  2. 对空片段给高置信度
- 证据等级：已验证

## Q18 JSON 解析：容错边界与截断
- 难度/题型：中 / 故障排查
- 题面：Agent 的 Final Answer 应为 JSON 数组。`parseFindings` 如何容忍代码围栏与前后文字？如果 message 中含有 `[` 字符，或者输出在 16384 token 处被截断，会发生什么？解析失败时，该 Agent 的状态如何？
- 代码锚点：`internal/ai/agents/agent.go:443 parseFindings`（`strings.Index` 在 `:451`，Decode 在 `:457`）；`agent.go:41 maxCompletionTokens`；`agent.go:226-228`（executor 错误返回）；`internal/harness/harness_impl.go:187-189`（default 分支 failed）（A101, A104）
- 考察点：
  1. 容错解析的边界
  2. 截断与部分输出
  3. 失败模式对 Agent 状态的影响
  4. schema 校验的位置
- 追问链：
  1) 代码事实：parseFindings 从第一个 `[` 开始用 json.Decoder 解码一个数组（`:451-459`）。前置文字中若含 `[`，解码失败，返回错误，Harness 标记 failed，整个 Agent 结果丢弃。
  2) 设计取舍：结构化输出（JSON mode 或 function calling）比正则式容错更可靠，但依赖 provider 能力。当前"解析失败即 Agent 失败"比"部分解析"保守。
  3) 线上故障：模型输出在第 37 条 finding 处被截断，Decode 失败，前 36 条全部丢失。Agent 显示 failed，评论中警告"安全审查未完成"。如何在不引入错误 finding 的前提下让部分解析成功？（答：先用 provider 的 finish_reason 判断截断，再对已闭合的对象做逐条解析。）
- 期望答案要点：
  1. 结构化输出优先
  2. 解析失败的粒度应当是单条而非整体
  3. 截断需要显式检测
- 危险信号：
  1. 用正则从自然语言中抠 JSON
  2. 忽略 max_tokens 截断
- 证据等级：已验证（解析逻辑）；截断场景为代码推断

## Q19 第三方 API：GitHub 与 LLM
- 难度/题型：高 / 故障排查
- 题面：GitHub 与 LLM 是两个主要外部依赖。请分别说明：限流（429，以及 403 的 secondary rate limit）、Compare API 的 404 与 422、GraphQL 线程查询的分页、LLM 超时与 429 的处理。哪些已实现，哪些缺失？
- 代码锚点：`internal/github/client.go:23 NewClient`（无重试或限流配置）；`internal/github/client.go:74 Client.GetCompareDiff`（404、422、413 映射）；`internal/github/threads.go:16 reviewThreadsQuery`（`first:100`，见 `:19`）；`internal/github/threads.go:74 ListReviewThreads`；`internal/ai/llm.go:32 NewLLM`（无 retry 选项）；`internal/harness/harness_impl.go:23`（defaultTimeout=60s）（A184, A186, A188, A189, A130）
- 考察点：
  1. 重试与退避策略
  2. 幂等性
  3. 分页与截断的正确性
  4. 错误分类与降级语义
- 追问链：
  1) 代码事实：GetCompareDiff 将 404 映射为 ErrCompareBaseUnreachable，422 与 413 映射为 ErrCompareLimit，两者都回退全量（`pipeline.go:306`）。ListReviewThreads 只请求第一页（reviewThreads 与 comments 都是 first:100），超过 100 条线程时后续线程不可见。
  2) 设计取舍：超限回退全量会在大 PR 上让 API 与 LLM 成本翻倍。线程截断会让 PlanPublish 把已存在的线程当作 new，产生重复评论。应分页，并在达到上限时告警。
  3) 线上故障：某仓库有 150 条历史线程，第 101 条之后的 persisting 发现被当作 new 重复评论。同时 GitHub 对该 token 返回 secondary rate limit，GetPRDiff 直接失败，review 标为 failed。如何设计重试、分页与降级？
- 期望答案要点：
  1. 分页是正确性问题，不只是性能问题
  2. 限流需要指数退避并遵守 Retry-After
  3. 错误分类决定是重试、回退还是失败
- 危险信号：
  1. 对所有错误统一重试
  2. 认为 first:100 足够
- 证据等级：已验证（缺分页与错误映射）；限流行为待确认

## Q20 故障复盘：假如线上发生最严重的事故（TODO，请替换为真实事故）
- 难度/题型：高 / 故障排查
- 题面：【TODO：请用真实线上事故替换本题。以下为从代码风险点构造的复盘模板。】假设线上发生一次严重事故：一段时间内，所有 Standard 与 Complex 的 PR 都收到 LGTM 评论，commit status 显示 success，但其中存在明显的安全问题。请按"时间线、影响面、根因、检测缺口、修复、防回归"六部分复盘，并指出你会优先查看的三个代码位置。
- 代码锚点：`cmd/server/main.go:104-109`（verifierFactory 仅在 enabled 时构造）；`internal/config/config.go:171 Config.ValidateVerifier`（enabled 为 false 时不校验）；`internal/config/config.go:209`（默认 true）；`internal/orchestrator/orchestrator.go:779-795`（Verifier 分支）；`internal/orchestrator/pipeline.go:247-256`（LGTM 判定）（A005, A006, A059, A060, A232, A234）
- 考察点：
  1. 复盘结构与从症状到代码路径的推导
  2. 默认值与配置开关的风险
  3. 缺少回归测试的检测缺口
- 追问链：
  1) 代码事实：`verifier.enabled=false` 时，`cmd/server/main.go:105` 不构造工厂，verifierFactory 为 nil。Standard 策略启用了 Verifier，因此 Step 6 进入 default 分支，verifier 为 nil，`orchestrator.go:785-794` 的条件不成立，`res.Findings` 从未被赋值，所有 finding 被静默丢弃。Pipeline 得到空计划，`pipeline.go:251-253` 在无线程时走 LGTM 分支，commit status 写为 success。
  2) 设计取舍：关闭验证以节省 LLM 成本是合理动机，但"关闭验证"的语义应该是"跳过验证、保留发现"，而不是"丢弃发现"。这里选择的失败方向是错的，应当 fail-open 到 acceptAsVerified。
  3) 线上故障：此问题在 orchestrator 测试中没有断言（测试中 `newTestOrchestrator(t, agts, nil)` 传入 nil，但未断言 findings 保留。该结论待运行确认）。如何增加回归测试，以及启动期校验（verifier 关闭时强制走 acceptAsVerified 路径）？
- 期望答案要点：
  1. 时间线、影响面的量化方法
  2. 根因：Verifier 缺失分支丢弃 findings；配置开关语义错误
  3. 检测缺口：测试只覆盖有 Verifier 的路径
  4. 修复与防回归：分支改为 acceptAsVerified，增加表驱动测试，启动日志声明验证状态
- 危险信号：
  1. 复盘只写"LLM 故障"而不追到代码路径
  2. 修复只改配置，不改代码
- 证据等级：TODO（复盘模板）；代码路径：已验证（静态）；运行复现：待确认

## Q21 LangGraph 与手写工作流的取舍（初）
- 难度/题型：初 / 设计取舍
- 题面：题库假设本项目使用 LangGraph。实际实现是 Go 手写 workflow（顺序步骤 + errgroup），加上 langchaingo 的 ReAct 执行器。请比较两种方式的可测试性、状态持久化、人工介入（interrupt）、并发控制。如果产品要求"审查结果发布前需要开发者在 GitHub 上批准"，你会选哪种？
- 代码锚点：`internal/orchestrator/orchestrator.go:511 Orchestrator.Run`；`orchestrator.go:698`（errgroup.Group）；`internal/ai/agents/agent.go:218`（agents.NewExecutor，langchaingo ReAct）；`internal/harness/types.go:74 Harness`；`go.mod:12`（langchaingo，无 langgraph 依赖）（A042, A053, A092）
- 考察点：
  1. 显式流程与图结构的区别
  2. 可测试性与框架绑定
  3. 中断与恢复
- 追问链：
  1) 代码事实：流程是普通 Go 函数调用，中间状态是局部变量。checkpoint 只保存 Agent 粒度的结果，没有节点级恢复。
  2) 设计取舍：显式代码易读、易测、没有框架锁定，但 interrupt 与节点恢复要自己实现（例如持久化待发布计划）。图框架原生支持这些，代价是学习成本和运行时依赖。
  3) 线上故障：审查在 interrupt 点等待三天，期间 PR 被 force-push，待发布计划是否仍有效？需要把计划绑定到 commit_sha，并在恢复时校验。
- 期望答案要点：
  1. 承认流程由代码显式表达，而非图
  2. interrupt 需要持久化状态与 commit_sha 绑定
  3. 框架选择应基于需求而非流行度
- 危险信号：
  1. 为了"用框架"而引入框架
  2. 没有想清楚恢复时的幂等性
- 证据等级：已验证（无 LangGraph 依赖与流程形态）

## Q22 长期记忆：跨 PR 的经验与隔离
- 难度/题型：中 / 设计取舍
- 题面：本项目在不同 PR 之间有"记忆"吗？当前唯一的跨回合记忆是什么（锚点 reviews 与 PRIOR_FINDINGS）？如果要让 Agent 记住"团队曾驳回过的某类 finding"（例如某条 rule 的误报），应放在哪里？repo 与 tenant（TODO）的隔离如何保证？
- 代码锚点：`internal/orchestrator/pipeline.go:142 LatestCompletedReview`；`pipeline.go:337 loadPriorFindings`（跳过 accepted_implicit 与人工已 resolve 的线程）；`internal/ai/agents/agent.go:305 renderTaskState`（PRIOR 段）；`internal/context/assembler.go:20 SourceMemory`（定义存在）；`internal/ai/agents/agent.go:292-295`（只写 SYSTEM 与 TASK_STATE）（A030, A033, A103, A148）
- 考察点：
  1. 短期记忆（上一轮结论）与长期记忆（经验）的区别
  2. 记忆的写入者与失效机制
  3. repo 与 tenant 隔离
  4. 记忆作为注入源的风险
- 追问链：
  1) 代码事实：PRIOR 来自最近一次 completed 审查的 findings，是结构化记录，不是语义记忆。MEMORY 块类型已定义，但 prepareQuestion 从不写入（`agent.go:292-295`）。
  2) 设计取舍：驳回记忆应是结构化规则（repo + rule_id + 路径模式 + 状态），而不是自然语言塞进 prompt。写入者应是人工操作，当前未发现人工驳回入口（待确认）。
  3) 线上故障：某团队的驳回记忆被滥用，导致真实漏报。如何审计与撤销？多组织场景需要 installation 级隔离（TODO）。
- 期望答案要点：
  1. 结构化记忆优于自然语言记忆
  2. 写入权限与审计
  3. 失效条件：规则变更、代码变更
- 危险信号：
  1. 把所有历史 comment 直接塞进 prompt
  2. 记忆没有 repo 维度
- 证据等级：已验证（无 MEMORY 写入）；记忆设计为设计建议

## Q23 降本：LLM 调用放大与取舍
- 难度/题型：中 / 设计取舍
- 题面：一次 Standard 级 PR 审查的 LLM 调用次数由哪些因素决定？请列出调用来源，并说明每个降本手段（策略、增量审查、checkpoint 复用、Trivial 跳过 Verifier、迭代上限、max_tokens）各自节省什么、可能牺牲什么。
- 代码锚点：`internal/review/strategy.go:40 Selector.Select`；`internal/ai/agents/agent.go:34 maxAgentIterations`（值为 8）；`agent.go:41 maxCompletionTokens`（值为 16384）；`internal/ai/agents/verifier.go:109-118`（每条去重后的 finding 一次 llmConfidence）；`internal/orchestrator/pipeline.go:306 resolveDiff`；`orchestrator.go:341 loadCheckpoints`；`internal/config/config.go:210`（trivial_verifier 默认 false）（A049, A100, A101, A126, A178, A208）
- 考察点：
  1. 成本模型：调用次数 = Agent 数 × 轮数 + finding 数
  2. 降本手段与质量的权衡
  3. 上线前如何评估成本
- 追问链：
  1) 代码事实：每个启用的 Agent 最多 8 轮 ReAct，每轮一次 LLM 调用。Verifier 对每条去重后的 finding 调用一次 LLM，因此 finding 越多成本越高。没有 checkpoint 时，4 个 Agent 全部运行。
  2) 设计取舍：Verifier 的逐条调用可以改为一次批量调用，代价是单次输出变长、解析风险上升（见 Q18）。Trivial 跳过 Verifier 节省成本，但发现不再经过对抗验证。
  3) 线上故障：某版本把 maxAgentIterations 从 8 调到 20，成本翻倍，且 Agent 超时率因 60 秒限制而上升。如何在上线前用 eval 数据集评估成本与质量？
- 期望答案要点：
  1. 成本的来源与放大因子
  2. 每个降本手段的代价
  3. 成本与质量在 eval 中联合评估
- 危险信号：
  1. 只看单次调用价格，不看调用次数
  2. 降本时不设质量回归门槛
- 证据等级：已验证（调用来源）；成本数值待用量数据确认

## Q24 幂等与重放：webhook 重投与重复审查
- 难度/题型：高 / 故障排查
- 题面：GitHub 会对 webhook 投递失败进行重投。请说明本项目对"同一个 pull_request 事件被重复投递"的幂等处理：HMAC 校验后、Process 之前、Run 之内、checkpoint 与 PlanPublish 各自做了什么？如果重投在 30 秒窗口之外到达，哪些成本会重复发生，哪些不会？
- 代码锚点：`internal/handler/webhook.go:81 Webhook.ServeHTTP`（不读取投递 ID）；`webhook.go:141`（异步 goroutine）；`internal/orchestrator/orchestrator.go:925 checkDuplicate`；`internal/orchestrator/pipeline.go:111 CreateReview`；`internal/repository/checkpoint_repo.go:73`；`internal/review/publish_plan.go:57 PlanPublish`（A017, A020, A027, A043, A045, A207）
- 考察点：
  1. 幂等的层次划分
  2. 投递 ID 的作用
  3. 窗口化去重的局限
  4. 成本与正确性的取舍
- 追问链：
  1) 代码事实：全仓库没有读取 X-GitHub-Delivery。去重只依赖 head_sha 的 30 秒内存窗口。每次投递都会 CreateReview 新建一行。
  2) 设计取舍：以投递 ID 去重能区分"GitHub 重投"与"开发者真实重复 push"，但投递 ID 无法覆盖"同一 SHA 的合理重跑"。以 (repo, pr, head_sha, action) 为键的 review_jobs（spec v1.16，未实现）更贴近业务。
  3) 线上故障：重投在 2 分钟后到达。checkpoint 命中，LLM 不重跑，但静态分析、Verifier 与 clone 全部重跑，reviews 表出现两条记录，dashboard 显示两次审查。如何收敛？
- 期望答案要点：
  1. 幂等分三层：入口（投递 ID 或业务键）、执行（锁或队列）、结果（checkpoint 与指纹）
  2. 窗口化内存去重在重启或多副本下失效
  3. 重复记录需要归并或标记
- 危险信号：
  1. 认为 checkpoint 能覆盖所有重复成本
  2. 把 30 秒窗口当成幂等保证
- 证据等级：已验证（无投递 ID 读取、窗口逻辑）；重投行为待确认（需 GitHub 重投样本）


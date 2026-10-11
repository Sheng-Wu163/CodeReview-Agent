# CodeReviewAgent - 产品需求规格

## 1. Overview

CodeReviewAgent 是一个自托管的、多 Agent 协作的 GitHub PR 代码审查系统。它通过 GitHub Webhook 接收 PR 事件，构建代码上下文，调度多个专业 Agent 进行并行审查，经由验证 Agent 过滤后，将结构化审查意见回写到 PR。

## 2. Why

### 2.1 业务背景

代码审查是研发流程中最耗时、最依赖经验的环节。资深工程师时间有限，无法逐行审查每个 PR。现有 AI 审查工具多为单次 LLM 调用，缺乏上下文收集能力，且误报率高，导致开发者信任度低。

### 2.2 学术依据

IEEE 2026 年的一项研究提出了四 Agent 代码审查框架（安全专家、性能优化器、元认知批评者、首席架构师），通过对抗性自验证，将误报率降低 40%，行号准确率从 67% 提升到 92%，幻觉率从 32% 降到 18%。本项目的 Agent 架构直接参考该论文的设计。

### 2.3 目标

- 为应届生秋招全栈 Agent 岗位提供一个完整的、可演示的工程作品
- 展示 Go 语言 + LangChainGo + 多 Agent 编排 + 静态分析集成 + GitHub 平台集成的综合能力

## 3. Scope

### 3.1 In Scope

- GitHub Webhook 接收与 HMAC 签名验证
- PR diff 解析与变更分类
- 四个专业 Agent 并行审查：安全、逻辑/质量、性能、规范
- 验证 Agent 对抗性审查（事实核查 + 去重 + 幻觉过滤）
- 审查结果以 GitHub Review 形式回写（行级评论）
- 置信度分级与严重度排序
- 审查历史持久化（PostgreSQL）
- 简单的 Web 仪表盘（审查历史列表 + 详情）
- Docker Compose 一键启动

### 3.2 Out of Scope

- 用户注册/登录系统（使用 GitHub App 授权即可）
- 多仓库的权限管理（第一版假设单用户自托管）
- Slack/邮件通知集成
- VS Code 插件
- RAG 代码库检索（第二版考虑）
- 支持除 GitHub 之外的代码托管平台
- 自动修复代码（只做审查，不做修改）
- Webhook 断点续跑与任务持久化队列（第一版用内存队列）

## 4. Impact Analysis

### 4.1 影响模块

本项目为全新项目，无已有模块依赖。

### 4.2 外部依赖

| 依赖 | 用途 | 风险 |
|------|------|------|
| GitHub API | Webhook 接收、PR 内容获取、Review 回写 | 需要 GitHub App 配置 |
| LLM API（OpenAI/Anthropic） | Agent 推理 | 需要 API Key，产生费用 |
| Semgrep CLI | 静态安全分析 | 需要安装 Semgrep |
| golangci-lint | Go 代码静态分析 | 需要安装 |

## 5. Requirements

### 5.1 功能需求

**FR-01: Webhook 接收与验证**
系统必须提供一个 HTTP 端点接收 GitHub Webhook 事件，并使用 HMAC-SHA256 验证请求签名（X-Hub-Signature-256 头）。验证失败时返回 401。

**FR-02: PR 事件处理**
系统必须支持 `pull_request` 事件的 `opened` 和 `synchronize` 动作。收到事件后，提取 PR 编号、仓库全名、head commit SHA，并触发审查流程。

**FR-03: Diff 解析**
系统必须解析 PR 的 diff，提取文件级和代码块级的变更信息，包括：文件路径、变更类型（新增/修改/删除）、变更行号范围、变更前后的代码内容。

**FR-04: 文件过滤**
系统必须在审查前过滤掉以下文件：自动生成文件（如 `*_generated.go`、`*.pb.go`）、vendor 目录、第三方依赖目录、文档文件（`*.md`）、锁文件（`go.sum`、`package-lock.json`）。

**FR-05: 安全审查 Agent**
安全 Agent 必须结合 Semgrep 静态分析和 LLM 语义推理，检测以下类型的漏洞：SQL 注入、命令注入、硬编码密钥/凭证、不安全的加密使用、路径遍历、SSRF。

**FR-06: 逻辑/质量审查 Agent**
逻辑 Agent 必须审查以下问题：空指针解引用、边界条件遗漏（off-by-one）、未处理的错误返回值、并发竞争条件（goroutine 泄漏、data race）、资源泄漏（未关闭的文件/连接）。

**FR-07: 性能审查 Agent**
性能 Agent 必须检测：N+1 数据库查询模式、不必要的内存分配（在热路径中）、低效的字符串拼接（循环中使用 `+`）、缺少索引提示的查询模式。

**FR-08: 规范审查 Agent**
规范 Agent 必须结合 golangci-lint 的输出，检查：命名规范、错误处理规范、注释完整性、导入分组规范。

**FR-09: 并行执行**
四个审查 Agent 必须并行执行（使用 Go goroutine），任一 Agent 的失败不得阻塞其他 Agent。

**FR-10: 验证 Agent**
验证 Agent 必须对所有审查 Agent 的发现执行以下操作：
- 行号验证：检查引用的行号是否在 diff 范围内
- 幻觉过滤：验证引用的代码片段是否真实存在于源文件中
- 去重：合并多个 Agent 报告的同一问题
- 置信度评估：为每条发现输出 0.0-1.0 的置信度分数

**FR-11: 上下文收集（ReAct Loop）**
每个审查 Agent 必须能够通过工具调用来收集额外上下文，包括：
- `read_file(path, start_line, end_line)`：读取指定文件的代码
- `search_code(pattern, file_glob)`：在代码库中搜索模式
- `get_function_definition(function_name)`：获取函数定义

**FR-12: GitHub Review 回写**
系统必须将最终审查结果以 GitHub Pull Request Review 的形式提交，包含：
- 总体评论（Summary）
- 行级评论（Inline Comments），每条包含：问题描述、严重度、置信度、修复建议
- Commit Status 更新（当存在 Critical 级别问题时，设置为 failure）

**FR-13: 严重度分级**
每条发现必须被标记为以下级别之一：
- `critical`：安全漏洞、数据丢失风险
- `warning`：逻辑错误、性能问题
- `suggestion`：规范建议、风格改进

**FR-14: 审查历史持久化**
系统必须将每次审查的完整记录（PR 信息、Agent 发现、验证结果、最终评论）存储到 PostgreSQL 数据库中。

**FR-15: Web 仪表盘**
系统必须提供一个简单的 Web 页面，展示：
- 审查历史列表（按时间倒序，显示 PR 标题、仓库名、发现数量、状态）
- 审查详情页（展示每个 Agent 的原始发现和验证后的最终发现）

### 5.2 非功能需求

**NFR-01: 性能**
单个 PR 的审查必须在 5 分钟内完成（对于变更量 < 500 行的 PR）。

**NFR-02: 可靠性**
任一审查 Agent 的失败（超时、API 错误）不得导致整个审查流程失败。系统应记录失败原因，并继续执行其他 Agent。

**NFR-03: 安全性**
- GitHub Webhook Secret 必须通过环境变量注入
- LLM API Key 必须通过环境变量注入
- 不将任何密钥硬编码在代码中

**NFR-04: 可观测性**
系统必须输出结构化日志（JSON 格式），包含：请求 ID、PR 编号、每个 Agent 的执行时间、发现的严重度分布。

**NFR-05: 可测试性**
每个 Agent 必须提供接口，使得可以使用 mock LLM 进行单元测试。

## 6. Acceptance Criteria

### AC-01: Webhook 签名验证
- Given GitHub Webhook 请求携带有效的 HMAC-SHA256 签名
- When 系统收到 POST /webhook 请求
- Then 请求被接受并返回 200

### AC-02: Webhook 签名拒绝
- Given GitHub Webhook 请求携带无效的签名或缺少签名头
- When 系统收到 POST /webhook 请求
- Then 请求被拒绝并返回 401

### AC-03: PR 事件解析
- Given 系统收到 `pull_request` 事件的 `opened` 动作
- When 事件被处理
- Then 提取 PR 编号、仓库全名、head SHA，并触发审查流程

### AC-04: Diff 解析正确性
- Given PR 包含 3 个文件的变更
- When Diff 被解析
- Then 系统识别出每个文件的变更类型和变更行号范围

### AC-05: 文件过滤
- Given PR 包含 `vendor/github.com/foo/bar.go` 和 `main.go` 两个文件
- When 文件过滤执行
- Then 只有 `main.go` 进入审查队列

### AC-06: 安全 Agent 检测 SQL 注入
- Given 变更中包含 `db.Query("SELECT * FROM users WHERE id = " + userID)` 字符串拼接
- When 安全 Agent 执行审查
- Then 系统报告一条 `critical` 级别的 SQL 注入发现

### AC-07: 逻辑 Agent 检测未处理错误
- Given 变更中包含 `json.Unmarshal(data, &result)` 且返回值被忽略
- When 逻辑 Agent 执行审查
- Then 系统报告一条 `warning` 级别的未处理错误发现

### AC-08: 验证 Agent 过滤幻觉
- Given 安全 Agent 报告了一条发现，但引用的代码片段在源文件中不存在
- When 验证 Agent 执行审查
- Then 该条发现被过滤，不进入最终输出

### AC-09: 验证 Agent 去重
- Given 安全 Agent 和逻辑 Agent 都报告了同一处未处理错误
- When 验证 Agent 执行去重
- Then 最终输出中只保留一条该发现

### AC-10: GitHub Review 回写
- Given 审查完成且存在 2 条 `warning` 和 1 条 `suggestion`
- When Review 回写执行
- Then GitHub PR 上出现一条 Review，包含总体评论和 3 条行级评论

### AC-11: Commit Status 更新
- Given 审查完成且存在至少 1 条 `critical` 发现
- When Commit Status 更新执行
- Then PR 的 commit status 被设置为 `failure`

### AC-12: 审查历史持久化
- Given 一次审查完成
- When 审查记录写入数据库
- Then 可以通过 API 查询到该次审查的完整记录

### AC-13: Web 仪表盘展示
- Given 系统已执行了 3 次审查
- When 访问 `/dashboard`
- Then 页面展示 3 条审查记录，按时间倒序排列

## 7. ADDED / MODIFIED / REMOVED Requirements

本版本为初始版本，全部为 ADDED。

## 8. Edge Cases

| 场景 | 期望行为 |
|------|---------|
| PR diff 为空（仅修改了注释） | 跳过审查，记录日志 "No meaningful changes" |
| LLM API 超时（> 60s） | 该 Agent 标记为失败，其他 Agent 继续执行 |
| Semgrep 未安装 | 安全 Agent 降级为纯 LLM 审查，记录警告日志 |
| PR 包含二进制文件 | 跳过该文件，记录日志 |
| 同一 PR 在 30 秒内被重复触发 | 使用 PR head SHA 去重，第二次触发被忽略 |
| 所有 Agent 均未发现任何问题 | 提交一条 Review 评论 "LGTM ✅"，commit status 设为 success |

## 9. Dependencies

### 9.1 Go 依赖

| 依赖 | 版本 | 用途 |
|------|------|------|
| github.com/tmc/langchaingo | latest | LLM Agent 框架 |
| github.com/go-git/go-git/v5 | v5.12+ | Diff 解析 |
| github.com/google/go-github/v66 | v66+ | GitHub API 客户端 |
| github.com/parsiya/semgrep_go | latest | Semgrep CLI 集成 |
| github.com/jackc/pgx/v5 | v5+ | PostgreSQL 驱动 |
| github.com/go-chi/chi/v5 | v5+ | HTTP 路由 |
| github.com/spf13/viper | latest | 配置管理 |
| go.uber.org/zap | latest | 结构化日志 |

### 9.2 外部工具

| 工具 | 用途 | 是否必须 |
|------|------|---------|
| Semgrep CLI | 安全静态分析 | 否（降级时不可用） |
| golangci-lint | Go 代码规范检查 | 否（降级时不可用） |

### 9.3 基础设施

| 组件 | 用途 |
|------|------|
| PostgreSQL 16 | 审查历史存储 |
| Docker + Docker Compose | 部署 |

## 10. Open Questions

1. ~~LLM 提供商选择~~ → 第一版支持 OpenAI 和 Anthropic，通过配置切换
2. ~~是否需要 RAG~~ → 第一版不做 RAG，用 ReAct Loop 替代（Agent 自主调用文件读取工具）
3. ~~前端框架~~ → 使用 Go 模板 + HTMX（避免引入 Node.js 构建链），或 Next.js 静态导出后嵌入
4. 审查结果的反馈闭环（开发者接受/驳回）是否在第一版实现？→ 暂定第一版只记录，不用于优化
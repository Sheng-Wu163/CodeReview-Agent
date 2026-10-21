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
| golang.org/x/tools | v0.52+ | go/packages（v1.1 局部上下文检索） |

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

## 11. v1.3 静态分析管道

### 11.1 Why

当前静态分析由 LLM 通过 `run_static_analysis` 工具在 ReAct 循环中按需调用，存在三个根本问题：

1. **结果不稳定**：同一个 PR，两次审查可能调用不同的工具组合（LLM 的决策有随机性），导致审查结果不可复现
2. **成本浪费**：让 LLM 决定何时调用静态分析，每一次决策都消耗 token；而静态分析本身的运行成本几乎为零
3. **覆盖不全**：LLM 可能忘记调用某些工具（尤其是 semgrep，因为它的输出很长，LLM 倾向于避免）

**目标**：把静态分析从"LLM 按需调用"改为"审查前确定性运行"，结果结构化为 `PreAnalysisFinding` 列表注入各审查 Agent 的 Prompt，让 Agent 聚焦于"验证已知问题 + 补充语义问题"。

### 11.2 Requirements

**FR-16: PreAnalysisFinding 结构**
系统必须定义一个跨工具统一的 `PreAnalysisFinding` 结构体，包含字段：ID（稳定指纹）、Tool、RuleID、FilePath、Line、EndLine、Severity、Message、Snippet。ID 的生成规则为 `<tool>:<ruleID>:<filePath>:<line>`，保证跨次扫描的去重稳定性。

**FR-17: 静态分析管道**
系统必须在 Diff 解析完成后、Agent 执行前，对变更涉及的每个文件运行三个静态分析工具：
- Semgrep（安全规则 + 项目自定义规则）
- golangci-lint（Go 代码质量）
- gitleaks（硬编码密钥检测）

**FR-18: Secret Scanner 集成**
系统必须集成 gitleaks 作为库（而非外部 CLI），扫描变更文件中是否包含硬编码的密钥、Token、密码。gitleaks 的默认规则集必须启用，且支持通过配置文件扩展自定义规则。

**FR-19: 并行执行与超时控制**
系统必须对每个文件并行运行三个工具（goroutine），并对每个工具对每个文件的执行设置 30 秒超时。整体管道的并发度通过配置控制（默认 4 个 worker）。

**FR-20: 降级策略**
系统必须在任一工具不可用时降级：
- Semgrep 不可用 → 记录 warning，安全 Agent 走纯 LLM 审查
- golangci-lint 不可用 → 记录 warning，规范 Agent 走纯 LLM 审查
- gitleaks 不可用 → 记录 critical error，因为 Secret Scanner 是内嵌的，不可用意味着构建错误

**FR-21: Agent 接口改造**
`ReviewAgent.Review` 方法必须新增 `PreFindings []PreAnalysisFinding` 参数。所有四个审查 Agent（安全、逻辑、性能、规范）和验证 Agent 必须适配新接口。

**FR-22: Prompt 注入**
Agent 的 User message 必须包含"静态分析发现"段落，格式为结构化的 preFindings 列表。System prompt 明确指示 Agent："静态分析发现是确定性工具的已知问题列表，你的任务是验证这些发现的真实性，并补充静态分析无法覆盖的语义问题（逻辑错误、并发问题、边界条件）。"

**FR-23: 移除 run_static_analysis 工具**
`run_static_analysis` 工具必须从工具注册表中移除。Agent 不再自己调用静态分析工具，所有静态分析结果由管道预先提供。

**FR-24: 配置化**
静态分析管道的所有参数必须通过配置文件控制（`configs/config.yaml`），包括：是否启用、每个文件的超时、并发度、每个工具的启用状态、Semgrep 自定义规则目录、golangci-lint 配置文件路径。

### 11.3 Interface Freeze Declaration

以下接口在本迭代完成后冻结，后续模块不得修改其字段：

```go
// PreAnalysisFinding 是跨工具统一的静态分析发现结构
type PreAnalysisFinding struct {
    ID       string // 稳定指纹: <tool>:<ruleID>:<filePath>:<line>
    Tool     string // "semgrep" | "golangci-lint" | "gitleaks"
    RuleID   string // 工具内部的规则 ID
    FilePath string
    Line     int
    EndLine  int
    Severity string // "critical" | "warning" | "suggestion"
    Message  string
    Snippet  string // 相关代码片段（最多 5 行）
}

// ReviewInput 是 Agent 的输入结构
type ReviewInput struct {
    Diffs        []diff.FileDiff
    PreFindings  []PreAnalysisFinding
    Tools        []tools.Tool
}
```

如果后续模块需要扩展这些结构，必须走"变更提案"机制（见 `specs/changes/` 目录规范）。

### 11.4 Impact Analysis

**受影响模块**：
- `internal/ai/agents/*`：所有 Agent 的 `Review` 方法签名变化
- `internal/ai/tools/static_analysis.go`：删除
- `internal/orchestrator/orchestrator.go`：新增静态分析管道调用
- `internal/ai/prompts/*.txt`：所有 Agent 的 User message 模板需要更新

**新增模块**：
- `internal/analysis/`：静态分析管道
  - `pipeline.go`：管道编排
  - `types.go`：PreAnalysisFinding 定义
  - `semgrep.go`：Semgrep 集成
  - `golangci.go`：golangci-lint 集成
  - `gitleaks.go`：gitleaks 集成
  - `fingerprint.go`：稳定指纹生成

**配置文件变化**：
`configs/config.yaml` 新增 `static_analysis` 段落。

### 11.5 Edge Cases

| 场景 | 期望行为 |
|------|---------|
| 变更文件为 `.go` 但语法错误，Semgrep 解析失败 | 记录 warning，跳过该文件的 Semgrep 分析，其他工具继续 |
| 变更文件超过 5000 行 | 跳过静态分析，记录 warning "file too large" |
| 所有静态分析工具均不可用 | 记录 error，Agent 走纯 LLM 审查，不阻塞流程 |
| 同一 PR 内同一文件被多次修改 | 只分析一次（用最终版本） |
| Semgrep 输出格式变化（版本升级） | 通过单元测试锁定解析器，升级时测试会失败并提示 |

## CHANGELOG

### v1.3 (2026-10-16) — tag: v1.3-complete
- ADDED: PreAnalysisFinding 结构与稳定性指纹 `<tool>:<ruleID>:<filePath>:<line>`（接口冻结，FR-16）
- ADDED: 静态分析预管道 `internal/analysis/`（Semgrep + golangci-lint + gitleaks，FR-17/FR-18）
- ADDED: 并行执行与每文件超时控制（worker pool + goroutine/select 超时，FR-19）
- ADDED: 降级策略（单工具失败仅降级该工具；全部失败则 Agent 纯 LLM 审查，FR-20）
- ADDED: `static_analysis` 配置块（开关/超时/worker/规则目录，FR-24）
- ADDED: 端到端集成测试 `internal/integration/e2e_test.go`（混合问题 PR 全链路验证）
- MODIFIED: `ReviewAgent.Review` 方法签名（`ReviewInput` 携带 PreFindings，FR-21）
- MODIFIED: Agent User message 模板（新增 `## 静态分析发现` 段落，FR-22）
- REMOVED: run_static_analysis 工具（FR-23）

### v1.2 (2026-10-12) — tag: v1.2-complete
- 基础版本（四 Agent + 验证 Agent + ReAct + GitHub 回写 + 仪表盘）

## 12. v1.1 局部上下文注入

### 12.1 Why

当前版本（v1.3）的审查 Agent 完全依赖 `read_file` 和 `search_code` 工具在 ReAct 循环中“盲目”收集上下文。这导致两个问题：
1. **视角盲区**：如果 Agent 没有主动去搜索某个函数的调用方，就会遗漏“接口变更导致调用方失效”这类跨文件 Bug。
2. **策略不稳定**：不同次审查中，Agent 可能选择不同的工具组合，导致同一 PR 的审查结果不可复现。

**对抗性审查后的修正结论**：不构建全仓库 Code Graph，不引入全局 Two-Pass（成本高、易引入幻觉）。改为**“局部上下文注入”**——只有当 Diff 中出现结构性变更（如导出函数签名变更、接口方法变更）时，才触发一次轻量的单层调用者/被调用者检索，将结果作为“补充上下文”注入 Prompt。

### 12.2 Requirements

**FR-25: 结构性变更检测**
系统必须解析 Diff 中变更的 Go 文件，识别出“结构性变更”。判定标准为：(1) 导出函数（首字母大写）的签名发生变化（参数或返回值改变）；(2) interface 的方法定义发生变化。普通的函数内部逻辑修改不触发上下文检索。

**FR-26: 局部调用者检索（最多 3 跳）**
对于检测到的结构性变更函数，系统必须使用 `go/packages` 检索其调用点。检索上限：最多找出 10 个调用点（Callers），如果超出则截断并记录 warning。不检索超过 3 跳的间接调用。

**FR-27: 局部被调用者摘要**
对于结构性变更函数内部调用的其他包级函数，提取其定义摘要（包名、函数签名、注释），作为 Callees 注入。

**FR-28: 上下文构建与防幻觉约束**
系统必须将检索结果打包为 `ContextBundle` 结构。在注入 Agent 的 Prompt 时，必须包含防幻觉声明：“补充上下文来自静态分析，可能不完整。请结合代码自身逻辑进行判断，不要将其作为唯一依据。”

**FR-29: 优雅降级**
如果 `go/packages` 检索失败（如代码存在编译错误），系统必须降级为空上下文（仅审查 Diff），并记录 warning，不得阻塞审查流程。

### 12.3 Interface Freeze Declaration

以下接口在本迭代完成后冻结：

```go
// StructuralChange 表示 diff 中的结构性变更
type StructuralChange struct {
	FilePath     string
	FunctionName string
	ChangeType   string // "signature_changed", "interface_changed"
}

// CallSite 表示一个调用点
type CallSite struct {
	FilePath   string
	Line       int
	Snippet    string
	CallerFunc string
}

// FunctionDef 表示函数定义摘要
type FunctionDef struct {
	Package   string
	Name      string
	Signature string
	Doc       string
}

// ContextBundle 是注入给 Agent 的局部上下文
type ContextBundle struct {
	Changes []StructuralChange
	Callers []CallSite
	Callees []FunctionDef
}

// ContextBuilder 定义了上下文构建器的接口
type ContextBuilder interface {
	Build(ctx context.Context, diffs []diff.FileDiff) (ContextBundle, error)
}

// ReviewInput 扩展
type ReviewInput struct {
	Diffs       []diff.FileDiff
	PreFindings []analysis.PreAnalysisFinding
	Context     ContextBundle
	Tools       []tools.Tool
}
```

### 12.4 Edge Cases

| 场景 | 期望行为 |
|------|---------|
| 变更文件解析失败（语法错误） | 跳过该文件的结构性变更检测，继续处理其他文件 |
| 结构性变更函数的调用点超过 10 个 | 截断至 10 个，附加警告日志 |
| 变更的是私有函数 | 不触发上下文检索，`ContextBundle` 为空 |
| 非 Go 文件变更（如 yaml、md） | 不进入 AST 解析流程 |
| `go/packages` 加载超时（> 10s） | 取消检索，返回空上下文并记录 warning |

## CHANGELOG

### v1.1 (2026-10-17) — tag: v1.1-complete
- ADDED: 局部上下文注入（`ContextBundle`：结构性变更 + 调用点 + 被调用者摘要，FR-25~FR-29）
- ADDED: 结构性变更检测（go/ast 解析 diff，导出函数签名/接口方法集变更）
- ADDED: `go/packages` 轻量级调用者检索（go/packages + 10s 超时 + 最多 10 个调用点截断）
- ADDED: 同包被调用者摘要提取（签名 + 首行注释）
- ADDED: `internal/context/` 包（types/builder/detector/callers/callees/builder_impl）
- MODIFIED: ReviewInput 结构体（新增 `Context ContextBundle` 字段）
- MODIFIED: 所有审查 Agent 的 User message（三段式：`## 代码变更` / `## 静态分析发现` / `## 补充上下文`）
- MODIFIED: System Prompt 末尾追加防幻觉声明（FR-28）

## 13. v1.6 评估体系（相对趋势与复核机制）

### 13.1 Why

在 v1.3 和 v1.1 引入后，我们需要量化审查质量以指导后续迭代（如 v1.2 的自适应策略）。但传统的 Precision/Recall 绝对分数存在致命缺陷：
1. **样本偏差**：人工标注（Ground Truth）是不完整的。如果 Agent 发现了一个标注者没注意到的真实 Bug，传统计算会将其记为 False Positive，从而**惩罚“发现意外真实问题”的能力**。
2. **阈值僵化**：追求绝对高分（如 Precision > 90%）会导致 Agent 倾向于只报告“教科书式”的简单问题，而忽略复杂的隐蔽逻辑。

**修正结论**：建立**相对趋势评估**体系。不追求绝对数值达标，只关注“v1.6 相比 v1.1 是变好了还是变差了”。同时引入 `unexpected_but_valid`（UBV）复核机制，将疑似误报转为人工确认队列，动态修正评估基线。

### 13.2 Requirements

**FR-30: 离线评估数据集格式**
系统必须支持读取本地 `eval/dataset/` 目录下的评估样本。每个样本包含：`pr.diff`（变更内容）、`source_files/`（受影响的源文件）、以及 `ground_truth.json`（人工标注的预期发现列表，允许不完整）。

**FR-31: 相对趋势计算器**
评估脚本必须计算当前版本相对于指定 baseline（例如上一个 tag）的 `Precision Delta` 和 `Recall Delta`。评估报告侧重于展示变化趋势，而非绝对值。

**FR-32: UBV (Unexpected But Valid) 复核机制**
当 Agent 报告的发现与 Ground Truth 不匹配时，**不直接标记为 False Positive**。而是将其归类为 `Unreviewed_Discovery`，并输出到一个单独的 Markdown 复核报告中。
人工（开发者）审阅该报告后，可以在 `ground_truth.json` 中追加这条发现。评估脚本下次运行时，将基于更新后的 Ground Truth 重新计算指标。

**FR-33: 自动化评估执行器**
提供 CLI 命令 `make eval`，它能批量读取 `eval/dataset/` 中的样本，调用完整的审查管道（Orchestrator），对比 Ground Truth 并生成最终评估报告（`eval/report.md` 和 `eval/metrics.json`）。

### 13.3 Interface Freeze Declaration

以下接口在本迭代完成后冻结：

```go
// GroundTruthItem 人工标注的预期问题
type GroundTruthItem struct {
	FilePath string
	Line     int
	Severity string
	Category string // "security", "logic", "performance", "style"
	Message  string
}

// EvalMetrics 评估指标
type EvalMetrics struct {
	TruePositives      int     `json:"tp"`
	FalseNegatives     int     `json:"fn"`
	UnreviewedDiscovery int    `json:"ubv"` // 待复核发现数
	Precision          float64 `json:"precision"`
	Recall             float64 `json:"recall"`
}

// EvalReport 最终评估报告结构
type EvalReport struct {
	Version       string      `json:"version"`
	BaselineVersion string    `json:"baseline_version"`
	Metrics       EvalMetrics `json:"metrics"`
	PrecisionDelta float64    `json:"precision_delta"`
	RecallDelta    float64    `json:"recall_delta"`
	UBVItems      []AgentFinding `json:"ubv_items"`
}
```

### 13.4 Edge Cases

| 场景 | 期望行为 |
|------|---------|
| 数据集为空 | 报错退出，提示需提供至少 1 个测试样本 |
| 某次审查因 LLM API 超时中断 | 记录该样本为 `failed`，跳过计算，不中断整体流程 |
| 生成的 UBV 报告过大（> 50 条） | 仅输出前 50 条，提示“请检查 Agent 是否发生幻觉” |
| Ground Truth 中某条预期问题未被发现 | 记为 False Negative，但不阻塞交付（允许当前版本存在 FN） |

## CHANGELOG

### v1.6 (2026-10-18) — tag: v1.6-complete
- ADDED: 离线评估数据集格式（`eval/dataset/<sample>/{pr.diff,source_files/,ground_truth.json}`，FR-30）
- ADDED: 相对趋势计算逻辑（Precision/Recall Delta，FR-31）
- ADDED: UBV（Unexpected-But-Valid）复核机制，输出 Markdown 报告（>50 条截断告警，FR-32）
- ADDED: CLI 评估执行器 `cmd/eval` + `make eval`（生成 `eval/metrics.json` 与 `eval/report.md`，FR-33）
- ADDED: `internal/eval/` 包（types/evaluator/reporter）+ 数据集样本 `sample-1`
- FIXED: 剥离 ReAct 模板注入的 `stop` 序列——推理模型在隐藏推理中回显 "Observation:" 被截断导致 content 为空（agent 报 "unable to parse agent output"）
- FIXED: 提高 completion token 预算（4096→16384），避免推理 token 挤占最终内容
- FIXED: `read_file` 容忍 LLM 传参带引号（`"db.go"`）

## 14. v1.2 自适应审查（策略开关）

### 14.1 Why

随着 v1.3（静态分析）和 v1.1（上下文注入）的引入，系统的审查能力显著增强，但也暴露出“一刀切”策略的工程问题：
1. **小 PR 过度审查**：修改 5 行代码的 Trivial PR，依然走 4 个 Agent + 验证 Agent 的完整流程，浪费大量 Token 和等待时间。
2. **大 PR 审查不足**：涉及核心模块的 Complex PR，需要验证 Agent 更加严格地把关。

**对抗性审查的修正结论**：v1.2 **只做“Agent 组合的开关”**，不做任何参数级的动态调优（如动态修改 `max_iterations`、温度、Prompt 变体）。因为参数调优会导致同一个 PR 两次审查的结果完全不可复现，这是从 Demo 走向生产环境的致命缺陷。

### 14.2 Requirements

**FR-34: 复杂度评估器（ComplexityAssessor）**
系统必须实现一个基于 Diff 统计特征的评估器，将 PR 分为三个等级：
- `Trivial`：变更总行数 < 20 行，且只涉及 1 个文件，且不涉及敏感目录/核心模块。
- `Complex`：变更总行数 > 200 行，或涉及 > 5 个文件，或触碰了配置文件中的 `sensitive_paths`（如 `internal/auth/`、`migrations/`）。
- `Standard`：介于两者之间的所有情况。

**FR-35: 策略选择器（StrategySelector）**
系统必须根据复杂度等级选择审查策略：
- `Trivial`：仅启用 `SecurityAgent` 和 `LintAgent`，**跳过** `LogicAgent`、`PerformanceAgent` 和 `VerifierAgent`。
- `Standard`：启用全部 4 个审查 Agent + 1 个 `VerifierAgent`（当前默认策略）。
- `Complex`：启用全部 4 个审查 Agent + 1 个 `VerifierAgent`（强制开启验证）。

**FR-36: 可复现性保证**
系统必须保证，**只要 PR 的 Diff 内容相同，复杂度评估的结果就必须完全一致**。评估器不得依赖 LLM 进行复杂度判定，必须使用确定性的代码统计逻辑。

**FR-37: 编排器集成**
`Orchestrator` 必须在执行审查前调用评估器，获取 `ComplexityLevel`，然后按需启动相应的 Agent。如果跳过了某个 Agent，必须在数据库中记录该 Agent 的状态为 `"skipped_by_strategy"`。

**FR-38: 配置化**
复杂度判定的阈值（20 行、200 行、敏感路径列表）必须支持通过 `configs/config.yaml` 配置，但不允许在运行时动态修改。

### 14.3 Interface Freeze Declaration

```go
// ComplexityLevel 定义了 PR 的复杂度
type ComplexityLevel string

const (
	ComplexityTrivial  ComplexityLevel = "trivial"
	ComplexityStandard ComplexityLevel = "standard"
	ComplexityComplex  ComplexityLevel = "complex"
)

// ReviewStrategy 定义了当前 PR 需要执行的审查策略
type ReviewStrategy struct {
	Level          ComplexityLevel
	EnabledAgents  []string // 例如 ["security", "lint"]
	EnableVerifier bool
}

// ComplexityAssessor 负责评估 PR 复杂度
type ComplexityAssessor interface {
	Assess(diffs []diff.FileDiff) ComplexityLevel
}

// StrategySelector 负责根据复杂度选择策略
type StrategySelector interface {
	Select(level ComplexityLevel) ReviewStrategy
}
```

### 14.4 Edge Cases

| 场景 | 期望行为 |
|------|---------|
| Diff 为空或全部被过滤 | 判定为 `Trivial`，直接提交 "LGTM ✅" 并结束 |
| 涉及敏感路径，但只改了 1 行注释 | 判定为 `Complex`（安全优先原则，宁可错杀不可放过） |
| 配置文件未定义 `sensitive_paths` | 使用代码内置的默认敏感路径（如 `auth`, `migration`） |

## CHANGELOG

### v1.2 (2026-10-18) — tag: v1.2-complete
- ADDED: 基于确定性统计的复杂度评估器（`internal/review/assessor.go`，无 LLM/无网络，FR-34/FR-36）
- ADDED: Agent 组合的开关式策略选择（Trivial 仅 security+lint 且跳过 Verifier，FR-35）
- ADDED: 敏感路径配置（`static_analysis.adaptive.sensitive_paths`，空则用内置默认，FR-38）
- ADDED: 阈值配置化（`trivial_max_lines`/`complex_min_lines`/`complex_min_files`，FR-38）
- MODIFIED: Orchestrator 按策略执行 Agent，被跳过的 Agent 记录为 `skipped_by_strategy`（FR-37）
- MODIFIED: `reviews` 表新增 `agent_status`（JSONB，migration 003）持久化各 Agent 状态（AC-77）
- FIXED: golangci-lint 单实例锁增加进程内互斥（修复 pipeline 多 worker 并发自锁）+ 跨进程退避重试

## 15. v1.4 隐式反馈追踪

### 15.1 Why

在真实工程中，开发者极少会在 PR 评论中显式回复“已修复”或“误报”。如果系统只依赖显式反馈，样本会严重偏向“情绪化评论”，导致模型优化方向跑偏。
**修正结论**：引入隐式反馈信号。系统通过追踪 PR 的后续 Push（`synchronize` 事件）和合并状态（`closed` 且 `merged`），自动推断开发者对审查发现的真实态度，构建无偏的反馈数据集。

### 15.2 Requirements

**FR-39: 数据库 Schema 扩展**
系统必须在 `findings` 表中增加 `feedback_status` 字段，枚举值为：`pending`（待定）、`accepted_implicit`（隐式接受：代码被修改）、`ignored_implicit`（隐式忽略：代码未改但 PR 合并）、`accepted_explicit`（显式接受，预留）、`rejected_explicit`（显式拒绝，预留）。同时新增 `pr_commits` 表，记录 PR 的每次 commit 历史。

**FR-40: Webhook 事件扩展**
系统必须扩展 Webhook 处理逻辑，支持监听 `pull_request` 的 `synchronize`（新代码推送到 PR）和 `closed`（PR 关闭/合并）动作。

**FR-41: 隐式反馈追踪器（ImplicitFeedbackTracker）**
- **收到 `synchronize` 事件时**：对比新的 commit 与审查发生时的旧 commit，如果发现某条 `pending` 的 finding 所在文件对应的代码行被修改，则将其状态更新为 `accepted_implicit`。
- **收到 `closed` 事件且 `merged=true` 时**：对于所有仍为 `pending` 的 finding，若在后续 commit 中未被修改，则更新为 `ignored_implicit`。

**FR-42: 纯文本级代码比对**
为了避免引入高成本的 AST 对比（容易引入偏差），隐式追踪器必须使用**基于字符串相似度的局部 diff 比对**。只对比 finding 指向的文件及周围 10 行代码。如果新代码与旧代码相似度 < 80%，视为“已被修改”。

**FR-43: 统计看板**
仪表盘必须新增“Agent 发现采纳率”指标，按显式 + 隐式综合计算接受率，并提供按 Agent 类型（安全、逻辑等）分组的统计数据。

### 15.3 Interface Freeze Declaration

```go
type FeedbackStatus string

const (
	StatusPending          FeedbackStatus = "pending"
	StatusAcceptedImplicit FeedbackStatus = "accepted_implicit"
	StatusIgnoredImplicit  FeedbackStatus = "ignored_implicit"
	// 预留：StatusAcceptedExplicit, StatusRejectedExplicit
)

// PRCommit 记录 PR 的提交历史
type PRCommit struct {
	RepoFullName string
	PRNumber     int
	SHA          string
	CreatedAt    time.Time
}

// FeedbackTracker 定义反馈追踪接口
type FeedbackTracker interface {
	TrackOnSynchronize(ctx context.Context, repo string, prNumber int, newSHA string, oldSHA string) error
	TrackOnClosed(ctx context.Context, repo string, prNumber int, isMerged bool) error
}
```

### 15.4 Edge Cases

| 场景 | 期望行为 |
|------|---------|
| PR 被直接关闭（未合并） | 所有 `pending` 状态的 finding 标记为 `ignored_implicit`（或直接忽略，不做采纳率计算） |
| 后续 Push 删除了 finding 所在的整个文件 | 视为 `accepted_implicit`（通过删除解决了问题） |
| Webhook 漏发了某个 commit | 允许追踪失效，系统在每日定时任务中通过 GitHub API 拉取缺失历史进行修正（v1.5实现，当前记录日志即可） |

## CHANGELOG

### v1.4 (2026-10-18) — tag: v1.4-complete
- ADDED: 隐式反馈追踪机制（synchronize/closed → accepted_implicit/ignored_implicit，FR-41）
- ADDED: findings 表扩展 `feedback_status` 字段（默认 pending，AC-81）
- ADDED: `pr_commits` 表（commit 历史，去重，FR-39）
- ADDED: 统计看板的采纳率指标（`GET /api/metrics/feedback` + 仪表盘进度条，FR-43）
- ADDED: `internal/feedback/` 包（types/tracker/diff_compare，纯文本相似度比对 FR-42）
- ADDED: `github.Client.GetFileAtRef`（按 ref 拉取文件，404 → ErrFileNotFound）
- ADDED: `db.EnsureDatabase`（测试包数据库隔离，消除并行 truncate 互相干扰）
- MODIFIED: Webhook 处理 `synchronize`（before/after SHA）与 `closed`（merged）动作，异步分发 Tracker（FR-40）

## 16. v1.7 前端可视化重构

### 16.1 Requirements

**FR-44: SSE 审查流接口**
系统必须新增 `GET /api/stream-review?pr_id={id}` 接口。该接口使用 SSE (Server-Sent Events) 协议，在 Orchestrator 执行过程中，实时推送 5 种结构化事件（status, agent_start, tool_call, finding, done）。

**FR-45: 无构建前端页面**
系统必须在 `static/` 目录下提供一个无构建的 Vue 3 单页应用（SPA），通过 CDN 引入 Vue 3、Tailwind CSS、marked.js 和 highlight.js，不引入 Node.js 构建链。

**FR-46: 双栏布局与过程可视化**
页面必须包含双栏布局：
- **左侧**：输入区（用于触发模拟审查）和审查历史列表。
- **右侧**：Agent 执行流展示。必须以时间轴的形式实时展示 Agent 的状态切换、工具调用过程（显示工具名和参数），以及流式输出的 Markdown 结果。

**FR-47: 状态与色彩标识**
前端必须根据 `agent` 字段（security, logic, performance, lint, verifier）使用不同的颜色高亮，并根据 `severity`（critical, warning, suggestion）展示不同的图标。

### 16.2 Edge Cases

| 场景 | 期望行为 |
|------|---------|
| SSE 连接网络断开 | 前端显示 "连接已断开，请刷新重试" 提示，并自动重连 1 次 |
| 后端返回的 Markdown 包含恶意 `<script>` | `marked.js` 渲染时需进行 XSS 转义（使用 `DOMPurify` 或在配置中禁用 HTML 标签解析） |
| 审查历史为空 | 左侧列表显示 "暂无审查记录" |

## CHANGELOG

### v1.7 (2026-10-18) — tag: v1.7-complete
- ADDED: SSE 流式审查接口 `GET /api/stream-review?pr_id={id}`（status/agent_start/tool_call/finding/done 五类事件，FR-44）
- ADDED: Vue 3 无构建可视化前端（`static/`，CDN 引入 Vue/Tailwind/marked/DOMPurify/highlight.js，FR-45）
- ADDED: 双栏布局：左侧触发区+审查历史，右侧 Agent 执行时间轴（FR-46）
- MODIFIED: Orchestrator 增加事件回调钩子（SetEventChan + 工具包装器，FR-44）
- ADDED: XSS 防护（DOMPurify 清洗 LLM 输出）与断线自动重连（spec 16.2）

### v1.8 (2026-10-21)
- ADDED: `internal/harness` Harness 控制层（`Harness` 接口、`AgentResult`、`AgentStatus`，FR-48/FR-49）
- ADDED: 工具调用五阶段治理：参数校验、单次超时、只读工具最多 2 次指数退避重试、SHA-256 指纹记录（FR-50）
- ADDED: 路径震荡检测（连续 3 次相同指纹 / A-B-A-B 两轮交替）并返回 `ErrOscillation`（FR-51）
- ADDED: 完成门禁：`completed` 且 `ToolCallCount == 0` 强制降级为 `partial`（`suspicious_no_tool_calls`）
- MODIFIED: Orchestrator 通过 Harness 执行所有 Agent，仅 `completed`/`partial` 结果进入下游验证；failed Agent 写入 `reviews.agent_results` 并在 Review 评论中报警
- ADDED: 迁移 `005_add_agent_results.sql`，持久化每个 Agent 的状态、工具调用次数与执行日志

## 17. v1.8 Harness 控制层与工具治理

### 17.1 Why
当前系统依赖 LangChainGo 的 AgentExecutor，缺乏统一的执行控制。导致：1) 工具调用超时无法全局干预；2) Agent 产生死循环（路径震荡）时只能等 max_iterations 耗尽；3) 无法区分“审查完成且无发现”和“Agent 崩溃未执行”。

### 17.2 Requirements
**FR-48: Harness 统一接口**
系统必须实现 `Harness` 接口，包装所有的 Agent 执行过程，输入 `ReviewInput`，输出结构化的 `AgentResult`。

**FR-49: 结构化结果与完成门禁**
`AgentResult` 必须包含 `Status` (completed / failed / partial)、`Findings`、`ToolCallCount` 和 `ErrorReason`。
**门禁规则**：如果 `Status == completed` 但 `ToolCallCount == 0`，系统必须将其降级为 `partial`，并在最终报告中标记“该维度审查存疑”。

**FR-50: 工具调用五阶段容错**
在 Harness 中拦截工具执行：
- 校验 (Validate)：工具参数格式与权限。
- 执行 (Execute)：超时控制。
- 确认 (Confirm)：对只读工具超时，允许进行最多 2 次指数退避重试。
- 记录 (Record)：记录每次工具的 `指纹` (ToolName + Hash(Args))。

**FR-51: 路径震荡检测**
如果连续 3 次工具调用的指纹完全一致，或出现 A→B→A 的交替调用超过 2 轮，Harness 必须抛出 `ErrOscillation`，中断该 Agent 并标记为 `failed`。

### 17.3 Interface Freeze Declaration
```go
type AgentStatus string

const (
	StatusCompleted AgentStatus = "completed"
	StatusFailed    AgentStatus = "failed"
	StatusPartial   AgentStatus = "partial"
)

type AgentResult struct {
	Status        AgentStatus
	Findings      []AgentFinding
	ToolCallCount int
	ErrorReason   string
	ExecutionLogs []ToolLog // 记录所有工具调用指纹
}

type ToolLog struct {
	ToolName  string
	ArgsHash  string
	Timestamp time.Time
	Success   bool
}

type Harness interface {
	Execute(ctx context.Context, agent ReviewAgent, input ReviewInput) (AgentResult, error)
}
```

### 17.4 Edge Cases
- 当发生震荡被中断时，必须保留中断前的 Findings，并附带 `partial` 状态。
- Harness 产生的日志需要异步写入数据库，以便排查执行差异。
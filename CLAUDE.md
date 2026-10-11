# CodeReviewAgent - 项目指令

## 技术栈

- **语言**：Go 1.23+
- **LLM 框架**：LangChainGo（github.com/tmc/langchaingo）
- **HTTP 路由**：chi（github.com/go-chi/chi/v5）
- **数据库**：PostgreSQL 16 + pgx（github.com/jackc/pgx/v5）
- **Git 操作**：go-git（github.com/go-git/go-git/v5）
- **GitHub API**：go-github（github.com/google/go-github/v66）
- **静态分析**：semgrep_go + golangci-lint
- **日志**：zap（go.uber.org/zap）
- **配置**：viper（github.com/spf13/viper）

## 构建与测试命令

- 构建：`go build ./...`
- 运行：`make run`
- 测试：`go test ./... -race`
- 测试（含覆盖率）：`go test ./... -cover`
- 类型检查：`go vet ./...`
- 代码检查：`golangci-lint run ./...`
- 迁移：`make migrate-up`
- Docker 启动：`docker compose up --build`

## Spec-Driven Development 工作流（必须遵守）

### 铁律：SPECS FIRST, CODE SECOND

**在 specs/code-review-agent/ 下的 spec.md 经过用户明确审批之前，绝对禁止编写任何实现代码。**

### 当前 Spec 状态

- spec.md：⬜ 待审批
- tasks.md：⬜ 待审批
- checklist.md：⬜ 待审批

**状态变更规则**：只有当用户明确回复“确认”“审批通过”等指令后，才可将对应文档状态改为 `✅ 已审批`。AI 不得自行修改此状态，也不得在未审批的情况下开始任何实现任务。

### 工作流程

1. 严格按照 tasks.md 中的任务顺序（T1 → T2 → … → T14）逐项执行
2. 每个任务完成后，在 tasks.md 中将对应任务的 `⬜ 未开始` 改为 `✅ 已完成`
3. 每完成一个任务，运行该任务“完成标准”中指定的验证命令
4. 所有任务完成后，按照 checklist.md 逐项验证
5. 检查点未通过的项，创建修复任务并重新执行

### Agent 实现规范

**所有 Agent 必须实现统一的接口**：

```go
type ReviewAgent interface {
    Name() string
    Review(ctx context.Context, diffs []diff.FileDiff, tools []tools.Tool) ([]AgentFinding, error)
}
```

**工具必须实现 LangChainGo 的 tools.Tool 接口**：

```go
type tools.Tool interface {
    Name() string
    Description() string
    Call(ctx context.Context, input string) (string, error)
}
```

使用编译期接口校验：`var _ tools.Tool = ReadFileTool{}`

**接口定义位置的约定**：
- 自定义接口（如 `ReviewAgent`、`ReviewRepository`）定义在消费方包内
- 实现 LangChainGo 的 `tools.Tool` 接口时，使用编译期断言 `var _ tools.Tool = XxxTool{}` 校验，这属于依赖外部契约，不违反“接口定义在消费方”原则

**Agent 的 System Prompt 必须外部化**：存放在 `internal/ai/prompts/*.txt` 中，便于迭代优化，不硬编码在 Go 代码中。

### 编码规范

- 使用 Go 标准项目布局（`cmd/`、`internal/`）
- 全部业务代码放在 `internal/` 下，本项目不对外暴露可导入的包
- 所有导出的函数和类型必须有文档注释
- 错误处理：使用 `fmt.Errorf("context: %w", err)` 包装，不吞错
- 不使用 `panic`（除 main 中的不可恢复错误外）
- 测试文件与被测文件同目录，命名为 `*_test.go`
- 使用表驱动测试（table-driven tests）
- 自定义接口定义在消费方（consumer）而非实现方

### 禁止事项

- ❌ 未经 Spec 审批就写实现代码
- ❌ 在 spec.md 范围之外修改无关文件
- ❌ 跳过 checklist 验证直接标记完成
- ❌ 在 spec.md 中未定义的功能上“自由发挥”
- ❌ 硬编码任何 API Key、Webhook Secret 或密码
- ❌ 在 Agent 的 System Prompt 中硬编码代码（必须外部化为 .txt 文件）
- ⚠️ 避免使用 `interface{}` 或 `any`。与 LangChainGo 等外部库交互时不可避免的场景除外，若确需使用，必须在该处写注释说明原因

### 目录结构

```
codereviewagent/
├── cmd/server/main.go
├── internal/
│   ├── ai/
│   │   ├── agents/          # 四个审查 Agent + 验证 Agent
│   │   ├── prompts/         # System Prompt (.txt)
│   │   ├── tools/           # LangChainGo 工具实现
│   │   └── llm.go           # LLM 客户端封装
│   ├── config/              # Viper 配置
│   ├── db/                  # pgx 连接 + 迁移
│   ├── diff/                # Diff 解析 + 文件过滤
│   ├── github/              # GitHub API 客户端
│   ├── handler/             # HTTP 处理器
│   ├── middleware/          # HMAC 签名验证
│   ├── orchestrator/        # 审查编排器
│   └── repository/          # 数据库仓储
├── templates/               # Go 模板（仪表盘）
├── configs/config.yaml.example
├── specs/code-review-agent/ # Spec 文档
├── docker-compose.yml
├── Dockerfile
├── Makefile
└── go.mod
```

## 环境变量

| 变量名 | 说明 | 是否必须 |
|--------|------|---------|
| `WEBHOOK_SECRET` | GitHub Webhook HMAC 密钥 | 是 |
| `GITHUB_TOKEN` | GitHub App Token | 是 |
| `LLM_PROVIDER` | `openai` 或 `anthropic` | 是 |
| `OPENAI_API_KEY` | OpenAI API Key | 当 LLM_PROVIDER=openai 时 |
| `ANTHROPIC_API_KEY` | Anthropic API Key | 当 LLM_PROVIDER=anthropic 时 |
| `DATABASE_URL` | PostgreSQL 连接字符串 | 是 |
| `SERVER_PORT` | HTTP 端口（默认 8080） | 否 |
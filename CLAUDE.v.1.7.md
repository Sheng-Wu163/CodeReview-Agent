# v1.7 前端可视化重构 - 专项指令

## 模块专属规范

- **无构建前端**：所有前端代码必须放在 `static/` 目录下。只能使用 CDN 引入依赖。禁止引入 `webpack`, `vite` 等构建工具。
- **SSE 超时控制**：Go 的 SSE Handler 内部必须将 `http.ResponseWriter` 断言为 `http.Flusher` 并定期 Flush，且要监听 `r.Context().Done()` 以便客户端断开时清理资源。
- **防 XSS 注入**：在使用 `marked.js` 渲染 Markdown 时，必须配置 `sanitize: true` 或结合 `DOMPurify`，因为 LLM 的返回内容是不可信的。
- **事件驱动 UI**：Vue 的渲染必须由 SSE 收到的 `event` 驱动，而不是定时轮询。

## UI 设计规范（极简科技风）

- 主色调：黑色（`bg-gray-950`）背景，代码与文字使用灰白色（`text-gray-200`）。
- 高亮色：Agent 状态用亮蓝色（`text-blue-400`），Critical 用红色（`text-red-500`），Warning 用黄色（`text-yellow-400`）。
- 排版：左侧边栏固定宽度（`w-1/3`），右侧内容区自适应（`flex-1`），内容区必须支持滚动。
- 动态效果：新事件插入时，需要带简单的淡入动画（`transition-all duration-300`）。

## Token 节省策略（执行任务时必须遵守）

1. **不要重写后端逻辑**：本次迭代只做“在已有逻辑中插入回调”。严禁为了让 SSE 好写而重构 Orchestrator 的核心业务逻辑。
2. **前端页面直接生成**：不要进行多轮 UI 调试，一次性生成完整的 `index.html` 和 `app.js`。依赖列表直接写在 `<head>` 的 CDN 标签里。
3. **联调测试使用 Mock**：编写一个极简的 `main_test.go` 验证 SSE 格式即可，不要启动真实的 LLM 来测试前端。
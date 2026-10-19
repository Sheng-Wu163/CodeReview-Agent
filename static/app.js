// CodeReviewAgent review-stream frontend (v1.7): a no-build Vue 3 SPA
// driven by SSE events (FR-45..FR-47).
"use strict";

const { createApp, ref, computed, nextTick } = Vue;

// AGENT_META colors each agent type (FR-47): the active agent is bright
// blue, severity drives the finding colors.
const AGENT_META = {
  security: { label: "安全 Agent", cls: "text-red-400" },
  logic: { label: "逻辑 Agent", cls: "text-emerald-400" },
  performance: { label: "性能 Agent", cls: "text-amber-400" },
  lint: { label: "规范 Agent", cls: "text-sky-400" },
  verifier: { label: "验证 Agent", cls: "text-violet-400" },
};

const SEVERITY_META = {
  critical: { icon: "🔴", color: "text-red-500" },
  warning: { icon: "🟡", color: "text-yellow-400" },
  suggestion: { icon: "🔵", color: "text-blue-400" },
};

// TOOL_VERBS renders the tool_call line in Chinese (FR-46).
const TOOL_VERBS = {
  read_file: "正在读取文件",
  search_code: "正在搜索代码",
  get_function_definition: "正在获取函数定义",
};

createApp({
  setup() {
    const prId = ref("demo");
    const events = ref([]);
    const isLoading = ref(false);
    const connectionLost = ref(false);
    const history = ref([]);
    const stream = ref(null);
    let source = null;
    let reconnected = false; // spec 16.2: auto-reconnect exactly once

    // renderMd sanitises untrusted LLM output before rendering (FR-45 /
    // spec 16.2): marked parses, DOMPurify strips scripts and handlers.
    function renderMd(text) {
      if (!text) return "";
      const html = marked.parse(String(text), { mangle: false, headerIds: false });
      return DOMPurify.sanitize(html, { USE_PROFILES: { html: true } });
    }

    // highlightCode applies highlight.js to a plain code snippet.
    function highlightCode(code) {
      if (window.hljs) {
        const result = window.hljs.highlight(code, { language: "go" });
        return result.value;
      }
      return code;
    }

    function agentColor(agent) {
      return (AGENT_META[agent] || { cls: "text-blue-400" }).cls + " bg-gray-800";
    }
    function agentLabel(agent) {
      return (AGENT_META[agent] || { label: agent }).label;
    }
    function severityIcon(sev) {
      return (SEVERITY_META[sev] || { icon: "⚪" }).icon;
    }
    function severityColor(sev) {
      return (SEVERITY_META[sev] || { color: "text-gray-300" }).color;
    }
    function findingBorder(sev) {
      return { critical: "border-red-500", warning: "border-yellow-400", suggestion: "border-blue-400" }[sev] || "border-gray-700";
    }

    function toolVerb(tool) {
      return TOOL_VERBS[tool] || "正在调用";
    }

    async function loadHistory() {
      try {
        const resp = await fetch("/api/reviews");
        const body = await resp.json();
        history.value = body.reviews || [];
      } catch {
        /* history is best-effort; the stream is the primary view */
      }
    }

    function scrollBottom() {
      nextTick(() => {
        if (stream.value) stream.value.scrollTop = stream.value.scrollHeight;
      });
    }

    function startReview() {
      if (isLoading.value) return;
      events.value = [];
      connectionLost.value = false;
      reconnected = false;
      isLoading.value = true;

      const id = prId.value.trim() || "demo";
      const url = `/api/stream-review?pr_id=${encodeURIComponent(id)}&demo=${id === "demo" ? 1 : 0}`;
      source = new EventSource(url);

      // The wire shape is {event, data}; the Vue UI is event-driven, never
      // polled (CLAUDE.v.1.7.md).
      source.onmessage = (e) => {
        try {
          events.value.push(JSON.parse(e.data));
        } catch {
          return; // ignore malformed frames
        }
        scrollBottom();
        const parsed = events.value[events.value.length - 1];
        if (parsed.event === "done") {
          isLoading.value = false;
          source.close();
          loadHistory();
        }
      };
      source.onerror = () => {
        source.close();
        if (!reconnected) {
          // spec 16.2: reconnect automatically once.
          reconnected = true;
          startReview();
          return;
        }
        isLoading.value = false;
        connectionLost.value = true;
      };
    }

    loadHistory();

    return {
      prId, events, isLoading, connectionLost, history, stream,
      startReview, renderMd, highlightCode,
      agentColor, agentLabel, severityIcon, severityColor, findingBorder, toolVerb,
    };
  },
}).mount("#app");

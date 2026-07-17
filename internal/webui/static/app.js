const app = document.querySelector("#app");
const toast = document.querySelector("#toast");
const agentDialog = document.querySelector("#agent-dialog");

const state = {
  agents: [],
  agent: null,
  sessions: [],
  session: null,
  busy: false,
  sidebarOpen: false,
  settingsTab: "soul",
  soulDraft: "",
  streamAbort: null,
};

const icons = {
  arrowLeft: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m15 18-6-6 6-6"/><path d="M9 12h10"/></svg>`,
  arrowRight: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 18 6-6-6-6"/></svg>`,
  chevronDown: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m8 10 4 4 4-4"/></svg>`,
  menu: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h16"/></svg>`,
  message: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 15a4 4 0 0 1-4 4H8l-5 3V7a4 4 0 0 1 4-4h10a4 4 0 0 1 4 4Z"/></svg>`,
  moon: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20.2 15.3A8.5 8.5 0 0 1 8.7 3.8 8.5 8.5 0 1 0 20.2 15.3Z"/></svg>`,
  plus: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg>`,
  send: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m22 2-7 20-4-9-9-4Z"/><path d="M22 2 11 13"/></svg>`,
  stop: `<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="7" y="7" width="10" height="10" rx="1.5" fill="currentColor" stroke="none"/></svg>`,
  settings: `<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1a1.7 1.7 0 0 0 1.9.3A1.7 1.7 0 0 0 10 3V2.8h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z"/></svg>`,
  spark: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m12 3 1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5Z"/><path d="m18.5 15 .8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8Z"/></svg>`,
  sun: `<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>`,
};

function escapeHTML(value = "") {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function initials(name = "Agent") {
  const chars = [...name.trim()];
  return escapeHTML(chars.slice(0, 2).join("").toUpperCase() || "AI");
}

function compactModel(model = "") {
  const parts = model.split("/");
  return escapeHTML(parts.at(-1) || model || "默认模型");
}

function formatRelative(value) {
  if (!value) return "尚未对话";
  const date = new Date(value);
  const diff = Date.now() - date.getTime();
  if (!Number.isFinite(diff)) return "最近使用";
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "刚刚使用";
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days} 天前`;
  return new Intl.DateTimeFormat("zh-CN", { month: "numeric", day: "numeric" }).format(date);
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: {
      ...(options.body ? { "Content-Type": "application/json" } : {}),
      ...(options.headers || {}),
    },
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(payload.error || `请求失败（${response.status}）`);
  return payload;
}

function showToast(message, isError = false) {
  toast.textContent = message;
  toast.classList.toggle("error", isError);
  toast.classList.add("visible");
  clearTimeout(showToast.timer);
  showToast.timer = setTimeout(() => toast.classList.remove("visible"), 2600);
}

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  localStorage.setItem("luckyclaw-theme", theme);
}

function toggleTheme() {
  applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark");
  const button = document.querySelector(".theme-toggle");
  const dark = document.documentElement.dataset.theme === "dark";
  if (button) {
    button.innerHTML = dark ? icons.sun : icons.moon;
    button.setAttribute("aria-label", `切换为${dark ? "浅色" : "深色"}主题`);
    button.setAttribute("title", "切换主题");
  }
}

function themeButton() {
  const dark = document.documentElement.dataset.theme === "dark";
  return `<button class="icon-button theme-toggle" type="button" aria-label="切换为${dark ? "浅色" : "深色"}主题" title="切换主题">${dark ? icons.sun : icons.moon}</button>`;
}

function renderHome() {
  state.agent = null;
  state.session = null;
  const sessionTotal = state.agents.reduce((total, agent) => total + agent.session_count, 0);
  const platformTotal = new Set(state.agents.flatMap((agent) => agent.connected_via.map((item) => item.channel))).size;
  app.innerHTML = `
    <div class="home-shell">
      <header class="topbar" aria-label="全局导航">
        <div class="brand-lockup">
          <div class="brand-mark" aria-hidden="true"><span>LC</span></div>
          <div>LuckyClaw<small>Agent workspace</small></div>
        </div>
        <div class="topbar-actions">
          <div class="runtime-pill"><span class="live-dot"></span>本地运行时已连接</div>
          ${themeButton()}
        </div>
      </header>
      <section class="home-main" aria-labelledby="page-title">
        <div class="hero">
          <div>
            <p class="eyebrow">你的 Agent 队伍 · ${String(state.agents.length).padStart(2, "0")}</p>
            <h1 id="page-title">让每一个 <em>Agent</em><br>都有自己的灵魂。</h1>
          </div>
          <aside class="hero-aside">
            <p>在一个工作台里管理角色、延续对话，并逐步把它们带到你每天使用的平台。</p>
            <div class="hero-stats" aria-label="运行统计">
              <div class="hero-stat"><strong>${state.agents.length}</strong><span>可用 Agent</span></div>
              <div class="hero-stat"><strong>${sessionTotal}</strong><span>网页会话</span></div>
              <div class="hero-stat"><strong>${platformTotal}</strong><span>接入渠道</span></div>
            </div>
          </aside>
        </div>
        <section class="collection" aria-labelledby="collection-title">
          <div class="section-title-row">
            <h2 id="collection-title">Agent 档案</h2>
            <p>选择一位 Agent，继续上次的工作</p>
          </div>
          <div class="agent-grid">
            ${state.agents.map(renderAgentCard).join("")}
            <button class="add-agent-card" type="button" id="add-agent">
              <span class="plus" aria-hidden="true">+</span>
              <strong>添加 Agent</strong>
              <span>在配置中定义模型和 Soul，重启后自动出现</span>
            </button>
          </div>
        </section>
      </section>
    </div>`;
  bindCommonActions();
  document.querySelector("#add-agent")?.addEventListener("click", () => agentDialog.showModal());
}

function renderAgentCard(agent, index) {
  const href = `#/agents/${encodeURIComponent(agent.id)}`;
  const modelCount = agent.models.length;
  const platformCount = agent.connected_via.length;
  return `
    <a class="agent-card" data-tone="${index % 4}" href="${href}" aria-label="打开 ${escapeHTML(agent.name)}">
      <div class="agent-card-top">
        <span class="agent-index">A·${String(index + 1).padStart(2, "0")}</span>
        <span class="agent-orbit" aria-hidden="true">${initials(agent.name)}</span>
      </div>
      <div class="agent-card-body">
        <h3>${escapeHTML(agent.name)}</h3>
        <p>${escapeHTML(agent.soul_preview || "尚未填写 Soul")}</p>
        <div class="agent-meta">
          <span class="mini-chip">${compactModel(agent.default_model)}</span>
          <span class="mini-chip">${modelCount} 个模型</span>
          <span class="mini-chip">${platformCount} 个渠道</span>
        </div>
      </div>
      <div class="agent-card-footer">
        <span>${agent.session_count} 段会话 · ${formatRelative(agent.last_active_at)}</span>
        <strong>开始对话 ${icons.arrowRight}</strong>
      </div>
    </a>`;
}

function sidebarMarkup(isSettings) {
  return `
    <aside class="session-sidebar" aria-label="会话导航">
      <div class="sidebar-top">
        <a class="back-link" href="#/">${icons.arrowLeft} 返回 Agent 工作台</a>
        <div class="workspace-agent">
          <div class="workspace-avatar" aria-hidden="true">${initials(state.agent.name)}</div>
          <div><h2>${escapeHTML(state.agent.name)}</h2><p><span class="live-dot"></span>运行中 · ${compactModel(state.agent.default_model)}</p></div>
        </div>
      </div>
      <button class="new-session-button" type="button" id="new-session">
        <span>${icons.plus} 新会话</span><kbd>⌘ K</kbd>
      </button>
      <nav class="session-nav" aria-label="会话记录">
        <p class="session-nav-label">最近会话</p>
        ${state.sessions.length ? state.sessions.map(renderSessionItem).join("") : `<p class="sidebar-empty">还没有会话记录<br>从一个新问题开始吧</p>`}
      </nav>
      <div class="sidebar-bottom">
        <button class="sidebar-settings ${isSettings ? "active" : ""}" type="button" id="open-settings">${icons.settings}<span>Agent 设置</span></button>
      </div>
    </aside>
    <button class="sidebar-scrim" type="button" aria-label="关闭会话侧栏"></button>`;
}

function renderSessionItem(session) {
  const active = state.session?.key === session.key;
  return `
    <button class="session-item ${active ? "active" : ""}" type="button" data-session-key="${escapeHTML(session.key)}" ${active ? 'aria-current="page"' : ""}>
      <strong>${escapeHTML(session.title)}</strong>
      <span>${escapeHTML(session.preview)}</span>
    </button>`;
}

function renderWorkspace() {
  if (!state.agent) return;
  const isSettings = location.hash.endsWith("/settings");
  app.innerHTML = `
    <div class="workspace ${state.sidebarOpen ? "sidebar-open" : ""}">
      ${sidebarMarkup(isSettings)}
      <section class="workspace-main">
        ${workspaceHeader(isSettings)}
        ${isSettings ? settingsMarkup() : chatMarkup()}
      </section>
    </div>`;
  bindWorkspaceActions(isSettings);
  if (!isSettings) {
    requestAnimationFrame(() => {
      const scroll = document.querySelector(".message-scroll");
      if (scroll) scroll.scrollTop = scroll.scrollHeight;
      document.querySelector("#message-input")?.focus({ preventScroll: true });
    });
  }
}

function workspaceHeader(isSettings) {
  const currentModel = state.session?.model_ref || state.agent.default_model;
  return `
    <header class="workspace-header">
      <button class="icon-button mobile-menu" type="button" id="mobile-menu" aria-label="打开会话侧栏">${icons.menu}</button>
      <div class="header-title">
        <h1>${isSettings ? "Agent 设置" : escapeHTML(state.session?.title || `与 ${state.agent.name} 对话`)}</h1>
        <p>${isSettings ? "定义它如何思考、行动与连接" : escapeHTML(state.session?.preview || "开始一段新的协作")}</p>
      </div>
      <div class="workspace-header-actions">
        ${!isSettings && state.session ? `
          <label class="model-picker">
            ${icons.spark}<span class="sr-only">会话模型</span>
            <select id="model-select" aria-label="选择当前会话模型" ${state.busy ? "disabled" : ""}>
              ${state.agent.models.map((model) => `<option value="${escapeHTML(model)}" ${model === currentModel ? "selected" : ""}>${compactModel(model)}</option>`).join("")}
            </select>
            ${icons.chevronDown}
          </label>` : ""}
        <button class="icon-button header-settings-button" type="button" id="header-settings" aria-label="${isSettings ? "返回聊天" : "打开设置"}">${isSettings ? icons.message : icons.settings}</button>
        ${themeButton()}
      </div>
    </header>`;
}

function chatMarkup() {
  const hasMessages = state.session?.messages?.length;
  return `
    <div class="chat-stage">
      <div class="message-scroll">
        <div class="message-list" aria-live="polite">
          ${hasMessages ? state.session.messages.map(renderMessage).join("") : welcomeMarkup()}
          ${state.busy ? `<div class="thinking-row"><div class="thinking-dots" aria-hidden="true"><span></span><span></span><span></span></div>${escapeHTML(state.agent.name)} 正在思考</div>` : ""}
        </div>
      </div>
      <div class="composer-wrap">
        <form class="composer" id="composer">
          <div class="composer-row">
            <textarea id="message-input" rows="1" maxlength="20000" aria-label="发送给 Agent 的消息" placeholder="${state.session ? `给 ${escapeHTML(state.agent.name)} 发送消息…` : "先创建一段新会话…"}" ${!state.session || state.busy ? "disabled" : ""}></textarea>
            <button class="send-button ${state.busy ? "stop" : ""}" type="${state.busy ? "button" : "submit"}" aria-label="${state.busy ? "停止生成" : "发送消息"}" ${!state.session ? "disabled" : ""}>${state.busy ? icons.stop : icons.send}</button>
          </div>
          <div class="composer-meta"><span>Enter 发送 · Shift + Enter 换行</span><span>AI 可能会犯错，请核对重要信息</span></div>
        </form>
      </div>
    </div>`;
}

function welcomeMarkup() {
  return `
    <section class="welcome-card">
      <div class="agent-orbit" aria-hidden="true">${initials(state.agent.name)}</div>
      <p class="eyebrow">${state.session ? "一段空白会话" : "还没有网页会话"}</p>
      <h2>${state.session ? `今天想让 ${escapeHTML(state.agent.name)} 做什么？` : `先为 ${escapeHTML(state.agent.name)} 开一张新纸。`}</h2>
      <p>${escapeHTML(state.agent.soul_preview)} 每一段会话都会独立保存，你可以随时从左侧回来继续。</p>
      <div class="empty-actions">
        ${state.session ? `<button class="button button-secondary prompt-chip" type="button" data-prompt="先介绍一下你能帮我做什么。">了解它的能力</button><button class="button button-secondary prompt-chip" type="button" data-prompt="帮我把今天最重要的任务梳理成一份行动清单。">规划今天</button>` : `<button class="button button-primary" type="button" id="welcome-new-session">${icons.plus} 创建第一段会话</button>`}
      </div>
    </section>`;
}

function renderMessage(message) {
  if (message.role === "tool") {
    return `<details class="tool-message"><summary>工具执行 · ${escapeHTML(message.tool_name || "运行结果")}</summary><pre>${escapeHTML(message.content)}</pre></details>`;
  }
  if (message.role === "user") {
    return `<article class="message-row user"><div class="message-bubble">${escapeHTML(message.content)}</div></article>`;
  }
  return `
    <article class="message-row assistant">
      <div class="message-avatar" aria-hidden="true">${initials(state.agent.name)}</div>
      <div class="message-content">
        <div class="message-heading"><strong>${escapeHTML(state.agent.name)}</strong><time>刚刚</time></div>
        <div class="message-bubble">${escapeHTML(message.content)}</div>
      </div>
    </article>`;
}

function settingsMarkup() {
  return `
    <div class="settings-stage">
      <div class="settings-shell">
        <div class="settings-heading">
          <div><p class="eyebrow">Agent control room</p><h2>塑造 ${escapeHTML(state.agent.name)}</h2></div>
          <p>设定它是谁、能调用什么，以及在哪些平台与你见面。</p>
        </div>
        <div class="settings-tabs" role="tablist" aria-label="Agent 设置分类">
          ${settingsTab("soul", "Soul")}
          ${settingsTab("mcp", "MCP")}
          ${settingsTab("channels", "连接平台")}
        </div>
        ${settingsPanel()}
      </div>
    </div>`;
}

function settingsTab(id, label) {
  const active = state.settingsTab === id;
  return `<button class="settings-tab ${active ? "active" : ""}" type="button" role="tab" aria-selected="${active}" data-settings-tab="${id}">${label}</button>`;
}

function settingsPanel() {
  if (state.settingsTab === "mcp") return mcpPanel();
  if (state.settingsTab === "channels") return channelsPanel();
  return soulPanel();
}

function soulPanel() {
  return `
    <section class="settings-panel" role="tabpanel">
      <div class="setting-card">
        <div class="setting-card-heading">
          <div><h3>它的 Soul</h3><p>这段文字会作为系统提示，在每次模型调用前重新读取。</p></div>
          <span class="mini-chip">实时生效</span>
        </div>
        <textarea class="soul-editor" id="soul-editor" maxlength="20000" aria-label="Agent Soul 内容">${escapeHTML(state.soulDraft)}</textarea>
        <div class="editor-footer"><span><span id="soul-count">${[...state.soulDraft].length}</span> / 20000 字 · 保存后影响下一条消息</span><button class="button button-primary" type="button" id="save-soul">保存 Soul</button></div>
      </div>
    </section>`;
}

function mcpPanel() {
  return `
    <section class="settings-panel" role="tabpanel">
      <div class="future-banner">
        <div><p class="eyebrow">后端能力预留</p><h3>给 Agent 装上新工具。</h3><p>MCP 服务管理尚未在后端实现。这里先保留连接入口、权限提示和服务状态，后续接口接入时无需重做页面结构。</p></div>
        <div class="future-mark" aria-hidden="true">MCP<br>soon</div>
      </div>
      <div class="mcp-grid">
        ${mcpCard("FS", "文件与知识库", "读取本地文档和团队资料")}
        ${mcpCard("DB", "数据与查询", "连接数据库和分析服务")}
        ${mcpCard("API", "业务工具", "接入内部系统与开放 API")}
        ${mcpCard("+", "自定义服务", "使用 stdio 或 HTTP transport")}
      </div>
    </section>`;
}

function mcpCard(icon, name, description) {
  return `<div class="mcp-card"><div class="mcp-icon" aria-hidden="true">${icon}</div><div><strong>${name}</strong><span>${description}</span></div></div>`;
}

function channelsPanel() {
  const connected = new Map(state.agent.connected_via.map((item) => [item.channel.toLowerCase(), item]));
  return `
    <section class="settings-panel" role="tabpanel">
      <div class="setting-card-heading"><div><h3>把它带到更多地方</h3><p>网页和已配置渠道会显示为已连接；其他平台等待后端适配器接入。</p></div></div>
      <div class="platform-grid">
        ${platformCard("web", "WEB", "网页工作台", "当前浏览器中的直接对话", connected.get("web"), true)}
        ${platformCard("terminal", ">_", "本地终端", "在命令行中与 Agent 对话", connected.get("terminal"), Boolean(connected.get("terminal")))}
        ${platformCard("feishu", "飞", "飞书", "群聊、私聊与话题线程", connected.get("feishu"), false)}
        ${platformCard("telegram", "TG", "Telegram", "Bot 私聊和群组消息", connected.get("telegram"), false)}
      </div>
    </section>`;
}

function platformCard(id, icon, name, description, connection, builtIn) {
  const connected = Boolean(connection);
  const status = connected ? `已连接${connection.account_id ? ` · ${escapeHTML(connection.account_id)}` : ""}` : builtIn ? "等待配置" : "后端待实现";
  return `
    <article class="platform-card" data-platform="${id}">
      <div class="platform-heading"><div class="platform-icon" aria-hidden="true">${icon}</div><div><h3>${name}</h3><p>${description}</p></div></div>
      <div class="platform-card-footer"><span class="connection-status ${connected ? "connected" : ""}"><span class="live-dot"></span>${status}</span><button class="button button-secondary" type="button" disabled>${connected ? "管理" : "连接"}</button></div>
    </article>`;
}

function bindCommonActions() {
  document.querySelector(".theme-toggle")?.addEventListener("click", toggleTheme);
}

function bindWorkspaceActions(isSettings) {
  bindCommonActions();
  document.querySelector("#mobile-menu")?.addEventListener("click", () => {
    abortActiveStream();
    state.sidebarOpen = true;
    renderWorkspace();
  });
  document.querySelector(".sidebar-scrim")?.addEventListener("click", () => {
    abortActiveStream();
    state.sidebarOpen = false;
    renderWorkspace();
  });
  document.querySelector("#new-session")?.addEventListener("click", createSession);
  document.querySelector("#welcome-new-session")?.addEventListener("click", createSession);
  document.querySelectorAll("[data-session-key]").forEach((button) => {
    button.addEventListener("click", () => openSession(button.dataset.sessionKey));
  });
  document.querySelector("#open-settings")?.addEventListener("click", () => {
    state.sidebarOpen = false;
    location.hash = `#/agents/${encodeURIComponent(state.agent.id)}/settings`;
  });
  document.querySelector("#header-settings")?.addEventListener("click", () => {
    location.hash = isSettings ? `#/agents/${encodeURIComponent(state.agent.id)}` : `#/agents/${encodeURIComponent(state.agent.id)}/settings`;
  });
  if (isSettings) bindSettingsActions();
  else bindChatActions();
}

function bindChatActions() {
  document.querySelector("#composer")?.addEventListener("submit", sendCurrentMessage);
  document.querySelector(".send-button.stop")?.addEventListener("click", stopCurrentStream);
  const input = document.querySelector("#message-input");
  input?.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      input.form.requestSubmit();
    }
  });
  input?.addEventListener("input", () => {
    input.style.height = "auto";
    input.style.height = `${Math.min(input.scrollHeight, 180)}px`;
  });
  document.querySelectorAll(".prompt-chip").forEach((button) => {
    button.addEventListener("click", () => {
      if (!input) return;
      input.value = button.dataset.prompt;
      input.focus();
    });
  });
  document.querySelector("#model-select")?.addEventListener("change", updateModel);
}

function bindSettingsActions() {
  document.querySelectorAll("[data-settings-tab]").forEach((button) => {
    button.addEventListener("click", () => {
      state.settingsTab = button.dataset.settingsTab;
      renderWorkspace();
    });
  });
  const editor = document.querySelector("#soul-editor");
  editor?.addEventListener("input", () => {
    state.soulDraft = editor.value;
    const count = document.querySelector("#soul-count");
    if (count) count.textContent = [...state.soulDraft].length;
  });
  document.querySelector("#save-soul")?.addEventListener("click", saveSoul);
}

async function createSession() {
  if (state.busy) return;
  state.busy = true;
  try {
    const created = await api(`/api/agents/${encodeURIComponent(state.agent.id)}/sessions`, { method: "POST" });
    state.session = created;
    await refreshSessions();
    state.sidebarOpen = false;
    location.hash = `#/agents/${encodeURIComponent(state.agent.id)}`;
  } catch (error) {
    showToast(error.message, true);
  } finally {
    state.busy = false;
    renderWorkspace();
  }
}

async function openSession(key) {
  if (state.busy) return;
  state.busy = true;
  try {
    state.session = await api(`/api/agents/${encodeURIComponent(state.agent.id)}/sessions/${encodeURIComponent(key)}`);
    state.sidebarOpen = false;
    if (location.hash.endsWith("/settings")) location.hash = `#/agents/${encodeURIComponent(state.agent.id)}`;
  } catch (error) {
    showToast(error.message, true);
  } finally {
    state.busy = false;
    renderWorkspace();
  }
}

async function sendCurrentMessage(event) {
  event.preventDefault();
  const input = document.querySelector("#message-input");
  const text = input?.value.trim();
  if (!text || !state.session || state.busy) return;
  const agentID = state.agent.id;
  const sessionKey = state.session.key;
  const controller = new AbortController();
  state.streamAbort = controller;
  state.busy = true;
  state.session.messages = [...(state.session.messages || []), { role: "user", content: text }];
  renderWorkspace();
  let finalReceived = false;
  try {
    const response = await fetch(`/api/agents/${encodeURIComponent(agentID)}/sessions/${encodeURIComponent(sessionKey)}/messages/stream`, {
      method: "POST",
      body: JSON.stringify({ text }),
      headers: { "Content-Type": "application/json" },
      signal: controller.signal,
    });
    if (!response.ok) {
      const payload = await response.json().catch(() => ({}));
      throw new Error(payload.error || `请求失败（${response.status}）`);
    }
    await readSSE(response, (streamEvent) => {
      if (streamEvent.type === "final") finalReceived = true;
      handleChatStreamEvent(streamEvent);
    });
    if (!finalReceived) throw new Error("流式响应提前结束");
  } catch (error) {
    if (error.name === "AbortError") showToast("已停止生成");
    else showToast(error.message, true);
  } finally {
    try {
      if (state.agent?.id === agentID) {
        const latest = await api(`/api/agents/${encodeURIComponent(agentID)}/sessions/${encodeURIComponent(sessionKey)}`);
        if (state.session?.key === sessionKey) state.session = latest;
        await refreshSessions();
      }
    } catch (error) {
      showToast(error.message, true);
    }
    if (state.streamAbort === controller) {
      state.streamAbort = null;
      state.busy = false;
    }
    if (state.agent?.id === agentID && state.session?.key === sessionKey && !location.hash.endsWith("/settings")) {
      renderWorkspace();
    }
  }
}

function stopCurrentStream() {
  state.streamAbort?.abort();
}

function abortActiveStream() {
  if (state.streamAbort && !state.streamAbort.signal.aborted) state.streamAbort.abort();
}

async function readSSE(response, onEvent) {
  if (!response.body) throw new Error("浏览器不支持流式响应");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (true) {
      const { value, done } = await reader.read();
      buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
      let separator = buffer.match(/\r?\n\r?\n/);
      while (separator) {
        const frame = buffer.slice(0, separator.index);
        buffer = buffer.slice(separator.index + separator[0].length);
        const data = frame
          .split(/\r?\n/)
          .filter((line) => line.startsWith("data:"))
          .map((line) => line.slice(5).trimStart())
          .join("\n");
        if (data && data !== "[DONE]") onEvent(JSON.parse(data));
        separator = buffer.match(/\r?\n\r?\n/);
      }
      if (done) break;
    }
  } catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
}

function handleChatStreamEvent(streamEvent) {
  const data = streamEvent.data || {};
  switch (streamEvent.type) {
    case "token_delta":
      removeThinkingRow();
      appendAssistantDelta(data.delta || "");
      break;
    case "tool_start":
      removeThinkingRow();
      showToolStart(data);
      break;
    case "tool_result":
      showToolResult(data);
      break;
    case "final":
      removeThinkingRow();
      if (!document.querySelector("[data-stream-assistant]") && data.content) appendAssistantDelta(data.content);
      break;
    case "error":
      throw new Error(data.message || "消息处理失败");
  }
  scrollChatToBottom();
}

function removeThinkingRow() {
  document.querySelector(".thinking-row")?.remove();
}

function appendAssistantDelta(delta) {
  if (!delta) return;
  let row = document.querySelector("[data-stream-assistant]");
  if (!row) {
    row = document.createElement("article");
    row.className = "message-row assistant";
    row.dataset.streamAssistant = "true";
    const avatar = document.createElement("div");
    avatar.className = "message-avatar";
    avatar.setAttribute("aria-hidden", "true");
    avatar.textContent = [...(state.agent?.name || "AI")].slice(0, 2).join("").toUpperCase();
    const content = document.createElement("div");
    content.className = "message-content";
    const heading = document.createElement("div");
    heading.className = "message-heading";
    const name = document.createElement("strong");
    name.textContent = state.agent?.name || "Agent";
    const time = document.createElement("time");
    time.textContent = "刚刚";
    heading.append(name, time);
    const bubble = document.createElement("div");
    bubble.className = "message-bubble";
    bubble.dataset.streamText = "true";
    content.append(heading, bubble);
    row.append(avatar, content);
    appendStreamNode(row);
  }
  const bubble = row.querySelector("[data-stream-text]");
  if (bubble) bubble.textContent += delta;
}

function showToolStart(data) {
  const details = document.createElement("details");
  details.className = "tool-message running";
  details.dataset.toolCallId = data.tool_call_id || "";
  details.open = true;
  const summary = document.createElement("summary");
  summary.textContent = `工具执行 · ${data.tool_name || "运行中"} · 执行中`;
  const result = document.createElement("pre");
  result.textContent = data.arguments || "等待工具参数";
  details.append(summary, result);
  appendStreamNode(details);
}

function showToolResult(data) {
  const details = [...document.querySelectorAll(".tool-message[data-tool-call-id]")]
    .find((item) => item.dataset.toolCallId === (data.tool_call_id || ""));
  if (!details) return;
  details.classList.remove("running");
  details.classList.add(data.success ? "succeeded" : "failed");
  details.querySelector("summary").textContent = `工具执行 · ${data.tool_name || "运行结果"} · ${data.success ? "已完成" : "失败"}`;
  details.querySelector("pre").textContent = data.result || "";
}

function appendStreamNode(node) {
  const list = document.querySelector(".message-list");
  if (!list) return;
  const thinking = list.querySelector(".thinking-row");
  list.insertBefore(node, thinking);
}

function scrollChatToBottom() {
  const scroll = document.querySelector(".message-scroll");
  if (scroll) scroll.scrollTop = scroll.scrollHeight;
}

async function updateModel(event) {
  if (!state.session || state.busy) return;
  state.busy = true;
  const selected = event.target.value;
  try {
    state.session = await api(`/api/agents/${encodeURIComponent(state.agent.id)}/sessions/${encodeURIComponent(state.session.key)}/model`, {
      method: "PUT",
      body: JSON.stringify({ model_ref: selected }),
    });
    await refreshSessions();
    showToast(`当前会话已切换到 ${selected}`);
  } catch (error) {
    showToast(error.message, true);
  } finally {
    state.busy = false;
    renderWorkspace();
  }
}

async function saveSoul() {
  const soul = state.soulDraft.trim();
  if (!soul) {
    showToast("Soul 不能为空", true);
    return;
  }
  const button = document.querySelector("#save-soul");
  button.disabled = true;
  button.textContent = "保存中…";
  try {
    const result = await api(`/api/agents/${encodeURIComponent(state.agent.id)}/soul`, {
      method: "PUT",
      body: JSON.stringify({ soul }),
    });
    state.soulDraft = result.soul;
    state.agent.soul = result.soul;
    state.agent.soul_preview = result.soul.split(/\s+/).join(" ").slice(0, 88);
    showToast("Soul 已保存，将从下一条消息开始生效");
  } catch (error) {
    showToast(error.message, true);
  } finally {
    renderWorkspace();
  }
}

async function refreshSessions() {
  const payload = await api(`/api/agents/${encodeURIComponent(state.agent.id)}/sessions`);
  state.sessions = payload.sessions;
}

async function loadHome() {
  app.innerHTML = `<div class="boot-screen" role="status"><div class="brand-mark" aria-hidden="true"><span>LC</span></div><p>正在整理 Agent 档案…</p></div>`;
  const payload = await api("/api/agents");
  state.agents = payload.agents;
  renderHome();
}

async function loadAgent(agentID) {
  app.innerHTML = `<div class="boot-screen" role="status"><div class="brand-mark" aria-hidden="true"><span>LC</span></div><p>正在进入 Agent 工作区…</p></div>`;
  const encodedID = encodeURIComponent(agentID);
  const [agent, sessionPayload] = await Promise.all([
    api(`/api/agents/${encodedID}`),
    api(`/api/agents/${encodedID}/sessions`),
  ]);
  state.agent = agent;
  state.soulDraft = agent.soul || "";
  state.sessions = sessionPayload.sessions;
  state.session = state.sessions.length
    ? await api(`/api/agents/${encodedID}/sessions/${encodeURIComponent(state.sessions[0].key)}`)
    : null;
  state.sidebarOpen = false;
  renderWorkspace();
}

async function route() {
  const parts = location.hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  try {
    if (parts[0] === "agents" && parts[1]) await loadAgent(decodeURIComponent(parts[1]));
    else await loadHome();
  } catch (error) {
    app.innerHTML = `<div class="error-screen"><div class="brand-mark" aria-hidden="true"><span>!</span></div><h1>工作台暂时无法打开</h1><p>${escapeHTML(error.message)}</p><button class="button button-primary" type="button" id="retry">重新加载</button></div>`;
    document.querySelector("#retry")?.addEventListener("click", route);
  }
}

document.querySelector("#copy-config")?.addEventListener("click", async () => {
  const sample = document.querySelector(".config-sample code")?.textContent || "";
  try {
    await navigator.clipboard.writeText(sample);
    showToast("配置示例已复制");
    agentDialog.close();
  } catch {
    showToast("复制失败，请手动选择代码", true);
  }
});

window.addEventListener("hashchange", () => {
  abortActiveStream();
  route();
});
window.addEventListener("pagehide", abortActiveStream);
window.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k" && state.agent) {
    event.preventDefault();
    createSession();
  }
});

const savedTheme = localStorage.getItem("luckyclaw-theme");
applyTheme(savedTheme || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"));
route();

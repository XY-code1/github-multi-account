/* GitHub 多账号运维中心 —— 前端逻辑 */
"use strict";

// ---------------- 工具 ----------------

function escapeHtml(str) {
  return String(str ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function safeUrl(url) {
  const s = String(url || "").trim().toLowerCase();
  if (s.startsWith("https://") || s.startsWith("http://") || s.startsWith("/")) {
    return escapeHtml(url);
  }
  return "#";
}

function repoName(url) {
  if (!url) return "";
  const parts = (url || "").replace(/\/$/, "").split("/");
  return parts.slice(-2).join("/");
}

function relTime(iso) {
  if (!iso) return "";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const diff = Date.now() - then;
  const min = Math.floor(diff / 60000);
  if (min < 1) return "刚刚";
  if (min < 60) return `${min} 分钟前`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h} 小时前`;
  const d = Math.floor(h / 24);
  if (d < 30) return `${d} 天前`;
  const m = Math.floor(d / 30);
  if (m < 12) return `${m} 个月前`;
  return `${Math.floor(m / 12)} 年前`;
}

function statusText(s) {
  const map = { active: "正常", invalid: "失效", error: "异常", warning: "警告", sync_failed: "同步失败", sync_stale: "长期未同步", unknown: "未检查" };
  return escapeHtml(map[s] || s);
}

function showToast(msg) {
  const toast = document.getElementById("toast");
  toast.textContent = msg;
  toast.classList.remove("hidden");
  clearTimeout(showToast._t);
  showToast._t = setTimeout(() => toast.classList.add("hidden"), 2600);
}

const AVATAR_FALLBACK =
  "data:image/svg+xml," +
  encodeURIComponent(
    "<svg xmlns='http://www.w3.org/2000/svg' width='40' height='40'><rect width='40' height='40' rx='20' fill='#eaeef2'/></svg>"
  );

function handleImgError(img) {
  img.onerror = null;
  img.src = AVATAR_FALLBACK;
}

async function api(path, options = {}) {
  const resp = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.detail || `请求失败（${resp.status}）`);
  return data;
}

// ---------------- 主题 ----------------

const THEME_KEY = "gh-theme";

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  const btn = document.getElementById("themeBtn");
  if (btn) btn.textContent = theme === "dark" ? "◑" : "◐";
}

function toggleTheme() {
  const cur = document.documentElement.dataset.theme === "dark" ? "dark" : "light";
  const next = cur === "dark" ? "light" : "dark";
  applyTheme(next);
  try { localStorage.setItem(THEME_KEY, next); } catch (e) { /* ignore */ }
}

// ---------------- 全局状态 ----------------

const state = {
  page: "overview",
  summary: null,
  tasks: [],
  activities: [],
  accounts: { items: [], total: 0, page: 1, page_size: 50, total_pages: 1, groups: [] },
  accountQuery: { query: "", status: "", type: "", group: "", tag: "", sort: "last_activity", order: "desc" },
  selected: new Set(),
  detail: null,
  detailCache: {},
  detailTab: "overview",
  history: [],
  metric: "followers",
  notifications: false,
  prefetchEnabled: true,
  collapsedGroups: new Set(),
  lastRefresh: null,
};

// ---------------- 路由 ----------------

const SUBTITLES = {
  overview: "账号总览",
  accounts: "全部账号",
  todos: "统一待办",
  automation: "自动化任务",
  detail: "账号详情",
};

function go(page) {
  state.page = page;
  document.querySelectorAll(".page").forEach((p) => p.classList.toggle("active", p.id === `page-${page}`));
  document.querySelectorAll(".nav-item").forEach((n) => n.classList.toggle("active", n.dataset.page === page));
  document.getElementById("subtitle").textContent = SUBTITLES[page] || "";
  const scrollers = document.querySelectorAll(".main");
  scrollers.forEach((s) => (s.scrollTop = 0));
  if (page === "overview") loadOverview();
  if (page === "accounts") loadAccountsPage();
  if (page === "todos") loadTodos();
  if (page === "automation") loadAutomation();
}

// ---------------- 账号总览 ----------------

async function loadOverview() {
  const cards = document.getElementById("summaryCards");
  cards.innerHTML = `<div class="empty-tab">加载中…</div>`;
  try {
    const [summary, taskData] = await Promise.all([
      api("/api/dashboard/summary"),
      api("/api/dashboard/tasks"),
    ]);
    state.summary = summary;
    state.tasks = taskData.tasks;
    renderSummary(summary);
    renderOverviewTasks(taskData.tasks);
    await loadOverviewActivities("all");
    document.getElementById("todoBadge").textContent = taskData.total;
    document.getElementById("todoBadge").classList.toggle("hidden", taskData.total === 0);
    updateLastRefresh();
  } catch (e) {
    cards.innerHTML = `<div class="empty-tab">加载失败：${escapeHtml(e.message)} <button class="btn btn-sm" onclick="loadOverview()">重试</button></div>`;
  }
}

function renderSummary(s) {
  const cards = [
    { num: s.total, label: "账号总数", cls: "is-accent" },
    { num: s.healthy, label: "健康账号", cls: "is-success" },
    { num: s.attention, label: "需要关注", cls: s.attention ? "is-danger" : "" },
    { num: s.credential_risk, label: "凭据风险", cls: s.credential_risk ? "is-danger" : "" },
    { num: s.quota_low, label: "配额不足", cls: s.quota_low ? "is-warning" : "" },
    { num: s.review_prs, label: "待 Review PR", cls: "" },
    { num: s.issues, label: "待处理 Issue", cls: "" },
  ];
  document.getElementById("summaryCards").innerHTML = cards
    .map((c) => `<div class="stat-card ${c.cls}"><span class="stat-num">${c.num}</span><span class="stat-label">${c.label}</span></div>`)
    .join("");
  document.getElementById("lastRefresh").textContent = s.last_sync_at
    ? "最近同步 " + relTime(s.last_sync_at)
    : "尚未同步";
}

function updateLastRefresh() {
  const el = document.getElementById("lastRefresh");
  if (state.summary?.last_sync_at) el.textContent = "最近同步 " + relTime(state.summary.last_sync_at);
}

function renderOverviewTasks(tasks) {
  const box = document.getElementById("overviewTasks");
  const top = tasks.slice(0, 8);
  if (!top.length) {
    box.innerHTML = `<div class="empty-tab">当前没有需要处理的事项 ✓</div>`;
    return;
  }
  box.innerHTML = `<div class="task-list">${top.map(taskHtml).join("")}</div>`;
}

function taskHtml(t) {
  const icon = {
    credential: "🔑", quota: "📊", review_pr: "👀", my_pr: "📤",
    issue: "📋", sync_failed: "⚠", sync_stale: "🕘",
  }[t.type] || "•";
  return `
    <div class="task" data-priority="${t.priority}">
      <div class="task-icon">${icon}</div>
      <div class="task-body">
        <div class="task-title">${escapeHtml(t.title)}</div>
        <div class="task-meta">
          <span class="tag">${escapeHtml(t.type_label || t.type)}</span>
          <span class="tag">${escapeHtml(t.account_label)}</span>
          <span>${escapeHtml(t.detail)}</span>
          ${t.time ? `<span>${relTime(t.time)}</span>` : ""}
        </div>
      </div>
      <div class="task-actions">
        <span class="prio prio-${t.priority}">${{ high: "高", medium: "中", low: "低" }[t.priority]}</span>
        <button class="btn btn-sm" onclick="openAccountDetail(${t.account_id})">${escapeHtml(t.action)}</button>
      </div>
    </div>`;
}

async function loadOverviewActivities(type) {
  const box = document.getElementById("overviewActivities");
  try {
    const data = await api(`/api/dashboard/activities?type=${encodeURIComponent(type)}&limit=30`);
    state.activities = data.activities;
    box.innerHTML = state.activities.length
      ? `<div class="activity-list">${state.activities.map(activityHtml).join("")}</div>`
      : `<div class="empty-tab">最近没有动态</div>`;
  } catch (e) {
    box.innerHTML = `<div class="empty-tab">加载失败：${escapeHtml(e.message)}</div>`;
  }
}

function activityHtml(a) {
  return `
    <div class="activity">
      ${a.actor_avatar ? `<img class="activity-actor" src="${escapeHtml(a.actor_avatar)}" alt="" onerror="handleImgError(this)">` : ""}
      <div class="activity-body">
        <div><strong>${escapeHtml(a.actor || a.account_label)}</strong> ${escapeHtml(a.type)}${a.action ? " · " + escapeHtml(a.action) : ""}</div>
        <div class="task-meta"><span class="tag">${escapeHtml(a.account_label)}</span>${a.repo ? `<span class="tag">${escapeHtml(a.repo)}</span>` : ""}</div>
      </div>
      <span class="activity-time">${relTime(a.created_at)}</span>
    </div>`;
}

// ---------------- 全部账号 ----------------

function syncUrlFromState() {
  const p = new URLSearchParams();
  const q = state.accountQuery;
  if (q.query) p.set("query", q.query);
  if (q.status) p.set("status", q.status);
  if (q.type) p.set("type", q.type);
  if (q.group) p.set("group", q.group);
  if (q.tag) p.set("tags", q.tag);
  p.set("sort", q.sort);
  p.set("order", q.order);
  p.set("page", state.accounts.page);
  p.set("pageSize", state.accounts.page_size);
  return p.toString();
}

async function loadAccountsPage(pushUrl = true) {
  const body = document.getElementById("accountTableBody");
  const loading = document.getElementById("accountTableLoading");
  const empty = document.getElementById("accountTableEmpty");
  loading.classList.remove("hidden");
  empty.classList.add("hidden");
  body.innerHTML = "";
  if (pushUrl) history.replaceState(null, "", `/?${syncUrlFromState()}`);
  try {
    const p = syncUrlFromState();
    const data = await api(`/api/accounts?${p}`);
    state.accounts = data;
    state.selected.clear();
    renderAccountTable(data.items);
    document.getElementById("accountsDesc").textContent =
      `共 ${data.total} 个账号${state.accountQuery.query ? `，匹配 “${state.accountQuery.query}”` : ""}`;
    document.getElementById("pagerInfo").textContent =
      `第 ${data.page} 页 / 共 ${data.total_pages} 页（共 ${data.total} 个账号，每页 ${data.page_size} 条）`;
    document.getElementById("prevPage").disabled = data.page <= 1;
    document.getElementById("nextPage").disabled = data.page >= data.total_pages;
    renderGroupFilter(data.groups);
    renderTagFilter(data.tags || []);
    loading.classList.add("hidden");
    if (!data.items.length) empty.classList.remove("hidden");
    updateBatchBar();
  } catch (e) {
    loading.classList.add("hidden");
    body.innerHTML = `<tr><td colspan="10"><div class="empty-tab">加载失败：${escapeHtml(e.message)} <button class="btn btn-sm" onclick="loadAccountsPage()">重试</button></div></td></tr>`;
  }
}

function renderAccountTable(items) {
  const body = document.getElementById("accountTableBody");
  body.innerHTML = items.map((a) => {
    const pct = a.quota_limit ? Math.round((a.remaining / a.quota_limit) * 100) : 0;
    const low = pct < 10 ? "is-low" : pct < 30 ? "is-mid" : "";
    return `
    <tr data-id="${a.id}" class="${state.selected.has(a.id) ? "is-selected" : ""} ${a.status === "invalid" || a.status === "error" ? "row-invalid" : ""}">
      <td class="col-check"><input type="checkbox" data-check="${a.id}" ${state.selected.has(a.id) ? "checked" : ""}></td>
      <td>
        <div class="cell-user">
          <img src="${a.avatar_url || ""}" alt="" onerror="handleImgError(this)">
          <div>
            <div class="cell-name">${escapeHtml(a.username)}</div>
            <div class="cell-sub">${escapeHtml(a.label || "")}${a.alias ? " · " + escapeHtml(a.alias) : ""}</div>
          </div>
        </div>
      </td>
      <td>
        <span class="tag">${{ personal: "个人", bot: "Bot", org: "组织" }[a.type] || "个人"}</span>
        ${a.tags.map((t) => `<span class="tag">${escapeHtml(t)}</span>`).join("")}
      </td>
      <td>
        <span class="status-badge ${a.status}">${statusText(a.status)}</span>
        ${a.error ? `<div class="cell-sub">${escapeHtml(a.error)}</div>` : ""}
      </td>
      <td>
        <div class="progress ${low}"><span style="width:${pct}%"></span></div>
        <span class="cell-sub">${a.remaining}/${a.quota_limit || "?"}</span>
      </td>
      <td>${a.review_prs}</td>
      <td>${a.assigned_issues}</td>
      <td>${a.last_sync_at ? relTime(a.last_sync_at) : "—"}</td>
      <td>${a.sync_enabled === false ? '<span class="tag">已暂停</span>' : '<span class="tag">已启用</span>'}</td>
      <td class="col-op">
        <button class="btn btn-sm" onclick="openAccountDetail(${a.id})">详情</button>
        <button class="btn btn-sm" onclick="syncAccount(${a.id})">同步</button>
      </td>
    </tr>`;
  }).join("");

  body.querySelectorAll("tr").forEach((tr) => {
    tr.addEventListener("click", (e) => {
      if (e.target.closest("input,button")) return;
      openAccountDetail(Number(tr.dataset.id));
    });
  });
  body.querySelectorAll("[data-check]").forEach((cb) => {
    cb.addEventListener("change", () => {
      const id = Number(cb.dataset.check);
      if (cb.checked) state.selected.add(id);
      else state.selected.delete(id);
      updateBatchBar();
    });
  });
}

function renderGroupFilter(groups) {
  const sel = document.getElementById("filterGroup");
  const cur = sel.value;
  sel.innerHTML = `<option value="">全部分组</option>` +
    groups.map((g) => `<option value="${escapeHtml(g)}">${escapeHtml(g)}</option>`).join("");
  sel.value = cur;
}

function renderTagFilter(tags) {
  const sel = document.getElementById("filterTag");
  const cur = sel.value;
  sel.innerHTML = `<option value="">全部标签</option>` +
    tags.map((t) => `<option value="${escapeHtml(t)}">${escapeHtml(t)}</option>`).join("");
  sel.value = cur;
}

function updateBatchBar() {
  const bar = document.getElementById("batchBar");
  const n = state.selected.size;
  bar.classList.toggle("hidden", n === 0);
  document.getElementById("selectedCount").textContent = `已选 ${n} 个`;
}

async function syncAccount(id) {
  try {
    await api(`/api/accounts/${id}/sync`, { method: "POST" });
    showToast("已同步");
    await loadAccountsPage(false);
    if (state.page === "detail" && state.detail && state.detail.id === id) await openAccountDetail(id);
  } catch (e) {
    showToast(e.message);
  }
}

async function batchRefresh() {
  if (!state.selected.size) return;
  try {
    await api("/api/accounts/batch-refresh", {
      method: "POST",
      body: JSON.stringify({ ids: [...state.selected] }),
    });
    showToast(`已刷新 ${state.selected.size} 个账号`);
    await loadAccountsPage(false);
  } catch (e) {
    showToast(e.message);
  }
}

async function batchExport() {
  if (!state.selected.size) return;
  try {
    const data = await api(`/api/accounts?pageSize=200&query=&sort=username&order=asc`);
    const ids = new Set(state.selected);
    const rows = data.items.filter((a) => ids.has(a.id));
    const payload = {
      exported_at: new Date().toISOString(),
      note: "账号信息备份（不含凭据 / Token）",
      accounts: rows.map((a) => ({
        username: a.username, label: a.label, alias: a.alias, type: a.type,
        group: a.group, tags: a.tags, note: a.note, status: a.status,
      })),
    };
    downloadJson(payload, `github-accounts-${rows.length}.json`);
    showToast(`已导出 ${rows.length} 个账号信息（不含凭据）`);
  } catch (e) {
    showToast(e.message);
  }
}

function downloadJson(obj, filename) {
  const blob = new Blob([JSON.stringify(obj, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// ---------------- 统一待办 ----------------

async function loadTodos() {
  const box = document.getElementById("todoList");
  box.innerHTML = `<div class="empty-tab">加载中…</div>`;
  try {
    const params = new URLSearchParams();
    const p = document.getElementById("todoPriority").value;
    const t = document.getElementById("todoType").value;
    const acc = document.getElementById("todoAccount").value;
    if (p) params.set("priority", p);
    if (t) params.set("type", t);
    if (acc) params.set("accountId", acc);
    let all = state.tasks;
    if (params.toString()) {
      const d = await api(`/api/dashboard/tasks?${params}`);
      all = d.tasks;
    }
    document.getElementById("todosDesc").textContent = `共 ${all.length} 条待办`;
    box.innerHTML = all.length
      ? `<div class="task-list">${all.map(taskHtml).join("")}</div>`
      : `<div class="empty-tab">当前没有需要处理的事项 ✓</div>`;
    renderAccountOptions();
  } catch (e) {
    box.innerHTML = `<div class="empty-tab">加载失败：${escapeHtml(e.message)}</div>`;
  }
}

function renderAccountOptions() {
  const sel = document.getElementById("todoAccount");
  const cur = sel.value;
  const accounts = state.accounts?.items || [];
  sel.innerHTML = `<option value="">全部账号</option>` +
    (state.accounts?.items || []).map((a) => `<option value="${a.id}">${escapeHtml(a.username)}</option>`).join("");
  sel.value = cur;
}

// ---------------- 自动化任务 ----------------

async function loadAutomation() {
  try {
    const [tasks, audit] = await Promise.all([
      api("/api/tasks"),
      api("/api/audit?limit=30"),
    ]);
    document.getElementById("automationSummary").innerHTML = `
      <div class="stat-grid">
        <div class="stat-card"><span class="stat-num">${tasks.config.concurrency}</span><span class="stat-label">并发上限</span></div>
        <div class="stat-card"><span class="stat-num">${tasks.config.interval_seconds}s</span><span class="stat-label">同步间隔</span></div>
        <div class="stat-card"><span class="stat-num">${tasks.states.length}</span><span class="stat-label">已跟踪账号</span></div>
        <div class="stat-card"><span class="stat-num">${tasks.running ? "运行中" : "空闲"}</span><span class="stat-label">同步状态</span></div>
      </div>`;
    document.getElementById("auditList").innerHTML = audit.items.length
      ? `<div class="item-list">${audit.items.map((a) => `
          <div class="item">
            <div class="item-body">
              <div class="item-title" style="font-weight:500">${escapeHtml(a.action)}</div>
              <div class="item-meta"><span class="tag">${escapeHtml(a.username || "-")}</span><span>${escapeHtml(a.detail || "")}</span></div>
            </div>
            <span class="item-time">${relTime(a.ts)}</span>
          </div>`).join("")}</div>`
      : `<div class="empty-tab">暂无审计记录</div>`;
  } catch (e) {
    document.getElementById("automationSummary").innerHTML = `<div class="empty-tab">加载失败：${escapeHtml(e.message)}</div>`;
  }
}

// ---------------- 单账号详情 ----------------

async function openAccountDetail(id) {
  state.detailCache = state.detailCache || {};
  go("detail");
  document.getElementById("detailContent").innerHTML = `<div class="empty-tab">加载中…</div>`;
  try {
    let d = state.detailCache[id];
    if (!d) {
      const data = await api(`/api/status/${id}`);
      d = data.account;
      state.detailCache[id] = d;
    }
    state.detail = d;
    renderDetailHead(d);
    renderDetailTab(d);
  } catch (e) {
    document.getElementById("detailContent").innerHTML =
      `<div class="empty-tab">加载失败：${escapeHtml(e.message)} <button class="btn btn-sm" onclick="openAccountDetail(${id})">重试</button></div>`;
  }
}

function renderDetailHead(d) {
  const user = d.user || {};
  const avatar = document.getElementById("detailAvatar");
  avatar.src = user.avatar_url || "";
  avatar.onerror = function () { handleImgError(this); };
  document.getElementById("detailName").textContent = d.label || d.username;
  const rate = d.rate_limit?.resources?.core;
  document.getElementById("detailMeta").textContent =
    `${user.login || d.username || ""}${user.name ? "（" + user.name + "）" : ""}${rate ? ` · API 剩余 ${rate.remaining}/${rate.limit}` : ""}`;
  const badge = document.getElementById("detailStatus");
  badge.className = `status-badge ${d.status}`;
  badge.innerHTML = `<span>${statusText(d.status)}</span>`;
  const warn = (d.warnings || []).length
    ? `<div class="test-result fail" style="color:var(--warning)">部分数据获取失败：${d.warnings.map(escapeHtml).join("、")}</div>`
    : "";
  document.getElementById("detailWarnings").innerHTML = warn;
}

function renderDetailTab(d) {
  document.querySelectorAll("#detailTabs .tab").forEach((t) =>
    t.classList.toggle("active", t.dataset.tab === state.detailTab)
  );
  const box = document.getElementById("detailContent");
  if (state.detailTab === "overview") box.innerHTML = overviewHtml(d);
  if (state.detailTab === "compare") box.innerHTML = compareHtml(d);
  if (state.detailTab === "review") box.innerHTML = prList(d.review_prs, "待我 Review");
  if (state.detailTab === "mypr") box.innerHTML = prList(d.my_prs, null);
  if (state.detailTab === "issues") box.innerHTML = issueList(d.assigned_issues);
  if (state.detailTab === "events") box.innerHTML = eventList(d.events);
  if (state.detailTab === "repos") box.innerHTML = repoList(d.repos);
}

const COMPARE_COLS = [
  { key: "followers_count", label: "粉丝" },
  { key: "following_count", label: "关注" },
  { key: "public_repos", label: "公开仓库" },
  { key: "review_prs_count", label: "待处理 PR" },
  { key: "my_prs_count", label: "我的 PR" },
  { key: "assigned_issues_count", label: "分配 Issue" },
  { key: "total_stars", label: "总 Star" },
];

function overviewHtml(d) {
  const cards = `
    <div class="stat-grid">
      <div class="stat-card"><span class="stat-num">${d.followers_count ?? 0}</span><span class="stat-label">粉丝</span></div>
      <div class="stat-card"><span class="stat-num">${d.following_count ?? 0}</span><span class="stat-label">关注</span></div>
      <div class="stat-card"><span class="stat-num">${d.total_stars ?? 0}</span><span class="stat-label">总 Star</span></div>
      <div class="stat-card"><span class="stat-num">${d.public_repos ?? 0}</span><span class="stat-label">公开仓库</span></div>
    </div>`;
  const top = d.top_repos || [];
  const max = Math.max(1, ...top.map((r) => r.stars));
  const bars = top.length
    ? `<div class="item-list">${top.map((r) => `
        <div class="item">
          <div class="item-body">
            <a class="item-title" href="${safeUrl(r.html_url)}" target="_blank" rel="noopener">${escapeHtml(r.name)}</a>
          </div>
          <div class="progress" style="width:120px"><span style="width:${((r.stars / max) * 100).toFixed(1)}%"></span></div>
          <span class="item-time">★ ${r.stars}</span>
        </div>`).join("")}</div>`
    : `<div class="empty-tab">没有 Star 数据</div>`;
  return `
    <div class="panel">
      <div class="panel-head"><h3>数据概览</h3><button class="btn btn-sm" onclick="loadHistoryDetail()">刷新趋势</button></div>
      ${cards}
      <h3 style="margin:14px 0 10px;font-size:14px">仓库 Star 排行</h3>
      ${bars}
      <div id="detailTrend"></div>
    </div>
    <div class="panel">
      <div class="panel-head"><h3>历史趋势</h3></div>
      <div id="trendBox"><div class="empty-tab">点击上方「刷新趋势」加载</div></div>
    </div>`;
}

function compareHtml(d) {
  const s = state.accounts?.items || [];
  const rows = s.map((a) => `
    <tr>
      <td>${escapeHtml(a.username)}</td>
      <td>${a.followers}</td>
      <td>${a.following}</td>
      <td>${a.public_repos}</td>
      <td>${a.review_prs}</td>
      <td>${a.assigned_issues}</td>
      <td>${a.total_stars}</td>
    </tr>`).join("");
  return `<div class="panel"><div class="panel-head"><h3>与全部账号对比</h3></div>
    <div class="table-wrap"><table class="data-table"><thead><tr><th>账号</th>${COMPARE_COLS.map((c) => `<th>${c.label}</th>`).join("")}</tr></thead><tbody>${rows}</tbody></table></div></div>`;
}

function prList(items, note) {
  if (!items.length) return `<div class="empty-tab">${note ? "没有待处理的 PR" : "没有发起中的 PR"}</div>`;
  return `<div class="item-list">${items.map((it) => `
    <div class="item">
      <div class="item-body">
        <a class="item-title" href="${safeUrl(it.html_url)}" target="_blank" rel="noopener">${escapeHtml(it.title)}</a>
        <div class="item-meta">
          <span class="tag">${escapeHtml(repoName(it.repository_url))}</span>
          <span>#${it.number}</span><span>@${escapeHtml(it.user?.login || "")}</span>
          ${it.labels?.slice(0, 3).map((l) => `<span class="tag">${escapeHtml(l.name)}</span>`).join("")}
        </div>
      </div>
      <span class="item-time">${relTime(it.created_at)}</span>
    </div>`).join("")}</div>`;
}

function issueList(items) {
  if (!items.length) return `<div class="empty-tab">没有分配给你的 Issue</div>`;
  return `<div class="item-list">${items.map((it) => `
    <div class="item">
      <div class="item-body">
        <a class="item-title" href="${safeUrl(it.html_url)}" target="_blank" rel="noopener">${escapeHtml(it.title)}</a>
        <div class="item-meta">
          <span class="tag">${escapeHtml(repoName(it.repository_url))}</span>
          <span>#${it.number}</span><span>@${escapeHtml(it.user?.login || "")}</span>
        </div>
      </div>
      <span class="item-time">${relTime(it.created_at)}</span>
    </div>`).join("")}</div>`;
}

const EVENT_TYPES = {
  PushEvent: { text: "推送了提交" }, PullRequestEvent: { text: "处理了 PR" },
  IssuesEvent: { text: "处理了 Issue" }, WatchEvent: { text: "Star 了仓库" },
  CreateEvent: { text: "创建了分支/标签" }, DeleteEvent: { text: "删除了分支/标签" },
  ReleaseEvent: { text: "发布了 Release" }, ForkEvent: { text: "Fork 了仓库" },
  IssueCommentEvent: { text: "评论了 Issue" }, CommitCommentEvent: { text: "评论了提交" },
};

function eventList(events) {
  if (!events.length) return `<div class="empty-tab">最近没有动态</div>`;
  return `<div class="item-list">${events.map((ev) => {
    const t = EVENT_TYPES[ev.type] || { text: ev.type };
    const actor = ev.actor || {};
    const repo = ev.repo || {};
    return `
    <div class="item">
      ${actor.avatar_url ? `<img class="activity-actor" src="${escapeHtml(actor.avatar_url)}" alt="" onerror="handleImgError(this)">` : ""}
      <div class="item-body">
        <div class="item-title" style="font-weight:500">${escapeHtml(actor.login || "")} ${escapeHtml(t.text)}${ev.payload?.action ? " · " + escapeHtml(ev.payload.action) : ""}</div>
        <div class="item-meta"><span class="tag">${escapeHtml(repo.name || "")}</span></div>
      </div>
      <span class="item-time">${relTime(ev.created_at)}</span>
    </div>`;
  }).join("")}</div>`;
}

function repoList(repos) {
  if (!repos.length) return `<div class="empty-tab">没有公开仓库</div>`;
  return `<div class="item-list">${repos.map((r) => `
    <div class="item">
      <div class="item-body">
        <a class="item-title" href="${safeUrl(r.html_url)}" target="_blank" rel="noopener">${escapeHtml(r.name)}</a>
        <div class="item-meta">
          <span>★ ${r.stargazers_count ?? 0}</span><span>⑂ ${r.forks_count ?? 0}</span>
          ${r.language ? `<span>${escapeHtml(r.language)}</span>` : ""}
        </div>
      </div>
      <span class="item-time">更新于 ${relTime(r.updated_at)}</span>
    </div>`).join("")}</div>`;
}

async function loadHistoryDetail() {
  if (!state.detail) return;
  try {
    const data = await api(`/api/history/${state.detail.id}?limit=300`);
    state.history = data.history || [];
    renderTrendBox();
  } catch (e) {
    document.getElementById("trendBox").innerHTML = `<div class="empty-tab">加载失败</div>`;
  }
}

function renderTrendBox() {
  const box = document.getElementById("trendBox");
  const history = state.history || [];
  if (!history.length) {
    box.innerHTML = `<div class="empty-tab">暂无历史数据，同步后自动生成</div>`;
    return;
  }
  const metric = state.metric;
  const points = history.map((h) => ({ t: new Date(h.created_at).getTime(), v: Number(h[metric]) || 0 }))
    .filter((p) => Number.isFinite(p.t) && Number.isFinite(p.v));
  if (!points.length) {
    box.innerHTML = `<div class="empty-tab">暂无有效数据</div>`;
    return;
  }
  const w = 640, h = 170, padL = 44, padR = 14, padT = 14, padB = 26;
  const xs = points.map((p) => p.t), ys = points.map((p) => p.v);
  const minX = Math.min(...xs), maxX = Math.max(...xs);
  const minY = Math.min(...ys), maxY = Math.max(...ys);
  const spanX = maxX - minX || 1;
  const spanY = maxY - minY || Math.max(1, maxY * 0.1);
  const px = (t) => padL + ((t - minX) / spanX) * (w - padL - padR);
  const py = (v) => padT + (1 - (v - minY) / spanY) * (h - padT - padB);
  const path = points.map((p, i) => `${i ? "L" : "M"}${px(p.t).toFixed(1)},${py(p.v).toFixed(1)}`).join(" ");
  const area = `${path} L${px(maxX).toFixed(1)},${(h - padB).toFixed(1)} L${px(minX).toFixed(1)},${(h - padB).toFixed(1)} Z`;
  let grid = "";
  for (let i = 0; i <= 4; i++) {
    const v = minY + (spanY * i) / 4;
    const y = py(v).toFixed(1);
    grid += `<line x1="${padL}" y1="${y}" x2="${w - padR}" y2="${y}" style="stroke:var(--border-muted)"/>`;
    grid += `<text x="${padL - 6}" y="${+y + 4}" text-anchor="end" font-size="10" style="fill:var(--text-faint)">${Math.round(v)}</text>`;
  }
  const fmt = (t) => new Date(t).toLocaleDateString("zh-CN", { month: "numeric", day: "numeric" });
  box.innerHTML = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;height:auto">
    ${grid}
    <path d="${area}" style="fill:var(--accent-soft)" stroke="none"/>
    <path d="${path}" fill="none" style="stroke:var(--accent)" stroke-width="2"/>
    <text x="${padL}" y="${h - 8}" font-size="10" style="fill:var(--text-faint)">${fmt(minX)}</text>
    <text x="${w - padR}" y="${h - 8}" text-anchor="end" font-size="10" style="fill:var(--text-faint)">${fmt(maxX)}</text>
  </svg>`;
}

// ---------------- 同步 ----------------

async function syncNow() {
  try {
    const r = await api("/api/sync/run", { method: "POST" });
    showToast(`同步完成：成功 ${r.done}，失败 ${r.failed}`);
    await loadOverview();
  } catch (e) {
    showToast(e.message);
  }
}

async function openSyncModal() {
  try {
    const d = await api("/api/tasks");
    document.getElementById("syncInterval").value = d.config.interval_seconds;
    document.getElementById("syncConcurrency").value = d.config.concurrency;
    document.getElementById("syncBackoff").value = d.config.base_backoff_seconds;
    document.getElementById("syncModal").classList.remove("hidden");
  } catch (e) {
    showToast(e.message);
  }
}

async function saveSyncSettings() {
  try {
    await api("/api/sync/settings", {
      method: "POST",
      body: JSON.stringify({
        enabled: Number(document.getElementById("syncInterval").value) > 0,
        interval_seconds: Number(document.getElementById("syncInterval").value),
        concurrency: Number(document.getElementById("syncConcurrency").value),
        base_backoff_seconds: Number(document.getElementById("syncBackoff").value),
      }),
    });
    document.getElementById("syncModal").classList.add("hidden");
    showToast("同步设置已保存");
  } catch (e) {
    showToast(e.message);
  }
}

// ---------------- 预取 ----------------

function togglePrefetch() {
  state.prefetchEnabled = !state.prefetchEnabled;
  const btn = document.getElementById("prefetchBtn");
  btn.classList.toggle("active", state.prefetchEnabled);
  btn.textContent = state.prefetchEnabled ? "预取·开" : "预取·关";
  showToast(state.prefetchEnabled ? "已开启详情预取" : "已关闭详情预取");
}

// ---------------- 版本 ----------------

async function loadVersion() {
  try {
    const v = await api("/api/version");
    const btn = document.getElementById("versionBtn");
    btn.textContent = "v" + v.version;
    btn.style.display = "";
  } catch (e) { /* ignore */ }
}

async function checkUpdate() {
  try {
    const r = await api("/api/update-check");
    if (!r.ok) { showToast(r.error || "无法检查更新"); return; }
    const cur = (document.getElementById("versionBtn").textContent || "").replace(/^v/, "");
    if (r.latest && r.latest !== cur) showToast(`有新版本 ${r.latest}`);
    else showToast("已是最新版本");
  } catch (e) { showToast("检查更新失败"); }
}

// ---------------- 通用 ----------------

function debounce(fn, ms) {
  let t;
  return (...args) => {
    clearTimeout(t);
    t = setTimeout(() => fn(...args), ms);
  };
}

function downloadJson(obj, filename) {
  const blob = new Blob([JSON.stringify(obj, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// ---------------- 添加账号 ----------------

function openModal() {
  document.getElementById("modal").classList.remove("hidden");
  document.getElementById("fUsername").focus();
}

function closeModal() {
  document.getElementById("modal").classList.add("hidden");
  ["fLabel", "fUsername", "fGroup", "fTags", "fNote", "fToken"].forEach((id) => (document.getElementById(id).value = ""));
  document.getElementById("fType").value = "personal";
  document.getElementById("testResult").textContent = "";
}

async function testConnection() {
  const username = document.getElementById("fUsername").value.trim();
  const token = document.getElementById("fToken").value.trim();
  const box = document.getElementById("testResult");
  if (!username || !token) { box.className = "test-result fail"; box.textContent = "请填写用户名和 Token"; return; }
  box.className = "test-result";
  box.textContent = "测试中…";
  try {
    const v = await api("/api/verify", { method: "POST", body: JSON.stringify({ username, token }) });
    box.className = v.status === "active" ? "test-result ok" : "test-result fail";
    box.textContent = v.status === "active" ? `✓ 连接成功：@${v.user?.login || username}` : `✗ ${v.error || "连接失败"}`;
  } catch (e) {
    box.className = "test-result fail";
    box.textContent = e.message;
  }
}

async function saveAccount() {
  const label = document.getElementById("fLabel").value.trim();
  const username = document.getElementById("fUsername").value.trim();
  const token = document.getElementById("fToken").value.trim();
  const group = document.getElementById("fGroup").value.trim();
  const type = document.getElementById("fType").value;
  const tags = document.getElementById("fTags").value.split(",").map((t) => t.trim()).filter(Boolean);
  const note = document.getElementById("fNote").value.trim();
  if (!username || !token) { showToast("请填写用户名和 Token"); return; }
  try {
    await api("/api/accounts", { method: "POST", body: JSON.stringify({ label, username, token, group, type, tags, note }) });
    showToast("已保存");
    closeModal();
    await loadAccountsPage(false);
    await loadOverview();
  } catch (e) {
    showToast(e.message);
  }
}

// ---------------- 备份 ----------------

function closeBackupModal() {
  document.getElementById("backupModal").classList.add("hidden");
}

async function exportBackup() {
  try {
    const data = await api("/api/accounts/export");
    downloadJson(data, `github-accounts-${new Date().toISOString().slice(0, 10)}.json`);
    document.getElementById("backupResult").className = "test-result ok";
    document.getElementById("backupResult").textContent = "✓ 已导出账号信息（不含凭据）";
  } catch (e) {
    document.getElementById("backupResult").className = "test-result fail";
    document.getElementById("backupResult").textContent = "导出失败：" + e.message;
  }
}

async function exportCredBackup() {
  const pwd = prompt("设置凭据备份密码（长度至少 4 位）", "");
  if (pwd === null) return;
  if (pwd.length < 4) { showToast("密码太短"); return; }
  try {
    const data = await api("/api/accounts/export", {
      method: "POST",
      body: JSON.stringify({ password: pwd }),
    });
    downloadJson(data, `github-accounts-encrypted-${new Date().toISOString().slice(0, 10)}.json`);
    showToast("已导出加密凭据备份");
  } catch (e) {
    showToast(e.message);
  }
}

async function importBackup(file) {
  const box = document.getElementById("backupResult");
  try {
    const data = JSON.parse(await file.text());
    let password = "";
    if (data.encrypted) {
      password = prompt("该备份已加密，请输入密码", "") || "";
    }
    const result = await api("/api/accounts/import", {
      method: "POST",
      body: JSON.stringify({ ...data, password }),
    });
    box.className = "test-result ok";
    box.textContent = `✓ 导入完成：新增 ${result.added}，更新 ${result.updated}${result.skipped ? `，跳过 ${result.skipped}` : ""}`;
    await loadAccountsPage(false);
  } catch (e) {
    box.className = "test-result fail";
    box.textContent = "导入失败：" + e.message;
  }
}

// ---------------- 全局搜索 ----------------

function openSearchModal() {
  document.getElementById("searchModal").classList.remove("hidden");
  const input = document.getElementById("globalSearchInput");
  input.value = "";
  document.getElementById("globalSearchResults").innerHTML =
    `<div class="empty-tab">输入关键字，搜索所有账号的 PR / Issue</div>`;
  setTimeout(() => input.focus(), 0);
}

function closeSearchModal() {
  document.getElementById("searchModal").classList.add("hidden");
}

async function doGlobalSearch() {
  const q = document.getElementById("globalSearchInput").value.trim();
  const kind = document.getElementById("globalSearchKind").value;
  const box = document.getElementById("globalSearchResults");
  if (!q) { box.innerHTML = `<div class="empty-tab">输入关键字开始搜索</div>`; return; }
  box.innerHTML = `<div class="empty-tab">搜索中…</div>`;
  try {
    const data = await api(`/api/search?q=${encodeURIComponent(q)}&kind=${encodeURIComponent(kind)}`);
    const results = data.results || [];
    box.innerHTML = results.length
      ? `<div class="item-list">${results.map((r) => `
          <div class="item">
            <div class="item-body">
              <a class="item-title" href="${safeUrl(r.html_url)}" target="_blank" rel="noopener">${escapeHtml(r.title)}</a>
              <div class="item-meta">
                <span class="tag">${escapeHtml(r.repository)}</span>
                <span class="tag">${escapeHtml(r.account_label)}</span>
                ${(r.labels || []).map((l) => `<span class="tag">${escapeHtml(l)}</span>`).join("")}
              </div>
            </div>
            <span class="item-time">${relTime(r.created_at)}</span>
          </div>`).join("")}</div>
        <div style="margin-top:10px;font-size:12px;color:var(--text-faint)">共 ${data.total} 条，搜索了 ${data.accounts} 个账号</div>`
      : `<div class="empty-tab">没有匹配的结果</div>`;
  } catch (e) {
    box.innerHTML = `<div class="empty-tab">搜索失败：${escapeHtml(e.message)}</div>`;
  }
}

// ---------------- 外部通知 ----------------

function maskUrl(url) {
  if (!url) return "";
  try {
    const u = new URL(url);
    return u.origin + "/***" + (u.search ? "?" + u.search.slice(0, 16) + "..." : "");
  } catch (e) {
    return url.length > 28 ? url.slice(0, 14) + "..." + url.slice(-6) : url;
  }
}

async function openNotifyModal() {
  document.getElementById("notifyModal").classList.remove("hidden");
  document.getElementById("notifyResult").textContent = "";
  await loadNotifyChannels();
}

function closeNotifyModal() {
  document.getElementById("notifyModal").classList.add("hidden");
}

async function loadNotifyChannels() {
  try {
    const data = await api("/api/notifications/settings");
    state.notifyChannels = data.channels || [];
    renderNotifyChannels();
  } catch (e) {
    showToast(e.message);
  }
}

function renderNotifyChannels() {
  const box = document.getElementById("notifyChannels");
  if (!state.notifyChannels.length) {
    box.innerHTML = `<div class="empty-tab">还没有配置通知渠道</div>`;
    return;
  }
  box.innerHTML = state.notifyChannels.map((c, i) => `
    <div class="notify-row">
      <span class="notify-name">${escapeHtml(c.name || c.type)}</span>
      <span class="tag">${escapeHtml(c.type)}</span>
      <span class="notify-url">${escapeHtml(maskUrl(c.url))}</span>
      <label style="display:inline-flex;align-items:center;gap:4px;color:var(--text-muted)">
        <input type="checkbox" data-notify-enable="${i}" ${c.enabled ? "checked" : ""}> 启用
      </label>
      <button class="btn btn-sm" data-notify-del="${i}">删除</button>
    </div>`).join("");
  box.querySelectorAll("[data-notify-enable]").forEach((cb) =>
    cb.addEventListener("change", async () => {
      state.notifyChannels[Number(cb.dataset.notifyEnable)].enabled = cb.checked;
      await saveNotifyChannels();
    })
  );
  box.querySelectorAll("[data-notify-del]").forEach((btn) =>
    btn.addEventListener("click", async () => {
      state.notifyChannels.splice(Number(btn.dataset.notifyDel), 1);
      await saveNotifyChannels();
      renderNotifyChannels();
    })
  );
}

async function saveNotifyChannels() {
  try {
    await api("/api/notifications/settings", { method: "POST", body: JSON.stringify({ channels: state.notifyChannels }) });
  } catch (e) {
    showToast(e.message);
  }
}

async function addNotifyChannel() {
  const type = document.getElementById("notifyType").value;
  const name = document.getElementById("notifyName").value.trim() || type;
  const url = document.getElementById("notifyUrl").value.trim();
  if (!url) { showToast("请填写推送 URL"); return; }
  state.notifyChannels.push({ type, name, url, enabled: true });
  document.getElementById("notifyName").value = "";
  document.getElementById("notifyUrl").value = "";
  await saveNotifyChannels();
  renderNotifyChannels();
  showToast("已添加");
}

async function testNotification() {
  const box = document.getElementById("notifyResult");
  if (!state.notifyChannels.length) { box.className = "test-result fail"; box.textContent = "请先添加通知渠道"; return; }
  box.className = "test-result";
  box.textContent = "发送中…";
  try {
    const r = await api("/api/notifications/test", { method: "POST" });
    box.className = "test-result ok";
    box.textContent = `✓ 已发送到 ${r.sent} 个渠道`;
  } catch (e) {
    box.className = "test-result fail";
    box.textContent = "发送失败：" + e.message;
  }
}

// ---------------- 账号列表交互 ----------------

function clearAccountSearch() {
  document.getElementById("accountSearch").value = "";
  state.accountQuery.query = "";
  state.accounts.page = 1;
  loadAccountsPage();
  document.getElementById("accountSearch").focus();
}

function toggleSortOrder() {
  state.accountQuery.order = state.accountQuery.order === "desc" ? "asc" : "desc";
  document.getElementById("sortOrder").textContent = state.accountQuery.order === "desc" ? "↓ 降序" : "↑ 升序";
  state.accounts.page = 1;
  loadAccountsPage();
}

function toggleCheckAll() {
  const checkAll = document.getElementById("checkAll");
  const boxes = document.querySelectorAll("#accountTableBody [data-check]");
  boxes.forEach((cb) => {
    cb.checked = checkAll.checked;
    const id = Number(cb.dataset.check);
    if (checkAll.checked) state.selected.add(id);
    else state.selected.delete(id);
  });
  updateBatchBar();
}

// ---------------- 快捷键 ----------------

function onKeydown(e) {
  if (e.key === "Escape") {
    ["modal", "backupModal", "searchModal", "notifyModal", "syncModal", "shortcutsModal"].forEach((id) => {
      const el = document.getElementById(id);
      if (el) el.classList.add("hidden");
    });
    return;
  }
  const typing = document.activeElement && ["INPUT", "TEXTAREA", "SELECT"].includes(document.activeElement.tagName);
  if (typing) return;
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") { e.preventDefault(); openSearchModal(); return; }
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  const k = e.key.toLowerCase();
  if (k === "t") toggleTheme();
  if (k === "r") syncNow();
  if (k === "a") openModal();
  if (k === "/") { e.preventDefault(); openSearchModal(); }
}

// ---------------- 事件绑定 ----------------

function bindEvents() {
  document.querySelectorAll(".nav-item").forEach((n) => n.addEventListener("click", () => go(n.dataset.page)));
  document.getElementById("viewAllAccounts").addEventListener("click", () => go("accounts"));
  document.getElementById("globalSearchBtn").addEventListener("click", openSearchModal);
  document.getElementById("syncNowBtn").addEventListener("click", syncNow);
  document.getElementById("syncSettingsBtn").addEventListener("click", openSyncModal);
  document.getElementById("prefetchBtn").addEventListener("click", togglePrefetch);
  document.getElementById("notifyBtn").addEventListener("click", openNotifyModal);
  document.getElementById("themeBtn").addEventListener("click", toggleTheme);
  document.getElementById("addBtn").addEventListener("click", openModal);
  document.getElementById("versionBtn").addEventListener("click", checkUpdate);

  document.getElementById("cancelBtn").addEventListener("click", closeModal);
  document.getElementById("saveBtn").addEventListener("click", saveAccount);
  document.getElementById("testBtn").addEventListener("click", testConnection);
  document.getElementById("modal").addEventListener("click", (e) => { if (e.target.id === "modal") closeModal(); });

  document.getElementById("exportBtn").addEventListener("click", exportBackup);
  document.getElementById("exportCredBtn").addEventListener("click", exportCredBackup);
  document.getElementById("importFile").addEventListener("change", (e) => { if (e.target.files[0]) importBackup(e.target.files[0]); });
  document.getElementById("backupCloseBtn").addEventListener("click", closeBackupModal);
  document.getElementById("backupModal").addEventListener("click", (e) => { if (e.target.id === "backupModal") closeBackupModal(); });

  document.getElementById("searchCloseBtn").addEventListener("click", closeSearchModal);
  document.getElementById("searchModal").addEventListener("click", (e) => { if (e.target.id === "searchModal") closeSearchModal(); });
  document.getElementById("globalSearchInput").addEventListener("keydown", (e) => { if (e.key === "Enter") doGlobalSearch(); });
  document.getElementById("globalSearchKind").addEventListener("change", () => {
    if (document.getElementById("globalSearchInput").value.trim()) doGlobalSearch();
  });

  document.getElementById("notifyCloseBtn").addEventListener("click", closeNotifyModal);
  document.getElementById("notifyModal").addEventListener("click", (e) => { if (e.target.id === "notifyModal") closeNotifyModal(); });
  document.getElementById("notifyAddBtn").addEventListener("click", addNotifyChannel);
  document.getElementById("notifyTestBtn").addEventListener("click", testNotification);

  document.getElementById("syncSaveBtn").addEventListener("click", saveSyncSettings);
  document.getElementById("openSyncSettings").addEventListener("click", openSyncModal);
  document.getElementById("automationSyncNow").addEventListener("click", syncNow);
  document.getElementById("syncCloseBtn").addEventListener("click", () => document.getElementById("syncModal").classList.add("hidden"));
  document.getElementById("syncModal").addEventListener("click", (e) => { if (e.target.id === "syncModal") document.getElementById("syncModal").classList.add("hidden"); });
  document.getElementById("syncNowBtn").addEventListener("dblclick", openSyncModal);

  document.getElementById("accountSearch").addEventListener("input", debounce(() => {
    state.accountQuery.query = document.getElementById("accountSearch").value;
    state.accounts.page = 1;
    loadAccountsPage();
  }, 300));
  document.getElementById("clearAccountSearch").addEventListener("click", clearAccountSearch);
  document.getElementById("filterStatus").addEventListener("change", (e) => { state.accountQuery.status = e.target.value; state.accounts.page = 1; loadAccountsPage(); });
  document.getElementById("filterType").addEventListener("change", (e) => { state.accountQuery.type = e.target.value; state.accounts.page = 1; loadAccountsPage(); });
  document.getElementById("filterGroup").addEventListener("change", (e) => { state.accountQuery.group = e.target.value; state.accounts.page = 1; loadAccountsPage(); });
  document.getElementById("filterTag").addEventListener("change", (e) => { state.accountQuery.tag = e.target.value; state.accounts.page = 1; loadAccountsPage(); });
  document.getElementById("sortBy").addEventListener("change", (e) => { state.accountQuery.sort = e.target.value; state.accounts.page = 1; loadAccountsPage(); });
  document.getElementById("sortOrder").addEventListener("click", toggleSortOrder);
  document.getElementById("prevPage").addEventListener("click", () => { if (state.accounts.page > 1) { state.accounts.page--; loadAccountsPage(); } });
  document.getElementById("nextPage").addEventListener("click", () => { if (state.accounts.page < state.accounts.total_pages) { state.accounts.page++; loadAccountsPage(); } });
  document.getElementById("pageSize").addEventListener("change", (e) => { state.accounts.page_size = Number(e.target.value); state.accounts.page = 1; loadAccountsPage(); });
  document.getElementById("checkAll").addEventListener("change", toggleCheckAll);
  document.getElementById("batchRefresh").addEventListener("click", batchRefresh);
  document.getElementById("batchExport").addEventListener("click", batchExport);

  document.querySelectorAll("#activityFilter .chip").forEach((c) => c.addEventListener("click", () => {
    document.querySelectorAll("#activityFilter .chip").forEach((x) => x.classList.toggle("active", x === c));
    loadOverviewActivities(c.dataset.type);
  }));

  document.getElementById("todoPriority").addEventListener("change", loadTodos);
  document.getElementById("todoType").addEventListener("change", loadTodos);
  document.getElementById("todoAccount").addEventListener("change", loadTodos);

  document.getElementById("backToAccounts").addEventListener("click", () => go("accounts"));
  document.getElementById("detailSyncBtn").addEventListener("click", () => { if (state.detail) syncAccount(state.detail.id); });
  document.querySelectorAll("#detailTabs .tab").forEach((t) => t.addEventListener("click", () => {
    state.detailTab = t.dataset.tab;
    renderDetailTab(state.detail);
  }));

  document.addEventListener("keydown", onKeydown);
}

// ---------------- 初始化 ----------------

async function init() {
  applyTheme(document.documentElement.dataset.theme || "light");
  bindEvents();
  await loadOverview();
  await loadAccountsPage(false);
  await loadTodos();
  loadVersion();
}

init().catch((e) => showToast(e.message));

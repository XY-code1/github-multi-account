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
  prefetchEnabled: false,
  version: null,
  webhooks: [],
  webhookTypes: [],
  email: {
    settings: null, accounts: [], inboxes: [], selected: "",
    items: [], detailId: null, q: "", sort: "time", filter: "all", mailboxQ: "",
  },
};

// ---------------- 路由 ----------------

const SUBTITLES = {
  overview: "账号总览",
  accounts: "全部账号",
  todos: "统一待办",
  automation: "自动化任务",
  graph: "关系图谱",
  email: "邮箱",
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
  if (page === "accounts") {
    loadAccountsPage();
    if (state.prefetchEnabled) runPrefetch();
  }
  if (page === "todos") loadTodos();
  if (page === "automation") loadAutomation();
  if (page === "graph") loadGraph();
  if (page === "email") loadEmail();
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
  const todos = (s.review_prs || 0) + (s.issues || 0);
  document.getElementById("summaryCards").innerHTML = `
    <div class="summary-card">
      <div class="summary-title">账号</div>
      <div class="summary-main"><span class="summary-num">${s.total}</span></div>
      <div class="summary-sub">
        <span class="tag is-success">健康 ${s.healthy}</span>
        <span class="tag ${s.attention ? "is-danger" : ""}">需关注 ${s.attention}</span>
      </div>
    </div>
    <div class="summary-card">
      <div class="summary-title">风险</div>
      <div class="summary-main"><span class="summary-num ${(s.credential_risk || s.quota_capped) ? "is-danger" : ""}">${(s.credential_risk || 0) + (s.quota_capped || 0)}</span></div>
      <div class="summary-sub">
        <span class="tag ${s.credential_risk ? "is-danger" : ""}">凭据 ${s.credential_risk}</span>
        <span class="tag ${s.quota_capped ? "is-danger" : ""}">已封顶 ${s.quota_capped}</span>
        <span class="tag ${s.quota_low ? "is-warning" : ""}">配额不足 ${s.quota_low}</span>
      </div>
    </div>
    <div class="summary-card">
      <div class="summary-title">待办</div>
      <div class="summary-main"><span class="summary-num">${todos}</span></div>
      <div class="summary-sub">
        <span class="tag">待 Review ${s.review_prs}</span>
        <span class="tag">Issue ${s.issues}</span>
      </div>
    </div>`;
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
    credential: "🔑", quota: "📊", quota_capped: "🚫", cross_account: "🔗",
    review_pr: "👀", my_pr: "📤", issue: "📋", sync_failed: "⚠", sync_stale: "🕘",
  }[t.type] || "•";
  const accountTag = t.account_username
    ? `${escapeHtml(t.account_label)} · @${escapeHtml(t.account_username)}`
    : escapeHtml(t.account_label);
  const isCross = t.type === "cross_account" && t.url;
  const titleHtml = isCross
    ? `<a class="task-title-link" href="${safeUrl(t.url)}" target="_blank" rel="noopener">${escapeHtml(t.title)}</a>`
    : escapeHtml(t.title);
  const actionHtml = isCross
    ? `<a class="btn btn-sm" href="${safeUrl(t.url)}" target="_blank" rel="noopener">${escapeHtml(t.action)}</a>`
    : `<button class="btn btn-sm" onclick="openAccountDetail(${t.account_id})">${escapeHtml(t.action)}</button>`;
  return `
    <div class="task" data-priority="${t.priority}">
      <div class="task-icon">${icon}</div>
      <div class="task-body">
        <div class="task-title">${titleHtml}</div>
        <div class="task-meta">
          <span class="tag">${escapeHtml(t.type_label || t.type)}</span>
          <span class="tag tag-account">${accountTag}</span>
          <span>${escapeHtml(t.detail)}</span>
          ${t.time ? `<span>${relTime(t.time)}</span>` : ""}
        </div>
      </div>
      <div class="task-actions">
        <span class="prio prio-${t.priority}">${{ high: "高", medium: "中", low: "低" }[t.priority]}</span>
        ${actionHtml}
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
  const ev = EVENT_TYPES[a.type] || { text: a.type };
  const act = ACTION_TYPES[a.action] || a.action;
  const count = a.count > 1 ? `<span class="tag is-count">×${a.count}</span>` : "";
  const account = a.account_username
    ? `${escapeHtml(a.account_label || "")} · @${escapeHtml(a.account_username)}`
    : escapeHtml(a.account_label);
  return `
    <div class="activity">
      ${a.actor_avatar ? `<img class="activity-actor" src="${escapeHtml(a.actor_avatar)}" alt="" onerror="handleImgError(this)">` : ""}
      <div class="activity-body">
        <div><strong>${escapeHtml(a.actor || a.account_label)}</strong> ${escapeHtml(ev.text)}${act ? " · " + escapeHtml(act) : ""} ${count}</div>
        <div class="task-meta">
          <span class="tag tag-account">${account}</span>
          ${a.repo ? `<span class="tag">${escapeHtml(a.repo)}</span>` : ""}
        </div>
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
            ${a.email ? `<div class="cell-sub cell-email">${escapeHtml(a.email)}</div>` : ""}
          </div>
        </div>
      </td>
      <td>
        <span class="tag">${{ personal: "个人", bot: "Bot", org: "组织" }[a.type] || "个人"}</span>
        ${a.cluster_label ? `<span class="tag tag-cluster">${escapeHtml(a.cluster_label)}</span>` : ""}
        ${a.tags.map((t) => `<span class="tag">${escapeHtml(t)}</span>`).join("")}
      </td>
      <td>
        <span class="status-badge ${a.status}">${statusText(a.status)}</span>
        ${a.error ? `<div class="cell-sub">${escapeHtml(a.error)}</div>` : ""}
      </td>
      <td>
        <div class="progress ${low}"><span style="width:${pct}%"></span></div>
        <span class="cell-sub">${a.remaining}/${a.quota_limit || "?"}</span>
        ${a.quota_capped ? '<span class="tag is-danger">已封顶</span>' : ""}
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
    // 同步后旧详情已过期，清掉缓存避免展示旧数据
    if (state.detailCache) delete state.detailCache[id];
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
    state.detailCache = {};
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

// ---------------- 批量操作 ----------------

async function batchSetSync(enabled) {
  if (!state.selected.size) return;
  try {
    await api("/api/accounts/batch-toggle-sync", {
      method: "POST",
      body: JSON.stringify({ ids: [...state.selected], enabled }),
    });
    showToast(enabled ? `已启用 ${state.selected.size} 个账号的同步` : `已暂停 ${state.selected.size} 个账号的同步`);
    await loadAccountsPage(false);
  } catch (e) {
    showToast(e.message);
  }
}

async function batchTag() {
  if (!state.selected.size) return;
  const input = prompt(`给选中的 ${state.selected.size} 个账号添加标签（逗号分隔）`, "");
  if (input === null) return;
  const tags = input.split(",").map((t) => t.trim()).filter(Boolean);
  if (!tags.length) return;
  try {
    await api("/api/accounts/batch-tag", {
      method: "POST",
      body: JSON.stringify({ ids: [...state.selected], tags, mode: "append" }),
    });
    showToast(`已给 ${state.selected.size} 个账号打标签`);
    await loadAccountsPage(false);
  } catch (e) {
    showToast(e.message);
  }
}

async function batchGroup() {
  if (!state.selected.size) return;
  const group = prompt(`把选中的 ${state.selected.size} 个账号移动到分组`, "");
  if (group === null || !group.trim()) return;
  try {
    await api("/api/accounts/batch-group", {
      method: "POST",
      body: JSON.stringify({ ids: [...state.selected], group: group.trim() }),
    });
    showToast(`已移动 ${state.selected.size} 个账号到「${group.trim()}」`);
    await loadAccountsPage(false);
  } catch (e) {
    showToast(e.message);
  }
}

// ---------------- Word 导入 ----------------

let docxFile = null;

// 上传 .docx：preview=true 只解析并返回候选，preview=false 按 indices 写入
async function uploadDocx(file, preview, indices, overrides) {
  const fd = new FormData();
  fd.append("file", file);
  const params = new URLSearchParams();
  params.set("preview", preview ? "true" : "false");
  if (indices && indices.length) params.set("indices", indices.join(","));
  if (overrides && Object.keys(overrides).length) fd.append("overrides", JSON.stringify(overrides));
  const resp = await fetch(`/api/accounts/import-docx?${params.toString()}`, { method: "POST", body: fd });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.detail || `上传失败（${resp.status}）`);
  return data;
}

async function handleDocxFile(e) {
  const file = e.target.files[0];
  if (!file) return;
  if (!file.name.toLowerCase().endsWith(".docx")) {
    showToast("请选择 .docx 文件");
    return;
  }
  docxFile = file;
  const box = document.getElementById("docxPreview");
  box.classList.remove("hidden");
  box.innerHTML = `<div class="empty-tab">正在解析文档…</div>`;
  try {
    const data = await uploadDocx(file, true);
    if (!data.candidates.length) {
      box.innerHTML = `<div class="empty-tab">${escapeHtml(data.message || "没有在文档里找到 GitHub Token")}</div>`;
      return;
    }
    renderDocxPreview(data.candidates);
  } catch (err) {
    box.innerHTML = `<div class="empty-tab">解析失败：${escapeHtml(err.message)}</div>`;
  }
}

const DOCX_PREVIEW_LIMIT = 200;

function renderDocxPreview(candidates) {
  const box = document.getElementById("docxPreview");
  // 预览表格较宽，加宽弹窗；行数封顶，超出的提示
  document.getElementById("modal").classList.add("modal-lg");
  const shown = candidates.slice(0, DOCX_PREVIEW_LIMIT);
  const rows = shown.map((c) => `
    <tr data-index="${c.index}" class="${c.valid ? "" : "row-invalid"}">
      <td class="col-check"><input type="checkbox" class="docx-check" data-index="${c.index}" ${c.valid ? "checked" : ""}></td>
      <td><input class="input-sm docx-username" data-index="${c.index}" value="${escapeHtml(c.username)}" placeholder="GitHub 用户名"></td>
      <td><input class="input-sm docx-label" data-index="${c.index}" value="${escapeHtml(c.label)}" placeholder="显示名称"></td>
      <td><code>${escapeHtml(c.token_masked)}</code></td>
      <td>${c.valid ? '<span class="status-badge active">有效</span>' : '<span class="status-badge invalid">无效</span>'}</td>
    </tr>`).join("");
  const capped = candidates.length > shown.length
    ? `<div class="docx-cap">共 ${candidates.length} 个账号，仅显示前 ${shown.length} 个</div>`
    : "";
  box.innerHTML = `
    <div class="table-wrap"><table class="data-table docx-table">
      <thead><tr>
        <th class="col-check"><input type="checkbox" id="docxCheckAll" checked></th>
        <th>用户名</th><th>显示名称</th><th>Token</th><th>状态</th>
      </tr></thead>
      <tbody>${rows}</tbody>
    </table></div>
    ${capped}
    <div class="docx-actions">
      <span class="docx-summary" id="docxSummary"></span>
      <span class="spacer"></span>
      <button id="docxImportBtn" class="btn btn-primary">导入选中</button>
    </div>`;
  document.getElementById("docxCheckAll").addEventListener("change", (e) => {
    box.querySelectorAll(".docx-check").forEach((cb) => { cb.checked = e.target.checked; });
    updateDocxSummary();
  });
  box.querySelectorAll(".docx-check").forEach((cb) => cb.addEventListener("change", updateDocxSummary));
  document.getElementById("docxImportBtn").addEventListener("click", importSelectedDocx);
  updateDocxSummary();
}

function updateDocxSummary() {
  const box = document.getElementById("docxPreview");
  const total = box.querySelectorAll(".docx-check").length;
  const checked = box.querySelectorAll(".docx-check:checked").length;
  const el = document.getElementById("docxSummary");
  if (el) el.textContent = `已识别 ${total} 个 Token，选中 ${checked} 个`;
}

async function importSelectedDocx() {
  if (!docxFile) return;
  const rows = document.querySelectorAll("#docxPreview tr[data-index]");
  const indices = [];
  const overrides = {};
  rows.forEach((tr) => {
    const cb = tr.querySelector(".docx-check");
    if (!cb.checked) return;
    const idx = Number(cb.dataset.index);
    indices.push(idx);
    overrides[idx] = {
      username: tr.querySelector(".docx-username").value.trim(),
      label: tr.querySelector(".docx-label").value.trim(),
    };
  });
  if (!indices.length) { showToast("请先勾选要导入的账号"); return; }
  try {
    const data = await uploadDocx(docxFile, false, indices, overrides);
    showToast(`导入完成：新增 ${data.added}，更新 ${data.updated}${data.skipped ? `，跳过 ${data.skipped}` : ""}`);
    closeModal();
    await loadAccountsPage(false);
    await loadOverview();
  } catch (e) {
    showToast(e.message);
  }
}

// ---------------- 出站 Webhook ----------------

async function loadWebhooks() {
  try {
    const data = await api("/api/webhooks");
    state.webhooks = data.webhooks || [];
    state.webhookTypes = data.types || [];
    renderWebhooks();
  } catch (e) {
    showToast(e.message);
  }
}

async function loadWebhookLog() {
  try {
    const data = await api("/api/webhooks/log?limit=20");
    const box = document.getElementById("webhookLog");
    box.innerHTML = data.items.length
      ? `<div class="item-list">${data.items.map((it) => `
          <div class="item">
            <div class="item-body">
              <div class="item-title" style="font-weight:500">${escapeHtml(it.event_type)}</div>
              <div class="item-meta"><span class="tag">${escapeHtml(it.url)}</span>${it.detail ? `<span>${escapeHtml(it.detail)}</span>` : ""}</div>
            </div>
            <span class="item-time">${it.ok ? "成功" : "失败"} · ${relTime(it.ts)}</span>
          </div>`).join("")}</div>`
      : `<div class="empty-tab">暂无投递记录</div>`;
  } catch (e) { /* 日志失败不影响主流程 */ }
}

function renderWebhooks() {
  const box = document.getElementById("webhookList");
  if (!state.webhooks.length) {
    box.innerHTML = `<div class="empty-tab">还没有配置出站 Webhook</div>`;
    return;
  }
  box.innerHTML = state.webhooks.map((h, i) => `
    <div class="webhook-row">
      <span class="webhook-name">${escapeHtml(h.name || "未命名")}</span>
      <span class="webhook-url">${escapeHtml(h.url)}</span>
      <span class="tag">${(h.events || []).length ? h.events.map(escapeHtml).join(", ") : "全部事件"}</span>
      <label style="display:inline-flex;align-items:center;gap:4px;color:var(--text-muted)">
        <input type="checkbox" data-webhook-enable="${i}" ${h.enabled ? "checked" : ""}> 启用
      </label>
      <button class="btn btn-sm" data-webhook-del="${i}">删除</button>
    </div>`).join("");
  box.querySelectorAll("[data-webhook-enable]").forEach((cb) =>
    cb.addEventListener("change", async () => {
      state.webhooks[Number(cb.dataset.webhookEnable)].enabled = cb.checked;
      await saveWebhooks();
    })
  );
  box.querySelectorAll("[data-webhook-del]").forEach((btn) =>
    btn.addEventListener("click", async () => {
      state.webhooks.splice(Number(btn.dataset.webhookDel), 1);
      await saveWebhooks();
      renderWebhooks();
    })
  );
}

async function saveWebhooks() {
  try {
    await api("/api/webhooks", { method: "POST", body: JSON.stringify({ webhooks: state.webhooks }) });
  } catch (e) {
    showToast(e.message);
  }
}

async function addWebhook() {
  const name = prompt("Webhook 名称", "");
  if (name === null) return;
  const url = prompt("推送 URL（POST JSON）", "");
  if (!url || !url.trim()) { showToast("请填写 URL"); return; }
  state.webhooks.push({ name: name.trim(), url: url.trim(), events: [], enabled: true });
  await saveWebhooks();
  renderWebhooks();
}

// ---------------- 账号画像 ----------------

async function loadProfiles() {
  try {
    const data = await api("/api/profiles");
    renderProfiles(data);
  } catch (e) {
    showToast(e.message);
  }
}

async function rebuildProfiles() {
  const btn = document.getElementById("profileRebuildBtn");
  btn.disabled = true;
  try {
    const data = await api("/api/profiles/rebuild", { method: "POST" });
    showToast("账号画像已重新聚类");
    renderProfiles(data);
    await loadAccountsPage(false);
  } catch (e) {
    showToast(e.message);
  } finally {
    btn.disabled = false;
  }
}

function renderProfiles(data) {
  const box = document.getElementById("profileSummary");
  if (!data.clusters.length) {
    box.innerHTML = `<div class="empty-tab">暂无画像数据</div>`;
    return;
  }
  const nameOf = (id) => {
    const acc = (state.accounts?.items || []).find((a) => a.id === id);
    return acc ? acc.username : `#${id}`;
  };
  box.innerHTML = `
    <div class="stat-grid">${data.clusters.map((c) => `
      <div class="stat-card"><span class="stat-num">${c.size}</span><span class="stat-label">${escapeHtml(c.label)}</span></div>`).join("")}
    </div>
    <div class="item-list">${data.accounts.map((a) => `
      <div class="item">
        <div class="item-body"><div class="item-title" style="font-weight:500">${escapeHtml(nameOf(a.account_id))}</div></div>
        <span class="tag tag-cluster">${escapeHtml(a.cluster_label)}</span>
      </div>`).join("")}</div>`;
}

// ---------------- 关系图谱 ----------------

async function loadGraph() {
  try {
    const data = await api("/api/graph");
    const badge = document.getElementById("graphBadge");
    badge.textContent = data.edges.length;
    badge.classList.toggle("hidden", data.edges.length === 0);
    document.getElementById("graphNote").textContent =
      `${data.nodes.length} 个账号 · ${data.edges.length} 条关系`;
    renderGraph(data);
  } catch (e) {
    showToast(e.message);
  }
  loadProfiles();
}

async function syncGraph() {
  const btn = document.getElementById("graphSyncBtn");
  btn.disabled = true;
  btn.textContent = "同步中…";
  try {
    const r = await api("/api/graph/sync", { method: "POST" });
    showToast(`关系数据同步完成：${r.total} 个账号`);
    await loadGraph();
  } catch (e) {
    showToast(e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = "同步关系数据";
  }
}

// 简单力导向布局：斥力 + 弹簧 + 向心，同步迭代后一次渲染
function renderGraph(data) {
  const svg = document.getElementById("graphCanvas");
  const empty = document.getElementById("graphEmpty");
  const nodes = data.nodes || [];
  const edges = data.edges || [];
  if (!nodes.length) {
    svg.innerHTML = "";
    empty.classList.remove("hidden");
    return;
  }
  empty.classList.add("hidden");
  const W = svg.clientWidth || 900;
  const H = 560;
  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);

  const byId = new Map();
  nodes.forEach((n, i) => {
    const angle = (i / Math.max(1, nodes.length)) * Math.PI * 2;
    byId.set(n.id, {
      ...n,
      x: W / 2 + Math.cos(angle) * 150,
      y: H / 2 + Math.sin(angle) * 150,
      vx: 0, vy: 0,
    });
  });
  const links = edges.filter((e) => byId.has(e.source) && byId.has(e.target));

  for (let k = 0; k < 120; k++) {
    for (let i = 0; i < nodes.length; i++) {
      const a = byId.get(nodes[i].id);
      for (let j = i + 1; j < nodes.length; j++) {
        const b = byId.get(nodes[j].id);
        const dx = a.x - b.x, dy = a.y - b.y;
        const d2 = dx * dx + dy * dy || 1;
        const d = Math.sqrt(d2);
        const f = 2600 / d2;
        a.vx += (dx / d) * f; a.vy += (dy / d) * f;
        b.vx -= (dx / d) * f; b.vy -= (dy / d) * f;
      }
    }
    for (const e of links) {
      const a = byId.get(e.source), b = byId.get(e.target);
      const dx = b.x - a.x, dy = b.y - a.y;
      const d = Math.hypot(dx, dy) || 1;
      const f = (d - 90) * 0.02;
      a.vx += (dx / d) * f; a.vy += (dy / d) * f;
      b.vx -= (dx / d) * f; b.vy -= (dy / d) * f;
    }
    for (const n of byId.values()) {
      n.vx += (W / 2 - n.x) * 0.012;
      n.vy += (H / 2 - n.y) * 0.012;
      n.vx *= 0.85; n.vy *= 0.85;
      n.x += Math.max(-8, Math.min(8, n.vx));
      n.y += Math.max(-8, Math.min(8, n.vy));
      n.x = Math.max(46, Math.min(W - 46, n.x));
      n.y = Math.max(40, Math.min(H - 44, n.y));
    }
  }

  const color = { follows: "var(--accent)", shared_repo: "var(--success)", shared_following: "var(--warning)" };
  const clipDefs = nodes.map((n) => `<clipPath id="clip-${n.id}"><circle cx="${n.x}" cy="${n.y}" r="15"/></clipPath>`).join("");
  const lines = links.map((e) => {
    const a = byId.get(e.source), b = byId.get(e.target);
    return `<line x1="${a.x}" y1="${a.y}" x2="${b.x}" y2="${b.y}" stroke="${color[e.type] || "var(--border)"}" stroke-width="1.4" opacity="0.55"><title>${escapeHtml(e.label)}</title></line>`;
  }).join("");
  const dots = nodes.map((n) => `
    <g>
      <circle cx="${n.x}" cy="${n.y}" r="15" fill="var(--bg)" stroke="var(--accent)" stroke-width="2"/>
      ${n.avatar_url ? `<image href="${escapeHtml(n.avatar_url)}" x="${n.x - 15}" y="${n.y - 15}" width="30" height="30" clip-path="url(#clip-${n.id})" onerror="this.remove()"/>` : ""}
      <text x="${n.x}" y="${n.y + 30}" text-anchor="middle" font-size="11" fill="var(--text)">${escapeHtml(n.username)}</text>
    </g>`).join("");
  svg.innerHTML = `<defs>${clipDefs}</defs>${lines}${dots}`;
}

// ---------------- 全部同步 ----------------

// 一键同步全部账号（与顶部「同步」相同），单账号的「同步」按钮保持不变
async function syncAllAccounts() {
  const btn = document.getElementById("syncAllAccountsBtn");
  btn.disabled = true;
  btn.textContent = "同步中…";
  try {
    const r = await api("/api/sync/run", { method: "POST" });
    showToast(`同步完成：成功 ${r.done}，失败 ${r.failed}`);
    state.detailCache = {};
    await loadAccountsPage(false);
  } catch (e) {
    showToast(e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = "全部同步";
  }
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
    ]);    document.getElementById("automationSummary").innerHTML = `
      <div class="stat-grid">
        <div class="stat-card"><span class="stat-num">${tasks.config.concurrency}</span><span class="stat-label">并发上限</span></div>
        <div class="stat-card"><span class="stat-num">${tasks.config.interval_seconds}s</span><span class="stat-label">同步间隔</span></div>
        <div class="stat-card"><span class="stat-num">${tasks.states.length}</span><span class="stat-label">已跟踪账号</span></div>
        <div class="stat-card"><span class="stat-num">${({ idle: "空闲", running: "同步中" })[tasks.progress?.status] || "空闲"}</span><span class="stat-label">同步状态</span></div>
        <div class="stat-card"><span class="stat-num">${tasks.progress.duration_seconds || 0}s</span><span class="stat-label">上轮耗时</span></div>
      </div>
      <p style="font-size:12px;color:var(--text-faint);margin-top:10px">
        上轮进度：${tasks.progress.done || 0}/${tasks.progress.total || 0}，失败 ${tasks.progress.failed || 0}
      </p>`;
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
  loadWebhooks();
  loadWebhookLog();
}

// ---------------- 邮箱 ----------------

function mailKeyInfo(key) {
  if (!key) return null;
  if (key.startsWith("inbox:")) {
    const id = Number(key.slice(6));
    const b = (state.email.inboxes || []).find((x) => x.id === id);
    return b
      ? { kind: "inbox", id, label: b.email || b.url, email: b.email || "", url: b.url }
      : null;
  }
  const id = Number(key);
  const a = (state.email.accounts || []).find((x) => x.id === id);
  return a
    ? { kind: "account", id, label: a.label || a.username, email: a.email || "", username: a.username }
    : null;
}

async function loadEmail() {
  try {
    const [settingsResp, inboxesResp] = await Promise.all([
      api("/api/email/settings"),
      api("/api/email/inboxes"),
    ]);
    state.email.settings = settingsResp.settings || {};
    state.email.accounts = settingsResp.accounts || [];
    state.email.inboxes = inboxesResp.items || [];
    if (!state.email.selected) {
      const firstAcc = (state.email.accounts || []).find((a) => a.email);
      const firstInbox = (state.email.inboxes || [])[0];
      state.email.selected = firstAcc
        ? `account:${firstAcc.id}`
        : firstInbox
          ? `inbox:${firstInbox.id}`
          : "";
    }
    renderMailboxList();
    await loadMessages();
  } catch (e) {
    document.getElementById("mailList").innerHTML =
      `<div class="empty-tab">加载失败：${escapeHtml(e.message)}</div>`;
  }
}

async function loadMessages() {
  const info = mailKeyInfo(state.email.selected);
  if (!info) {
    state.email.items = [];
    state.email.detailId = null;
    renderMailHero();
    renderMailList();
    return;
  }
  try {
    const q = state.email.q ? `&q=${encodeURIComponent(state.email.q)}` : "";
    const sort = state.email.sort === "code" ? "&sort=code" : "";
    const hasCode = state.email.filter === "code" ? "&hasCode=1" : "";
    const idParam = info.kind === "inbox" ? `inboxId=${info.id}` : `accountId=${info.id}`;
    const resp = await api(`/api/email/messages?limit=200&${idParam}${q}${sort}${hasCode}`);
    state.email.items = resp.items || [];
  } catch (e) {
    state.email.items = [];
    showToast(e.message);
  }
  state.email.detailId = null;
  renderMailHero();
  renderMailList();
}

function renderMailboxList() {
  const box = document.getElementById("mailboxList");
  const kw = (state.email.mailboxQ || "").trim().toLowerCase();
  const match = (label, email) =>
    !kw ||
    (label || "").toLowerCase().includes(kw) ||
    (email || "").toLowerCase().includes(kw);
  const acc = (state.email.accounts || []).filter(
    (a) => a.email && match(a.label || a.username, a.email),
  );
  const inb = (state.email.inboxes || []).filter((b) =>
    match(b.email || b.url, b.email),
  );
  const item = (key, label, sub) => `
    <div class="mailbox-item ${state.email.selected === key ? "active" : ""}" data-key="${escapeHtml(key)}">
      <div class="mailbox-name">${escapeHtml(label)}</div>
      ${sub ? `<div class="mailbox-sub">${escapeHtml(sub)}</div>` : ""}
    </div>`;
  let html = "";
  if (acc.length) {
    html += `<div class="mailbox-group">账号邮箱（${acc.length}）</div>` +
      acc.map((a) => item(`account:${a.id}`, a.label || a.username, a.email)).join("");
  }
  if (inb.length) {
    html += `<div class="mailbox-group">独立邮箱（${inb.length}）</div>` +
      inb.map((b) => item(`inbox:${b.id}`, b.email || b.url, "")).join("");
  }
  box.innerHTML = html || `<div class="empty-tab">暂无邮箱，点「导入邮箱」添加</div>`;
  box.querySelectorAll(".mailbox-item").forEach((el) => {
    el.addEventListener("click", () => {
      state.email.selected = el.dataset.key;
      renderMailboxList();
      loadMessages();
    });
  });
}

function renderMailHero() {
  const box = document.getElementById("mailHero");
  const info = mailKeyInfo(state.email.selected);
  if (!info) {
    box.innerHTML = `<div class="mail-hero-empty">从左侧选择邮箱</div>`;
    return;
  }
  const items = state.email.items || [];
  const withCode = items.find((m) => m.code);
  const title = info.kind === "inbox" ? info.label : info.label || info.username;
  box.innerHTML = `
    <div class="mail-hero-head">
      <div class="mail-hero-title">${escapeHtml(title)}</div>
      <div class="mail-hero-sub">${escapeHtml(info.email || info.url || "")}</div>
    </div>
    ${withCode ? `
      <div class="mail-hero-code">
        <div class="mail-hero-label">最新验证码</div>
        <div class="mail-hero-value">${escapeHtml(withCode.code)}</div>
        <button class="btn btn-primary" data-copy="${escapeHtml(withCode.code)}">复制验证码</button>
      </div>` : `
      <div class="mail-hero-empty">该邮箱暂无验证码邮件（共 ${items.length} 封）</div>`}`;
  box.querySelectorAll("[data-copy]").forEach((btn) =>
    btn.addEventListener("click", () => copyText(btn.dataset.copy)),
  );
}

function emailUnbound(n) {
  const badge = document.getElementById("emailBadge");
  badge.textContent = n;
  badge.classList.toggle("hidden", !n);
}

function highlightCodes(text, code) {
  const safe = escapeHtml(text || "");
  if (!code) return safe;
  const escaped = escapeHtml(code);
  if (!escaped || !safe.includes(escaped)) return safe;
  return safe.split(escaped).join(`<span class="code-chip">${escaped}</span>`);
}

function renderMailList() {
  const box = document.getElementById("mailList");
  const items = state.email.items || [];
  emailUnbound(items.length);
  document.getElementById("emailDesc").textContent = state.email.selected
    ? `当前邮箱共 ${items.length} 封（仅展示，验证码不会自动填充到任何网站）`
    : "从左侧选择邮箱，查看通知、安全邮件与验证码（仅展示，不会自动填充到任何网站）";
  if (state.email.detailId) {
    renderMailDetail(box);
    return;
  }
  if (!items.length) {
    box.innerHTML = `<div class="empty-tab">暂无邮件：点「刷新邮件」按配置拉取，或「导入 JSON」手动导入</div>`;
    return;
  }
  box.innerHTML = `<div class="mail-rows">${items.map((m) => `
    <div class="mail-row" data-id="${m.id}">
      <div class="mail-row-main">
        <div class="mail-row-subject">${escapeHtml(m.subject || "(无主题)")}</div>
        <div class="mail-row-meta">
          <span class="tag">${escapeHtml(m.sender || "-")}</span>
          <span>${relTime(m.received_at || m.created_at)}</span>
        </div>
      </div>
      ${m.code ? `<span class="code-chip code-chip-lg">${escapeHtml(m.code)}</span>` : '<span class="mail-row-code"></span>'}
    </div>`).join("")}</div>`;
  box.querySelectorAll(".mail-row").forEach((row) => {
    row.addEventListener("click", () => {
      state.email.detailId = Number(row.dataset.id);
      renderMailList();
    });
  });
}

function renderMailDetail(box) {
  const m = (state.email.items || []).find((x) => x.id === state.email.detailId);
  if (!m) {
    state.email.detailId = null;
    renderMailList();
    return;
  }
  box.innerHTML = `
    <div class="mail-detail">
      <div class="mail-detail-head">
        <button class="btn btn-sm" id="mailBackBtn">← 返回列表</button>
        <div class="mail-detail-title">${escapeHtml(m.subject || "(无主题)")}</div>
        <div class="mail-detail-meta">
          <span class="tag">${escapeHtml(m.sender || "-")}</span>
          <span>${relTime(m.received_at || m.created_at)}</span>
        </div>
      </div>
      ${m.code ? `<div class="mail-detail-code">验证码 <span class="code-chip code-chip-lg">${escapeHtml(m.code)}</span><button class="btn btn-sm" data-copy="${escapeHtml(m.code)}">复制</button></div>` : ""}
      <div class="mail-body mail-body-detail">${highlightCodes(m.body || "", m.code)}</div>
    </div>`;
  document.getElementById("mailBackBtn").addEventListener("click", () => {
    state.email.detailId = null;
    renderMailList();
  });
  box.querySelectorAll("[data-copy]").forEach((btn) =>
    btn.addEventListener("click", () => copyText(btn.dataset.copy)),
  );
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    showToast(`已复制验证码 ${text}`);
  } catch (e) {
    showToast("复制失败，请手动选择验证码");
  }
}

async function refreshEmail() {
  try {
    const res = await api("/api/email/refresh", { method: "POST", body: "{}" });
    if (!res.ok) {
      showToast(res.hint || "拉取未启用：请在「邮箱设置」开启并配置取件链接");
      return;
    }
    const added = res.results.reduce((s, r) => s + (r.added || 0), 0);
    const codes = res.results.map((r) => r.verificationCode).filter(Boolean);
    if (codes.length) showToast(`已刷新，新增 ${added} 封；当前验证码 ${codes[0]}`);
    else showToast(`已刷新，新增 ${added} 封`);
    await loadEmail();
  } catch (e) {
    showToast(e.message);
  }
}

function importEmailFile() {
  const input = document.createElement("input");
  input.type = "file";
  input.accept = "json,application/json";
  input.onchange = async () => {
    const file = input.files && input.files[0];
    if (!file) return;
    try {
      const data = JSON.parse(await file.text());
      const messages = Array.isArray(data) ? data : (data.messages || []);
      if (!messages.length) { showToast("文件中没有邮件"); return; }
      const res = await api("/api/email/import", {
        method: "POST",
        body: JSON.stringify({ messages }),
      });
      showToast(`导入完成：新增 ${res.added}，跳过 ${res.skipped}`);
      await loadEmail();
    } catch (e) {
      showToast("导入失败：" + e.message);
    }
  };
  input.click();
}

// ---------------- 导入邮箱清单（邮笺导出） ----------------

async function openEmailInboxImport() {
  document.getElementById("emailInboxText").value = "";
  document.getElementById("emailInboxPreview").innerHTML = "";
  document.getElementById("emailInboxFile").value = "";
  document.getElementById("emailInboxDoImportBtn").disabled = true;
  document.getElementById("emailInboxModal").classList.remove("hidden");
}

function handleEmailInboxFile(e) {
  const file = e.target.files && e.target.files[0];
  if (!file) return;
  file.text().then((text) => {
    document.getElementById("emailInboxText").value = text;
    parseEmailInboxes();
  }).catch((err) => showToast("读取文件失败：" + err.message));
}

async function parseEmailInboxes() {
  const text = document.getElementById("emailInboxText").value;
  const preview = document.getElementById("emailInboxPreview");
  if (!text.trim()) {
    preview.innerHTML = `<div class="empty-tab">请粘贴文本或选择文件</div>`;
    return;
  }
  try {
    const data = await api("/api/email/inboxes/parse", {
      method: "POST",
      body: JSON.stringify({ text }),
    });
    const rows = data.candidates || [];
    state.email.inboxCandidates = rows;
    if (!rows.length) {
      preview.innerHTML = `<div class="empty-tab">未解析到邮箱，请检查格式</div>`;
      document.getElementById("emailInboxDoImportBtn").disabled = true;
      return;
    }
    const matched = rows.filter((r) => r.accountId).length;
    preview.innerHTML = `<table class="mail-table"><thead><tr>
      <th></th><th>邮箱 / 标识</th><th>取件链接</th><th>账号匹配</th></tr></thead><tbody>
      ${rows.map((c, i) => `<tr class="${c.accountId ? "" : "row-invalid"}">
        <td class="col-check"><input type="checkbox" data-inbox="${i}" checked></td>
        <td>${escapeHtml(c.email || "-")}</td>
        <td class="cell-sub">${escapeHtml(c.url || "-")}</td>
        <td>${c.accountId ? escapeHtml(c.accountUsername) : '<span class="tag">作为独立邮箱</span>'}</td>
      </tr>`).join("")}</tbody></table>
      <p class="page-desc">已匹配 ${matched} / ${rows.length} 条：匹配的会回填到账号「关联邮箱」，未匹配的导入为独立邮箱（不依赖账号）。</p>`;
    preview.querySelectorAll("[data-inbox]").forEach((cb) => {
      cb.addEventListener("change", () => {
        const any = preview.querySelectorAll("[data-inbox]:checked").length;
        document.getElementById("emailInboxDoImportBtn").disabled = !any;
      });
    });
    document.getElementById("emailInboxDoImportBtn").disabled = !rows.length;
  } catch (e) {
    preview.innerHTML = `<div class="empty-tab">解析失败：${escapeHtml(e.message)}</div>`;
  }
}

async function importEmailInboxes() {
  const preview = document.getElementById("emailInboxPreview");
  const idxs = Array.from(preview.querySelectorAll("[data-inbox]:checked"))
    .map((cb) => Number(cb.dataset.inbox));
  const items = idxs.map((i) => state.email.inboxCandidates[i]).filter(Boolean)
    .map((c) => ({ accountId: c.accountId || null, email: c.email, url: c.url }));
  if (!items.length) return;
  try {
    const res = await api("/api/email/inboxes/import", {
      method: "POST",
      body: JSON.stringify({ items }),
    });
    showToast(`已导入：关联 ${res.updated} 个账号`);
    document.getElementById("emailInboxModal").classList.add("hidden");
    await loadEmail();
    try { await loadAccountsPage(false); } catch (e) { /* 列表页未加载时忽略 */ }
  } catch (e) {
    showToast("导入失败：" + e.message);
  }
}

function openEmailSettings() {
  const s = state.email.settings || {};
  document.getElementById("emailEnabled").checked = !!s.enabled;
  document.getElementById("emailSiteUrl").value = s.site_url || "";
  document.getElementById("emailApiBase").value = s.api_base || "";
  document.getElementById("emailToken").value = s.token || "";
  document.getElementById("emailInterval").value = s.fetch_interval_seconds ?? 300;
  document.getElementById("emailCodePattern").value = s.code_pattern || "";
  document.getElementById("emailContext").checked = s.highlight_context !== false;
  document.getElementById("emailSettingsResult").textContent = "";
  document.getElementById("emailModal").classList.remove("hidden");
}

async function saveEmailSettings() {
  const body = {
    enabled: document.getElementById("emailEnabled").checked,
    site_url: document.getElementById("emailSiteUrl").value.trim(),
    api_base: document.getElementById("emailApiBase").value.trim(),
    token: document.getElementById("emailToken").value.trim(),
    fetch_interval_seconds: Number(document.getElementById("emailInterval").value || 0),
    code_pattern: document.getElementById("emailCodePattern").value.trim(),
    highlight_context: document.getElementById("emailContext").checked,
  };
  try {
    await api("/api/email/settings", { method: "POST", body: JSON.stringify(body) });
    document.getElementById("emailModal").classList.add("hidden");
    showToast("已保存邮箱设置");
    await loadEmail();
  } catch (e) {
    const box = document.getElementById("emailSettingsResult");
    box.className = "test-result fail";
    box.textContent = e.message;
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
  if (state.detailTab === "social") box.innerHTML = socialHtml(d);
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
  return `<div class="panel"><div class="panel-head"><h3>与当前列表对比</h3></div>
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

// GitHub 事件 payload.action 的中文说法
const ACTION_TYPES = {
  opened: "开启", closed: "关闭", created: "创建", edited: "编辑", deleted: "删除",
  merged: "合并", pushed: "推送", labeled: "加标签", unlabeled: "移除标签",
  assigned: "分配", unassigned: "取消分配", reopened: "重新开启", synchronized: "同步",
  published: "发布", forked: "Fork", starred: "Star", completed: "完成",
  started: "开始", reviewed: "评审", dismissed: "驳回", added: "添加", removed: "移除",
};

function eventList(events) {
  if (!events.length) return `<div class="empty-tab">最近没有动态</div>`;
  return `<div class="item-list">${events.map((ev) => {
    const t = EVENT_TYPES[ev.type] || { text: ev.type };
    const act = ACTION_TYPES[ev.payload?.action] || ev.payload?.action;
    const actor = ev.actor || {};
    const repo = ev.repo || {};
    return `
    <div class="item">
      ${actor.avatar_url ? `<img class="activity-actor" src="${escapeHtml(actor.avatar_url)}" alt="" onerror="handleImgError(this)">` : ""}
      <div class="item-body">
        <div class="item-title" style="font-weight:500">${escapeHtml(actor.login || "")} ${escapeHtml(t.text)}${act ? " · " + escapeHtml(act) : ""}</div>
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

function personGrid(items, emptyText) {
  if (!items.length) return `<div class="empty-tab">${emptyText}</div>`;
  return `<div class="person-grid">${items.map((p) => `
    <a class="person-card" href="${safeUrl(p.html_url)}" target="_blank" rel="noopener" title="@${escapeHtml(p.login)}">
      <img src="${escapeHtml(p.avatar_url)}" alt="" onerror="handleImgError(this)">
      <span class="person-login">${escapeHtml(p.login)}</span>
    </a>`).join("")}</div>`;
}

function socialHtml(d) {
  const followers = d.follower_list || [];
  const following = d.following_list || [];
  return `
    <div class="panel">
      <div class="panel-head">
        <h3>粉丝（${followers.length}）</h3>
        <span class="panel-note">最多显示 50 个</span>
      </div>
      ${personGrid(followers, "暂无粉丝数据，打开详情页后会自动拉取")}
    </div>
    <div class="panel">
      <div class="panel-head">
        <h3>关注（${following.length}）</h3>
        <span class="panel-note">最多显示 50 个</span>
      </div>
      ${personGrid(following, "暂无关注数据，打开详情页后会自动拉取")}
    </div>`;
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
    state.detailCache = {};
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

let prefetchInFlight = false;
const PREFETCH_LIMIT = 10;
const PREFETCH_CONCURRENCY = 2;

function setPrefetch(on) {
  state.prefetchEnabled = on;
  const cb = document.getElementById("menuPrefetch");
  if (cb) cb.checked = on;
  showToast(on ? "已开启详情预取（会消耗 GitHub 配额）" : "已关闭详情预取");
  if (on) runPrefetch();
}

// ---------------- 更多操作菜单 ----------------

function openMoreMenu() {
  document.getElementById("moreMenu").classList.toggle("hidden");
}

function closeMoreMenu() {
  document.getElementById("moreMenu").classList.add("hidden");
}

// 限量预取当前列表前若干个账号的详情，写入缓存供详情页秒开。
async function runPrefetch() {
  if (prefetchInFlight) return;
  const ids = (state.accounts?.items || []).slice(0, PREFETCH_LIMIT).map((a) => a.id);
  if (!ids.length) return;
  prefetchInFlight = true;
  let next = 0;
  async function worker() {
    while (next < ids.length) {
      const id = ids[next++];
      try {
        const data = await api(`/api/status/${id}`);
        state.detailCache[id] = data.account;
      } catch (e) { /* 单个失败不影响其他 */ }
    }
  }
  try {
    await Promise.all(Array.from({ length: PREFETCH_CONCURRENCY }, worker));
  } finally {
    prefetchInFlight = false;
  }
}

// ---------------- 版本 ----------------

async function loadVersion() {
  try {
    const v = await api("/api/version");
    state.version = v.version;
    document.getElementById("versionText").textContent = "v" + v.version;
  } catch (e) { /* ignore */ }
}

async function checkUpdate() {
  try {
    const r = await api("/api/update-check");
    if (!r.ok) { showToast(r.error || "无法检查更新"); return; }
    if (r.latest && r.latest !== state.version) showToast(`有新版本 ${r.latest}`);
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

// ---------------- 添加账号 ----------------

function openModal() {
  document.getElementById("modal").classList.remove("hidden");
  document.getElementById("modal").classList.remove("modal-lg");
  document.getElementById("fUsername").focus();
}

function closeModal() {
  document.getElementById("modal").classList.add("hidden");
  ["fLabel", "fUsername", "fGroup", "fTags", "fNote", "fEmail", "fToken"].forEach((id) => (document.getElementById(id).value = ""));
  document.getElementById("fType").value = "personal";
  document.getElementById("testResult").textContent = "";
  // 重置 Word 导入
  const modalEl = document.getElementById("modal");
  modalEl.classList.remove("modal-lg");
  const preview = document.getElementById("docxPreview");
  preview.classList.add("hidden");
  preview.innerHTML = "";
  document.getElementById("docxFile").value = "";
  docxFile = null;
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
  const email = document.getElementById("fEmail").value.trim();
  if (!username || !token) { showToast("请填写用户名和 Token"); return; }
  try {
    await api("/api/accounts", { method: "POST", body: JSON.stringify({ label, username, token, group, type, tags, note, email }) });
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

function focusGlobalSearch() {
  const input = document.getElementById("globalSearchInput");
  if (!input) return;
  input.focus();
  input.select();
  if (input.value.trim()) doGlobalSearch();
}

function closeGlobalSearch() {
  const box = document.getElementById("globalSearchResults");
  if (box) box.classList.add("hidden");
}

async function doGlobalSearch() {
  const q = document.getElementById("globalSearchInput").value.trim();
  const kind = document.getElementById("globalSearchKind").value;
  const box = document.getElementById("globalSearchResults");
  if (!q) {
    box.classList.add("hidden");
    box.innerHTML = "";
    return;
  }
  box.classList.remove("hidden");
  box.innerHTML = `<div class="search-dropdown-hint">搜索中…</div>`;
  try {
    const data = await api(`/api/search?q=${encodeURIComponent(q)}&kind=${encodeURIComponent(kind)}`);
    const results = data.results || [];
    if (!results.length) {
      box.innerHTML = `<div class="search-dropdown-hint">没有匹配的结果</div>`;
      return;
    }
    box.innerHTML = `<div class="search-dropdown-list">${results.slice(0, 12).map((r) => `
      <a class="search-item" href="${safeUrl(r.html_url)}" target="_blank" rel="noopener">
        <div class="search-item-title">${escapeHtml(r.title)}</div>
        <div class="search-item-meta">
          <span class="tag">${escapeHtml(r.repository)}</span>
          <span class="tag tag-account">${escapeHtml(r.account_label)}</span>
          ${(r.labels || []).slice(0, 3).map((l) => `<span class="tag">${escapeHtml(l)}</span>`).join("")}
        </div>
      </a>`).join("")}</div>
      <div class="search-dropdown-hint">共 ${data.total} 条，搜索了 ${data.accounts} 个账号${results.length < data.total ? `（显示前 ${results.length} 条）` : ""}</div>`;
  } catch (e) {
    box.innerHTML = `<div class="search-dropdown-hint">搜索失败：${escapeHtml(e.message)}</div>`;
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

function openShortcutsModal() {
  document.getElementById("shortcutsModal").classList.remove("hidden");
}

function closeShortcutsModal() {
  document.getElementById("shortcutsModal").classList.add("hidden");
}

function onKeydown(e) {
  if (e.key === "Escape") {
    ["modal", "backupModal", "notifyModal", "syncModal", "shortcutsModal"].forEach((id) => {
      const el = document.getElementById(id);
      if (el) el.classList.add("hidden");
    });
    closeGlobalSearch();
    closeMoreMenu();
    return;
  }
  const typing = document.activeElement && ["INPUT", "TEXTAREA", "SELECT"].includes(document.activeElement.tagName);
  if (typing) return;
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") { e.preventDefault(); focusGlobalSearch(); return; }
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  const k = e.key.toLowerCase();
  if (k === "t") toggleTheme();
  if (k === "r") syncNow();
  if (k === "a") openModal();
  if (k === "/") { e.preventDefault(); focusGlobalSearch(); }
  if (k === "?") { e.preventDefault(); openShortcutsModal(); }
}

// ---------------- 事件绑定 ----------------

function bindEvents() {
  document.querySelectorAll(".nav-item").forEach((n) => n.addEventListener("click", () => go(n.dataset.page)));
  document.getElementById("syncNowBtn").addEventListener("click", syncNow);
  document.getElementById("themeBtn").addEventListener("click", toggleTheme);
  document.getElementById("addBtn").addEventListener("click", openModal);

  // 全局搜索框
  document.getElementById("globalSearchInput").addEventListener("input", debounce(doGlobalSearch, 300));
  document.getElementById("globalSearchInput").addEventListener("focus", () => {
    if (document.getElementById("globalSearchInput").value.trim()) doGlobalSearch();
  });
  document.getElementById("globalSearchInput").addEventListener("keydown", (e) => {
    if (e.key === "Escape") { closeGlobalSearch(); document.getElementById("globalSearchInput").blur(); }
  });
  document.getElementById("globalSearchKind").addEventListener("change", doGlobalSearch);

  // 更多操作菜单
  document.getElementById("moreBtn").addEventListener("click", (e) => { e.stopPropagation(); openMoreMenu(); });
  document.getElementById("menuSyncSettings").addEventListener("click", () => { closeMoreMenu(); openSyncModal(); });
  document.getElementById("menuNotify").addEventListener("click", () => { closeMoreMenu(); openNotifyModal(); });
  document.getElementById("menuPrefetch").addEventListener("change", (e) => { closeMoreMenu(); setPrefetch(e.target.checked); });
  document.getElementById("menuCheckUpdate").addEventListener("click", () => { closeMoreMenu(); checkUpdate(); });
  document.getElementById("menuShortcuts").addEventListener("click", () => { closeMoreMenu(); openShortcutsModal(); });
  document.addEventListener("click", (e) => {
    if (!e.target.closest("#globalSearchWrap")) closeGlobalSearch();
    if (!e.target.closest(".menu-wrap")) closeMoreMenu();
  });

  document.getElementById("cancelBtn").addEventListener("click", closeModal);
  document.getElementById("saveBtn").addEventListener("click", saveAccount);
  document.getElementById("testBtn").addEventListener("click", testConnection);
  document.getElementById("docxFile").addEventListener("change", handleDocxFile);
  document.getElementById("modal").addEventListener("click", (e) => { if (e.target.id === "modal") closeModal(); });

  document.getElementById("exportBtn").addEventListener("click", exportBackup);
  document.getElementById("exportCredBtn").addEventListener("click", exportCredBackup);
  document.getElementById("importFile").addEventListener("change", (e) => { if (e.target.files[0]) importBackup(e.target.files[0]); });
  document.getElementById("backupCloseBtn").addEventListener("click", closeBackupModal);
  document.getElementById("backupModal").addEventListener("click", (e) => { if (e.target.id === "backupModal") closeBackupModal(); });

  document.getElementById("notifyCloseBtn").addEventListener("click", closeNotifyModal);
  document.getElementById("notifyModal").addEventListener("click", (e) => { if (e.target.id === "notifyModal") closeNotifyModal(); });
  document.getElementById("notifyAddBtn").addEventListener("click", addNotifyChannel);
  document.getElementById("notifyTestBtn").addEventListener("click", testNotification);

  document.getElementById("syncSaveBtn").addEventListener("click", saveSyncSettings);
  document.getElementById("openSyncSettings").addEventListener("click", openSyncModal);
  document.getElementById("automationSyncNow").addEventListener("click", syncNow);
  document.getElementById("syncCloseBtn").addEventListener("click", () => document.getElementById("syncModal").classList.add("hidden"));
  document.getElementById("shortcutsCloseBtn").addEventListener("click", closeShortcutsModal);
  document.getElementById("shortcutsModal").addEventListener("click", (e) => { if (e.target.id === "shortcutsModal") closeShortcutsModal(); });
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
  document.getElementById("batchPause").addEventListener("click", () => batchSetSync(false));
  document.getElementById("batchResume").addEventListener("click", () => batchSetSync(true));
  document.getElementById("batchTag").addEventListener("click", batchTag);
  document.getElementById("batchGroup").addEventListener("click", batchGroup);
  document.getElementById("syncAllAccountsBtn").addEventListener("click", syncAllAccounts);
  document.getElementById("webhookAddBtn").addEventListener("click", addWebhook);
  document.getElementById("graphSyncBtn").addEventListener("click", syncGraph);
  document.getElementById("profileRebuildBtn").addEventListener("click", rebuildProfiles);

  document.getElementById("emailRefreshBtn").addEventListener("click", refreshEmail);
  document.getElementById("emailImportBtn").addEventListener("click", importEmailFile);
  document.getElementById("emailInboxImportBtn").addEventListener("click", openEmailInboxImport);
  document.getElementById("emailInboxFileBtn").addEventListener("click", () => document.getElementById("emailInboxFile").click());
  document.getElementById("emailInboxFile").addEventListener("change", handleEmailInboxFile);
  document.getElementById("emailInboxParseBtn").addEventListener("click", parseEmailInboxes);
  document.getElementById("emailInboxClose").addEventListener("click", () => document.getElementById("emailInboxModal").classList.add("hidden"));
  document.getElementById("emailInboxDoImportBtn").addEventListener("click", importEmailInboxes);
  document.getElementById("emailInboxModal").addEventListener("click", (e) => { if (e.target.id === "emailInboxModal") document.getElementById("emailInboxModal").classList.add("hidden"); });
  document.getElementById("emailSettingsBtn").addEventListener("click", openEmailSettings);
  document.getElementById("emailSettingsClose").addEventListener("click", () => document.getElementById("emailModal").classList.add("hidden"));
  document.getElementById("emailSettingsSave").addEventListener("click", saveEmailSettings);
  document.getElementById("emailModal").addEventListener("click", (e) => { if (e.target.id === "emailModal") document.getElementById("emailModal").classList.add("hidden"); });
  document.getElementById("emailSearch").addEventListener("input", debounce(() => {
    state.email.q = document.getElementById("emailSearch").value.trim();
    loadMessages();
  }, 300));
  document.getElementById("emailSort").addEventListener("change", (e) => {
    state.email.sort = e.target.value;
    loadMessages();
  });
  document.getElementById("emailFilter").addEventListener("change", (e) => {
    state.email.filter = e.target.value;
    loadMessages();
  });
  document.getElementById("emailMailboxSearch").addEventListener("input", (e) => {
    state.email.mailboxQ = e.target.value;
    renderMailboxList();
  });

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

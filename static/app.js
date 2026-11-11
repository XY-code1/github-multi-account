/* GitHub 多账号状态仪表盘 —— 前端逻辑 */
"use strict";

const state = {
  accounts: [],        // 账号列表（含状态摘要）
  selectedId: null,
  detail: null,        // 当前账号完整状态
  activeTab: "review",
  searchQuery: "",     // 账号列表过滤关键字
  timer: null,
};

// ---------------- 工具函数 ----------------

function escapeHtml(str) {
  return String(str ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function repoName(url) {
  if (!url) return "";
  const parts = url.replace(/\/$/, "").split("/");
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

function statusText(status) {
  return { active: "有效", invalid: "无效", error: "异常", unknown: "未检查" }[status] || status;
}

function showToast(msg) {
  const toast = document.getElementById("toast");
  toast.textContent = msg;
  toast.classList.remove("hidden");
  clearTimeout(showToast._t);
  showToast._t = setTimeout(() => toast.classList.add("hidden"), 2600);
}

// ---------------- API ----------------

async function api(path, options = {}) {
  const resp = await fetch(path, {
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    ...options,
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.detail || `请求失败（${resp.status}）`);
  return data;
}

async function loadAccounts() {
  const data = await api("/api/status");
  state.accounts = data.accounts || [];
  renderAccountList();
  return state.accounts;
}

async function loadDetail(id) {
  const data = await api(`/api/status/${id}`);
  state.detail = data.account;
  renderDetail();
}

async function refreshAll(selectFirstIfNone = false) {
  try {
    await loadAccounts();
    if (state.selectedId) {
      await loadDetail(state.selectedId);
    } else if (selectFirstIfNone && state.accounts.length) {
      selectAccount(state.accounts[0].id);
      return;
    }
    updateLastRefresh();
  } catch (e) {
    showToast(e.message);
  }
}

// ---------------- 渲染：账号侧栏 ----------------

function renderAccountList() {
  const list = document.getElementById("accountList");
  const query = state.searchQuery.trim().toLowerCase();

  const accounts = state.accounts.filter((a) => {
    if (!query) return true;
    const user = a.user || {};
    return (
      (a.label || "").toLowerCase().includes(query) ||
      (a.username || "").toLowerCase().includes(query) ||
      (user.login || "").toLowerCase().includes(query)
    );
  });

  document.getElementById("accountCount").textContent = query
    ? `${accounts.length}/${state.accounts.length}`
    : state.accounts.length;
  document.getElementById("clearSearch").classList.toggle("hidden", !query);

  if (!state.accounts.length) {
    list.innerHTML = `<div class="empty-tab">暂无账号</div>`;
    return;
  }
  if (!accounts.length) {
    list.innerHTML = `<div class="empty-tab">没有匹配的账号</div>`;
    return;
  }

  list.innerHTML = accounts.map((a) => {
    const user = a.user || {};
    const avatar = user.avatar_url
      ? `<img class="account-avatar" src="${escapeHtml(user.avatar_url)}" alt="">`
      : `<div class="account-avatar account-avatar-placeholder">${escapeHtml((a.label || "?")[0])}</div>`;
    const counts = a.status === "active"
      ? `<span>${a.review_prs.length} PR</span><span>·</span><span>${a.assigned_issues.length} Issue</span>`
      : "";
    const err = a.error ? `<div class="account-user" style="color:var(--red)">${escapeHtml(a.error)}</div>` : "";
    return `
      <div class="account-card ${a.id === state.selectedId ? "selected" : ""} ${a.status === "invalid" || a.status === "error" ? "invalid" : ""}"
           data-id="${a.id}">
        ${avatar}
        <div class="account-info">
          <div class="account-name">${escapeHtml(a.label)}</div>
          <div class="account-user">${escapeHtml(user.login || a.username || "")}</div>
          <div class="account-status">
            <span class="dot ${escapeHtml(a.status)}"></span>
            <span>${statusText(a.status)}</span>
            ${counts}
          </div>
          ${err}
        </div>
        <button class="account-del" data-del="${a.id}" title="删除账号">×</button>
      </div>`;
  }).join("");

  list.querySelectorAll(".account-card").forEach((card) => {
    card.addEventListener("click", (e) => {
      if (e.target.closest("[data-del]")) return;
      selectAccount(Number(card.dataset.id));
    });
  });
  list.querySelectorAll("[data-del]").forEach((btn) => {
    btn.addEventListener("click", (e) => {
      e.stopPropagation();
      deleteAccount(Number(btn.dataset.del));
    });
  });
}

// ---------------- 渲染：详情 ----------------

function renderDetail() {
  const d = state.detail;
  if (!d) return;

  document.getElementById("emptyState").classList.add("hidden");
  document.getElementById("detail").classList.remove("hidden");

  // 头部
  const user = d.user || {};
  document.getElementById("avatar").src = user.avatar_url || "";
  document.getElementById("detailName").textContent = d.label || d.username;
  const rate = d.rate_limit?.resources?.core;
  const rateText = rate ? ` · API 剩余 ${rate.remaining}/${rate.limit}` : "";
  document.getElementById("detailMeta").textContent =
    `${user.login || d.username || ""}${user.name ? "（" + user.name + "）" : ""}${rateText}`;
  const badge = document.getElementById("statusBadge");
  badge.className = `status-badge ${escapeHtml(d.status)}`;
  badge.innerHTML = `<span class="dot ${escapeHtml(d.status)}"></span>${statusText(d.status)}`;

  // 统计
  document.getElementById("statReview").textContent = d.review_prs.length;
  document.getElementById("statMyPr").textContent = d.my_prs.length;
  document.getElementById("statIssue").textContent = d.assigned_issues.length;
  document.getElementById("statEvents").textContent = d.events.length;

  renderTab();
}

function renderTab() {
  document.querySelectorAll("#tabs .tab").forEach((t) => {
    t.classList.toggle("active", t.dataset.tab === state.activeTab);
  });
  const content = document.getElementById("tabContent");
  const d = state.detail;
  if (!d) return;

  switch (state.activeTab) {
    case "review":
      content.innerHTML = prList(d.review_prs, "待我 Review");
      break;
    case "mypr":
      content.innerHTML = prList(d.my_prs, null);
      break;
    case "issues":
      content.innerHTML = issueList(d.assigned_issues);
      break;
    case "events":
      content.innerHTML = eventList(d.events);
      break;
    case "repos":
      content.innerHTML = repoList(d.repos);
      break;
  }
}

function prList(items, note) {
  if (!items.length) return `<div class="empty-tab">${note ? "没有待处理的 PR" : "没有发起中的 PR"}</div>`;
  return `<div class="item-list">${items.map((it) => `
    <div class="item">
      <div class="item-icon pr">PR</div>
      <div class="item-body">
        <a class="item-title" href="${escapeHtml(it.html_url)}" target="_blank" rel="noopener">${escapeHtml(it.title)}</a>
        <div class="item-meta">
          <span class="repo-tag">${escapeHtml(repoName(it.repository_url))}</span>
          <span>#${it.number}</span>
          <span>@${escapeHtml(it.user?.login || "")}</span>
          ${it.labels?.slice(0, 3).map((l) => `<span class="label-tag">${escapeHtml(l.name)}</span>`).join("")}
        </div>
      </div>
      <span class="item-time">${relTime(it.created_at)}</span>
    </div>`).join("")}</div>`;
}

function issueList(items) {
  if (!items.length) return `<div class="empty-tab">没有分配给你的 Issue</div>`;
  return `<div class="item-list">${items.map((it) => `
    <div class="item">
      <div class="item-icon issue">IS</div>
      <div class="item-body">
        <a class="item-title" href="${escapeHtml(it.html_url)}" target="_blank" rel="noopener">${escapeHtml(it.title)}</a>
        <div class="item-meta">
          <span class="repo-tag">${escapeHtml(repoName(it.repository_url))}</span>
          <span>#${it.number}</span>
          <span>@${escapeHtml(it.user?.login || "")}</span>
          ${it.labels?.slice(0, 3).map((l) => `<span class="label-tag">${escapeHtml(l.name)}</span>`).join("")}
        </div>
      </div>
      <span class="item-time">${relTime(it.created_at)}</span>
    </div>`).join("")}</div>`;
}

const EVENT_TYPES = {
  PushEvent: { text: "推送了提交", color: "var(--green)" },
  PullRequestEvent: { text: "处理了 PR", color: "var(--accent)" },
  IssuesEvent: { text: "处理了 Issue", color: "var(--yellow)" },
  WatchEvent: { text: "Star 了仓库", color: "var(--yellow)" },
  CreateEvent: { text: "创建了分支/标签", color: "var(--purple)" },
  DeleteEvent: { text: "删除了分支/标签", color: "var(--red)" },
  ReleaseEvent: { text: "发布了 Release", color: "var(--green)" },
  ForkEvent: { text: "Fork 了仓库", color: "var(--text-secondary)" },
  IssueCommentEvent: { text: "评论了 Issue", color: "var(--text-secondary)" },
  CommitCommentEvent: { text: "评论了提交", color: "var(--text-secondary)" },
  PullRequestReviewEvent: { text: "评审了 PR", color: "var(--accent)" },
  PullRequestReviewCommentEvent: { text: "评论了 PR", color: "var(--accent)" },
  MemberEvent: { text: "成员变更", color: "var(--text-secondary)" },
};

function eventList(events) {
  if (!events.length) return `<div class="empty-tab">最近没有动态（received_events 仅包含公开可见事件）</div>`;
  return `<div class="item-list">${events.map((ev) => {
    const type = EVENT_TYPES[ev.type] || { text: ev.type, color: "var(--text-secondary)" };
    const actor = ev.actor || {};
    const repo = ev.repo || {};
    const action = ev.payload?.action ? ` · ${escapeHtml(ev.payload.action)}` : "";
    return `
    <div class="item">
      <img class="item-actor" src="${escapeHtml(actor.avatar_url || "")}" alt="">
      <div class="item-body">
        <div class="item-title" style="font-weight:500">
          <span style="color:${type.color}">${escapeHtml(actor.login || "")}</span>
          ${escapeHtml(type.text)}${action}
        </div>
        <div class="item-meta">
          <span class="repo-tag">${escapeHtml(repo.name || "")}</span>
        </div>
      </div>
      <span class="item-time">${relTime(ev.created_at)}</span>
    </div>`;
  }).join("")}</div>`;
}

function repoList(repos) {
  if (!repos.length) return `<div class="empty-tab">没有公开仓库</div>`;
  return `<div class="item-list">${repos.map((r) => `
    <div class="item">
      <div class="item-icon" style="background:var(--accent-soft);color:var(--accent)">◇</div>
      <div class="item-body">
        <a class="item-title" href="${escapeHtml(r.html_url)}" target="_blank" rel="noopener">${escapeHtml(r.name)}</a>
        <div class="item-meta">
          <span>★ ${r.stargazers_count ?? 0}</span>
          <span>⑂ ${r.forks_count ?? 0}</span>
          ${r.language ? `<span>${escapeHtml(r.language)}</span>` : ""}
          ${r.private ? `<span class="label-tag">私有</span>` : ""}
        </div>
      </div>
      <span class="item-time">更新于 ${relTime(r.updated_at)}</span>
    </div>`).join("")}</div>`;
}

// ---------------- 交互 ----------------

function selectAccount(id) {
  state.selectedId = id;
  renderAccountList();
  loadDetail(id).catch((e) => showToast(e.message));
}

function clearSearch() {
  document.getElementById("searchInput").value = "";
  state.searchQuery = "";
  renderAccountList();
  document.getElementById("searchInput").focus();
}

async function deleteAccount(id) {
  const acc = state.accounts.find((a) => a.id === id);
  if (!acc) return;
  if (!confirm(`确定删除账号「${acc.label}」吗？`)) return;
  try {
    await api(`/api/accounts/${id}`, { method: "DELETE" });
    if (state.selectedId === id) {
      state.selectedId = null;
      state.detail = null;
      document.getElementById("detail").classList.add("hidden");
      document.getElementById("emptyState").classList.remove("hidden");
    }
    showToast("已删除");
    await refreshAll();
  } catch (e) {
    showToast(e.message);
  }
}

function updateLastRefresh() {
  document.getElementById("lastRefresh").textContent =
    "更新于 " + new Date().toLocaleTimeString("zh-CN");
}

// ---------------- 模态框 ----------------

function openModal() {
  document.getElementById("modal").classList.remove("hidden");
  document.getElementById("fLabel").focus();
}

function closeModal() {
  document.getElementById("modal").classList.add("hidden");
  document.getElementById("fLabel").value = "";
  document.getElementById("fUsername").value = "";
  document.getElementById("fToken").value = "";
  document.getElementById("testResult").textContent = "";
}

async function testConnection() {
  const username = document.getElementById("fUsername").value.trim();
  const token = document.getElementById("fToken").value.trim();
  const box = document.getElementById("testResult");
  if (!username || !token) {
    box.className = "test-result fail";
    box.textContent = "请填写用户名和 Token";
    return;
  }
  box.className = "test-result";
  box.textContent = "测试中…";
  try {
    const v = await api("/api/verify", {
      method: "POST",
      body: JSON.stringify({ label: "", username, token }),
    });
    if (v.status === "active") {
      box.className = "test-result ok";
      box.textContent = `✓ 连接成功：@${v.user?.login || username}`;
    } else {
      box.className = "test-result fail";
      box.textContent = `✗ ${v.error || "连接失败"}`;
    }
  } catch (e) {
    box.className = "test-result fail";
    box.textContent = e.message;
  }
}

async function saveAccount() {
  const label = document.getElementById("fLabel").value.trim();
  const username = document.getElementById("fUsername").value.trim();
  const token = document.getElementById("fToken").value.trim();
  if (!username || !token) {
    showToast("请填写用户名和 Token");
    return;
  }
  try {
    await api("/api/accounts", {
      method: "POST",
      body: JSON.stringify({ label, username, token }),
    });
    showToast("已保存");
    closeModal();
    await refreshAll();
  } catch (e) {
    showToast(e.message);
  }
}

// ---------------- 事件绑定与启动 ----------------

document.getElementById("refreshBtn").addEventListener("click", () => refreshAll());
document.getElementById("addBtn").addEventListener("click", openModal);
document.getElementById("cancelBtn").addEventListener("click", closeModal);
document.getElementById("saveBtn").addEventListener("click", saveAccount);
document.getElementById("testBtn").addEventListener("click", testConnection);

document.getElementById("searchInput").addEventListener("input", (e) => {
  state.searchQuery = e.target.value;
  renderAccountList();
});
document.getElementById("searchInput").addEventListener("keydown", (e) => {
  if (e.key === "Escape") {
    e.stopPropagation();
    clearSearch();
  }
});
document.getElementById("clearSearch").addEventListener("click", clearSearch);

document.getElementById("modal").addEventListener("click", (e) => {
  if (e.target.id === "modal") closeModal();
});

document.querySelectorAll("#tabs .tab").forEach((t) => {
  t.addEventListener("click", () => {
    state.activeTab = t.dataset.tab;
    renderTab();
  });
});

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") closeModal();
});

async function init() {
  await refreshAll(true);
  // 每 2 分钟自动刷新
  state.timer = setInterval(() => refreshAll(), 120000);
}

init().catch((e) => showToast(e.message));

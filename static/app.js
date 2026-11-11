/* GitHub 多账号状态仪表盘 —— 前端逻辑 */
"use strict";

const state = {
  accounts: [],        // 账号列表（含状态摘要）
  selectedId: null,
  detail: null,        // 当前账号完整状态
  activeTab: "overview",
  searchQuery: "",     // 账号列表过滤关键字
  history: [],         // 当前账号的历史快照
  metric: "followers", // 趋势图当前指标
  timer: null,
  notifications: false,      // 是否开启变化桌面提醒
  previousCounts: {},        // 上一次各账号的 PR/Issue 计数（用于对比）
  detailRequestId: 0,        // 详情请求代际，丢弃过期响应
  historyRequestId: 0,       // 历史请求代际
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

/** 只允许 http/https/相对路径，阻止 javascript: 等协议 */
function safeUrl(url) {
  const s = String(url || "").trim().toLowerCase();
  if (s.startsWith("https://") || s.startsWith("http://") || s.startsWith("/")) {
    return escapeHtml(url);
  }
  return "#";
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
  const map = { active: "有效", invalid: "无效", error: "异常", unknown: "未检查" };
  return escapeHtml(map[status] || status);
}

function showToast(msg) {
  const toast = document.getElementById("toast");
  toast.textContent = msg;
  toast.classList.remove("hidden");
  clearTimeout(showToast._t);
  showToast._t = setTimeout(() => toast.classList.add("hidden"), 2600);
}

// 头像加载失败时降级为占位图
const AVATAR_FALLBACK =
  "data:image/svg+xml," +
  encodeURIComponent(
    "<svg xmlns='http://www.w3.org/2000/svg' width='40' height='40'><rect width='40' height='40' rx='20' fill='#21262d'/></svg>"
  );

function handleImgError(img) {
  img.onerror = null;
  img.src = AVATAR_FALLBACK;
}

// ---------------- API ----------------

async function api(path, options = {}) {
  const resp = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.detail || `请求失败（${resp.status}）`);
  return data;
}

async function loadAccounts() {
  const data = await api("/api/status");
  const accounts = data.accounts || [];
  checkNotifications(accounts);
  state.accounts = accounts;
  renderAccountList();
  return state.accounts;
}

function checkNotifications(accounts) {
  const current = Object.fromEntries(
    accounts.map((a) => [a.id, { review: a.review_prs_count ?? 0, issues: a.assigned_issues_count ?? 0 }])
  );
  if (state.notifications) {
    for (const a of accounts) {
      const prev = state.previousCounts[a.id];
      if (!prev) continue;
      const review = current[a.id].review;
      const issues = current[a.id].issues;
      if (review > prev.review || issues > prev.issues) {
        const parts = [];
        if (review > prev.review) parts.push(`待处理 PR +${review - prev.review}`);
        if (issues > prev.issues) parts.push(`分配 Issue +${issues - prev.issues}`);
        try {
          new Notification(`GitHub · ${a.label}`, { body: parts.join("，") });
        } catch (e) { /* 某些环境不允许构造通知，忽略 */ }
      }
    }
  }
  state.previousCounts = current;
}

async function loadDetail(id) {
  const reqId = ++state.detailRequestId;
  const data = await api(`/api/status/${id}`);
  if (reqId !== state.detailRequestId) return; // 已有更新的请求，丢弃过期响应
  state.detail = data.account;
  renderDetail();
}

let refreshInFlight = false;

async function refreshAll(selectFirstIfNone = false) {
  if (refreshInFlight) return; // 防止定时刷新与手动刷新重叠
  refreshInFlight = true;
  try {
    await loadAccounts();
    // 账号被删除后 id 会重排，失效的选中要清空
    if (state.selectedId && !state.accounts.some((a) => a.id === state.selectedId)) {
      state.selectedId = null;
      state.detail = null;
      document.getElementById("detail").classList.add("hidden");
      document.getElementById("emptyState").classList.remove("hidden");
    }
    if (state.selectedId) {
      await loadDetail(state.selectedId);
    } else if (selectFirstIfNone && state.accounts.length) {
      selectAccount(state.accounts[0].id);
      updateLastRefresh();
      return; // selectAccount 内部已加载详情
    }
    updateLastRefresh();
  } catch (e) {
    showToast(e.message);
  } finally {
    refreshInFlight = false;
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
      ? `<img class="account-avatar" src="${escapeHtml(user.avatar_url)}" alt="" onerror="handleImgError(this)">`
      : `<div class="account-avatar account-avatar-placeholder">${escapeHtml((a.label || "?")[0])}</div>`;
    const counts = a.status === "active"
      ? `<span>${a.review_prs_count ?? 0} PR</span><span>·</span><span>${a.assigned_issues_count ?? 0} Issue</span>`
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

  // 部分数据获取失败的提示
  const warnBox = document.getElementById("detailWarnings");
  if (warnBox) {
    warnBox.innerHTML = (d.warnings || []).length
      ? `<div class="warn-bar">部分数据获取失败：${d.warnings.map(escapeHtml).join("、")}</div>`
      : "";
  }

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
    case "overview":
      content.innerHTML = overviewHtml(d);
      bindMetricPills();
      bindSnapshotBtn();
      bindExportHistoryBtn();
      loadHistory(d.id);
      break;
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

const METRICS = [
  { key: "followers", label: "粉丝" },
  { key: "following", label: "关注" },
  { key: "total_stars", label: "Star" },
  { key: "review_prs", label: "PR" },
  { key: "assigned_issues", label: "Issue" },
];

function overviewHtml(d) {
  const stats = `
    <div class="stats">
      <div class="stat-card"><span class="stat-num">${d.followers_count}</span><span class="stat-label">粉丝</span></div>
      <div class="stat-card"><span class="stat-num">${d.following_count}</span><span class="stat-label">关注</span></div>
      <div class="stat-card"><span class="stat-num">${d.total_stars}</span><span class="stat-label">总 Star</span></div>
      <div class="stat-card"><span class="stat-num">${d.public_repos}</span><span class="stat-label">公开仓库</span></div>
    </div>`;

  const topRepos = d.top_repos || [];
  const maxStars = Math.max(1, ...topRepos.map((r) => r.stars));
  const bars = topRepos.length
    ? `<div class="bar-chart">${topRepos.map((r) => `
        <div class="bar-row">
          <a class="bar-label" href="${safeUrl(r.html_url)}" target="_blank" rel="noopener" title="${escapeHtml(r.name)}">${escapeHtml(r.name)}</a>
          <div class="bar-track"><div class="bar-fill" style="width:${((r.stars / maxStars) * 100).toFixed(1)}%"></div></div>
          <span class="bar-value">★ ${r.stars}</span>
        </div>`).join("")}</div>`
    : `<div class="empty-tab">没有 Star 数据</div>`;

  const colors = ["var(--text-muted)", "var(--green)", "var(--accent)", "var(--purple)", "var(--yellow)"];
  const dist = d.star_distribution || [];
  const distTotal = dist.reduce((sum, x) => sum + x.count, 0);
  const distSegs = dist.map((x, i) =>
    `<div class="dist-seg" style="width:${distTotal ? ((x.count / distTotal) * 100).toFixed(2) : 0}%;background:${colors[i % colors.length]}" title="${escapeHtml(x.label)}: ${x.count}"></div>`
  ).join("");
  const distLegend = dist.map((x, i) =>
    `<span><span class="dist-dot" style="background:${colors[i % colors.length]}"></span>${escapeHtml(x.label)} · ${x.count}</span>`
  ).join("");
  const distHtml = `<div class="dist-bar">${distSegs}</div><div class="dist-legend">${distLegend}</div>`;

  const avatarGrid = (list) => (list || []).length
    ? `<div class="avatar-grid">${list.map((u) => `
        <a class="avatar-chip" href="${safeUrl(u.html_url)}" target="_blank" rel="noopener" title="${escapeHtml(u.login)}">
          <img src="${escapeHtml(u.avatar_url)}" alt="" loading="lazy">${escapeHtml(u.login)}
        </a>`).join("")}</div>`
    : `<div class="empty-tab">暂无数据</div>`;

  return `
    ${stats}
    <div class="panel">
      <h3>趋势 <span class="muted">历史快照</span></h3>
      <div class="metric-pills" id="metricPills">
        ${METRICS.map((m) => `<button class="pill ${state.metric === m.key ? "active" : ""}" data-metric="${m.key}">${m.label}</button>`).join("")}
      </div>
      <div id="trendContent"><div class="empty-tab">加载中...</div></div>
      <div class="trend-footer">
        <button id="snapshotBtn" class="btn">记录当前快照</button>
        <button id="exportHistoryBtn" class="btn">导出 CSV</button>
        <span id="trendStats"></span>
      </div>
    </div>
    <div class="panel">
      <h3>仓库 Star 排行 <span class="muted">Top ${topRepos.length}</span></h3>
      ${bars}
    </div>
    <div class="panel">
      <h3>Star 分布 <span class="muted">共 ${distTotal} 个仓库</span></h3>
      ${distHtml}
    </div>
    <div class="panel">
      <h3>粉丝 <span class="muted">前 ${(d.follower_list || []).length} 个</span></h3>
      ${avatarGrid(d.follower_list)}
    </div>
    <div class="panel">
      <h3>关注 <span class="muted">前 ${(d.following_list || []).length} 个</span></h3>
      ${avatarGrid(d.following_list)}
    </div>`;
}

function lineChart(points, label) {
  if (!points.length) return `<div class="empty-tab">暂无历史数据</div>`;
  const w = 640, h = 180, padL = 44, padR = 14, padT = 14, padB = 26;
  const xs = points.map((p) => p.t);
  const ys = points.map((p) => p.v);
  const minX = Math.min(...xs), maxX = Math.max(...xs);
  const minY = Math.min(...ys), maxY = Math.max(...ys);
  const spanX = maxX - minX || 1;
  const spanY = maxY - minY || Math.max(1, maxY * 0.1);
  const px = (t) => padL + ((t - minX) / spanX) * (w - padL - padR);
  const py = (v) => padT + (1 - (v - minY) / spanY) * (h - padT - padB);

  const path = points.map((p, i) => `${i ? "L" : "M"}${px(p.t).toFixed(1)},${py(p.v).toFixed(1)}`).join(" ");
  const area = `${path} L${px(maxX).toFixed(1)},${(h - padB).toFixed(1)} L${px(minX).toFixed(1)},${(h - padB).toFixed(1)} Z`;

  let grid = "";
  const ticks = 4;
  for (let i = 0; i <= ticks; i++) {
    const v = minY + (spanY * i) / ticks;
    const y = py(v).toFixed(1);
    grid += `<line x1="${padL}" y1="${y}" x2="${w - padR}" y2="${y}" stroke="#21262d" stroke-width="1"/>`;
    grid += `<text x="${padL - 6}" y="${+y + 4}" text-anchor="end" font-size="10" fill="#6e7681">${Math.round(v)}</text>`;
  }

  const fmt = (t) => new Date(t).toLocaleDateString("zh-CN", { month: "numeric", day: "numeric" });
  const xLabels =
    `<text x="${padL}" y="${h - 8}" font-size="10" fill="#6e7681">${fmt(minX)}</text>` +
    `<text x="${w - padR}" y="${h - 8}" text-anchor="end" font-size="10" fill="#6e7681">${fmt(maxX)}</text>`;

  return `<svg viewBox="0 0 ${w} ${h}" class="trend-chart" role="img" aria-label="${escapeHtml(label)}趋势">
    ${grid}
    <path d="${area}" fill="rgba(88,166,255,0.12)" stroke="none"/>
    <path d="${path}" fill="none" stroke="#58a6ff" stroke-width="2" stroke-linejoin="round"/>
    ${xLabels}
  </svg>`;
}

function renderTrend() {
  const box = document.getElementById("trendContent");
  if (!box) return;
  const conf = METRICS.find((m) => m.key === state.metric) || METRICS[0];
  const history = state.history || [];
  const stats = document.getElementById("trendStats");
  if (!history.length) {
    box.innerHTML = `<div class="empty-tab">暂无历史数据，运行一段时间后自动生成</div>`;
    if (stats) stats.textContent = "";
    return;
  }
  const points = history
    .map((h) => ({ t: new Date(h.created_at).getTime(), v: Number(h[conf.key]) || 0 }))
    .filter((p) => Number.isFinite(p.t) && Number.isFinite(p.v));
  if (!points.length) {
    box.innerHTML = `<div class="empty-tab">暂无有效历史数据</div>`;
    if (stats) stats.textContent = "";
    return;
  }
  const first = history[0][conf.key];
  const last = history[history.length - 1][conf.key];
  const diff = last - first;
  const pct = first ? ((diff / first) * 100).toFixed(1) : null;
  const trend = diff > 0
    ? `↗ +${diff}${pct !== null ? ` (${pct}%)` : ""}`
    : diff < 0
    ? `↘ ${diff}${pct !== null ? ` (${pct}%)` : ""}`
    : "→ 无变化";
  box.innerHTML = lineChart(points, conf.label);
  if (stats) stats.textContent = `${conf.label}：首条 ${first} → 最新 ${last}（${trend}）`;
}

async function loadHistory(accountId) {
  const box = document.getElementById("trendContent");
  if (!box) return;
  const reqId = ++state.historyRequestId;
  try {
    const data = await api(`/api/history/${accountId}?limit=300`);
    if (reqId !== state.historyRequestId || accountId !== state.selectedId) return;
    state.history = data.history || [];
    renderTrend();
  } catch (e) {
    if (reqId !== state.historyRequestId) return;
    state.history = [];
    box.innerHTML = `<div class="empty-tab">无法加载历史数据</div>`;
  }
}

function bindMetricPills() {
  document.querySelectorAll("#metricPills .pill").forEach((p) => {
    p.addEventListener("click", () => {
      state.metric = p.dataset.metric;
      document.querySelectorAll("#metricPills .pill").forEach((x) => x.classList.toggle("active", x === p));
      renderTrend();
    });
  });
}

function bindSnapshotBtn() {
  const btn = document.getElementById("snapshotBtn");
  if (btn) btn.addEventListener("click", takeSnapshot);
}

async function takeSnapshot() {
  if (!state.detail) return;
  try {
    await api(`/api/snapshot/${state.detail.id}`, { method: "POST" });
    showToast("已记录快照");
    await loadHistory(state.detail.id);
  } catch (e) {
    showToast(e.message);
  }
}

function bindExportHistoryBtn() {
  const btn = document.getElementById("exportHistoryBtn");
  if (btn) btn.addEventListener("click", exportHistory);
}

async function exportHistory() {
  if (!state.detail) return;
  try {
    const data = await api(`/api/history/${state.detail.id}?limit=1000`);
    const history = data.history || [];
    if (!history.length) {
      showToast("暂无历史数据");
      return;
    }
    const headers = ["时间", "粉丝", "关注", "总Star", "公开仓库", "待处理PR", "我的PR", "分配Issue", "动态"];
    const rows = history.map((h) => [
      h.created_at, h.followers, h.following, h.total_stars, h.public_repos,
      h.review_prs, h.my_prs, h.assigned_issues, h.events,
    ]);
    // \uFEFF 是 BOM，保证 Excel 正确识别 UTF-8
    const csv = "\uFEFF" + [headers, ...rows].map((r) => r.join(",")).join("\n");
    const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `github-history-${state.detail.username || state.detail.id}-${new Date().toISOString().slice(0, 10)}.csv`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    showToast("历史已导出");
  } catch (e) {
    showToast(e.message);
  }
}

function prList(items, note) {
  if (!items.length) return `<div class="empty-tab">${note ? "没有待处理的 PR" : "没有发起中的 PR"}</div>`;
  return `<div class="item-list">${items.map((it) => `
    <div class="item">
      <div class="item-icon pr">PR</div>
      <div class="item-body">
        <a class="item-title" href="${safeUrl(it.html_url)}" target="_blank" rel="noopener">${escapeHtml(it.title)}</a>
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
        <a class="item-title" href="${safeUrl(it.html_url)}" target="_blank" rel="noopener">${escapeHtml(it.title)}</a>
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
        <a class="item-title" href="${safeUrl(r.html_url)}" target="_blank" rel="noopener">${escapeHtml(r.name)}</a>
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
  const loader = document.getElementById("detailLoading");
  if (loader) loader.classList.remove("hidden");
  loadDetail(id)
    .catch((e) => showToast(e.message))
    .finally(() => {
      if (loader) loader.classList.add("hidden");
    });
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

function openBackupModal() {
  document.getElementById("backupModal").classList.remove("hidden");
  document.getElementById("backupResult").textContent = "";
  document.getElementById("importFile").value = "";
}

function closeBackupModal() {
  document.getElementById("backupModal").classList.add("hidden");
}

async function exportBackup() {
  if (!confirm("备份文件包含 Token 明文，确定导出吗？")) return;
  try {
    const resp = await fetch("/api/accounts/export");
    if (!resp.ok) throw new Error("导出失败");
    const blob = await resp.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `github-accounts-backup-${new Date().toISOString().slice(0, 10)}.json`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    showToast("备份已导出");
  } catch (e) {
    showToast(e.message);
  }
}

async function importBackup(file) {
  const box = document.getElementById("backupResult");
  try {
    const text = await file.text();
    const data = JSON.parse(text);
    const result = await api("/api/accounts/import", {
      method: "POST",
      body: JSON.stringify(data),
    });
    box.className = "test-result ok";
    box.textContent = `✓ 导入完成：新增 ${result.added} 个，更新 ${result.updated} 个${result.skipped ? `，跳过 ${result.skipped} 个` : ""}`;
    await refreshAll();
  } catch (e) {
    box.className = "test-result fail";
    box.textContent = "导入失败：" + e.message;
  }
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

// ---------------- 通知 / 刷新间隔 ----------------

async function toggleNotifications() {
  if (state.notifications) {
    state.notifications = false;
    updateNotifyBtn();
    showToast("已关闭变化提醒");
    return;
  }
  if (!("Notification" in window)) {
    showToast("当前浏览器不支持桌面通知");
    return;
  }
  if (Notification.permission === "denied") {
    showToast("通知权限已在浏览器设置中被拒绝，请先允许");
    return;
  }
  const perm = await Notification.requestPermission();
  state.notifications = perm === "granted";
  updateNotifyBtn();
  showToast(state.notifications ? "已开启变化提醒" : "通知权限被拒绝");
}

function updateNotifyBtn() {
  const btn = document.getElementById("notifyBtn");
  if (!btn) return;
  btn.classList.toggle("active", state.notifications);
  btn.textContent = state.notifications ? "提醒·开" : "提醒";
}

function startRefreshTimer() {
  if (state.timer) {
    clearInterval(state.timer);
    state.timer = null;
  }
  const sec = Number(document.getElementById("refreshInterval").value);
  if (sec > 0) state.timer = setInterval(() => refreshAll(), sec * 1000);
}

// ---------------- 事件绑定与启动 ----------------

document.getElementById("refreshBtn").addEventListener("click", () => refreshAll());
document.getElementById("notifyBtn").addEventListener("click", toggleNotifications);
document.getElementById("refreshInterval").addEventListener("change", startRefreshTimer);
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

document.getElementById("backupBtn").addEventListener("click", openBackupModal);
document.getElementById("backupCloseBtn").addEventListener("click", closeBackupModal);
document.getElementById("backupModal").addEventListener("click", (e) => {
  if (e.target.id === "backupModal") closeBackupModal();
});
document.getElementById("exportBtn").addEventListener("click", exportBackup);
document.getElementById("importFile").addEventListener("change", (e) => {
  if (e.target.files && e.target.files[0]) importBackup(e.target.files[0]);
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
  startRefreshTimer();
  updateNotifyBtn();
}

init().catch((e) => showToast(e.message));

# GitHub 多账号运维中心

面向**数百个 GitHub 账号**的运维中心：全局状态总览、跨账号统一待办、后台自动同步、服务端搜索/筛选/排序/分页。

## 页面结构

| 页面 | 用途 |
| --- | --- |
| **账号总览**（默认首页） | 全局统计卡、今日需要处理（按优先级）、最近活动 |
| **全部账号** | 服务端分页表格、搜索/筛选/排序、批量操作 |
| **统一待办** | 跨账号聚合 Review 请求、PR、Issue、凭据/配额/同步异常 |
| **自动化任务** | 同步配置、任务进度、审计日志 |
| **账号详情**（二级） | 概览、账号对比、PR/Issue、动态、仓库、历史趋势 |

## 核心能力

- **后台同步引擎**：定时同步所有账号（默认 15 分钟、并发 5），失败隔离 + 指数退避重试，结果写入本地缓存；带错峰、按配额自适应间隔、单账号冷却等反风控策略
- **全局聚合接口**：`/api/dashboard/summary`、`/api/dashboard/tasks`、`/api/dashboard/activities`，全部读缓存，打开页面不会打爆 GitHub
- **服务端账号查询**：`page`/`pageSize`/`query`/`status`/`type`/`group`/`tags`/`sort`/`order`，支持数百账号
- **账号元数据**：别名、内部编号、标签、分组、备注、类型（个人/Bot/组织）
- **安全**：Token 加密存储、导出默认不含明文、审计日志、敏感字段脱敏
- 历史快照 + 趋势、外部通知（Bark/Server酱/企业微信/Webhook）、全局搜索、主题切换、快捷键

- **登录有效性** —— Token 是否有效、是否过期、速率限制剩余
- **数据概览** —— 粉丝 / 关注 / 总 Star 统计，仓库 Star 排行与分布可视化
- **账号对比** —— 多账号指标并排对比，粉丝数条形图，一键加载各账号 Star
- **历史趋势** —— 自动记录状态快照（10 分钟节流），折线图看粉丝 / Star / PR 变化，支持导出 CSV
- **备份恢复** —— 导出 / 导入账号备份，支持**可选密码加密**（PBKDF2 + AES-GCM）
- **Word 文档导入** —— 上传 `.docx` 自动识别 Token 与用户名，调 GitHub 验证后预览并批量导入
- **变化提醒** —— 可选开启桌面通知，PR / Issue 增加时提醒
- **账号搜索** —— 侧栏按显示名 / 用户名实时筛选账号，账号多时快速定位
- **详情预取** —— 后台静默预取所有账号详情，任意切换秒开（可在顶栏关闭）
- **外部通知** —— Bark / Server酱 / 企业微信 / Webhook，关掉页面也能收到 PR / Issue 变化
- **账号分组** —— 给账号打分组标签，侧栏按分组折叠展示
- **全局搜索** —— `Ctrl+K` 跨所有账号搜索 PR / Issue 标题，一次找全
- **列表过滤** —— PR / Issue 列表按仓库 / 标签 / 关键字即时过滤
- **待处理 PR** —— 等待你 Review 的开放 PR
- **我的 PR** —— 你发起的开放 PR 状态
- **分配 Issue** —— 分配给你的开放 Issue
- **仓库动态** —— received events（被 @、仓库被 Star、关注的更新等）
- **仓库列表** —— 最近更新的公开仓库
- **粉丝 / 关注** —— 详情页展示具体是哪些人（带头像卡片），不再只是数字
- **账号头像** —— 账号列表展示 GitHub 头像，多个账号一眼区分
- **一键全部同步** —— 账号列表工具栏一键同步全部账号（单账号「同步」按钮保留）
- **API 已封顶归类** —— 剩余额度 ≤ 60/小时（未认证额度）的账号计为异常，进入总览风险与高优先级待办

### 进阶能力（2026-11 新增）

- **增量同步** —— GitHub 客户端带 ETag 发条件请求，未变化（304）不消耗 API 配额；几百个账号时配额消耗可降一个数量级
- **跨账号去重** —— 同一个 PR / Issue 被多个账号看到时合并成一条待办，标注「N 个账号相关」
- **MCP Server** —— `GitHub仪表盘.exe --mcp` 以 stdio 暴露 MCP 工具（查待办、账号列表、总览、详情、同步、批量打标签、审计，以及 star 仓库 / 关注用户 / 创建 PR），供 Claude / Cursor 直接调用
- **出站 Webhook** —— 把账号事件（PR/Issue 增加、凭据失效、封顶、同步失败）推送到 n8n / Zapier / 自建服务，带投递日志
- **账号关系图谱** —— 关注关系、共同仓库、共同关注三类边，SVG 力导向布局展示
- **账号画像自动聚类** —— 按粉丝 / Star / 仓库 / 参与度 / 活跃度做 k-means 分群（核心 / 活跃 / 沉寂），作为打标签参考
- **主动操作** —— 以指定账号 Star 仓库、关注用户、创建 Pull Request（同仓库分支或 fork 两种模式），写操作前做配额预检，star / follow / PR 均幂等，全部记审计
- **邮箱面板** —— 查看账号关联邮箱的通知 / 安全邮件 / 登录验证码，验证码高亮 + 一键复制（仅展示，不自动填充），支持导入 JSON 与按配置拉取
- **主题切换** —— 浅色 / 深色一键切换，默认跟随系统偏好，自动记忆
- **键盘快捷键** —— 刷新、切标签页、账号导航等全流程键盘操作

后端代理所有 GitHub API 请求，**Token 不暴露给浏览器**，只以脱敏形式展示。

## 快速开始

**单文件 exe 版（推荐分发给别人）**：

> ✅ exe 已与 Python 版对齐「多账号运维中心」：后台同步引擎、全局聚合、服务端分页、统一待办、审计日志、四个一级页面全部可用。

直接双击项目根目录的 **`GitHub仪表盘.exe`** —— **无需 Python、无需安装任何依赖**，单文件、无控制台窗口、自动打开浏览器。它和 Python 版共用同一份 `config/` 配置（账号、通知渠道、历史数据都在里面），Token 不用重录。详见 [go-app/README.md](go-app/README.md)。

**自己开发 / 调试用 Python 版**：

- **Windows（无黑框，推荐）**：双击 `Start-Hidden.vbs` —— 后台启动服务、自动打开浏览器，不显示控制台窗口
- **Windows（可见控制台）**：双击 `start.bat`
- **macOS / Linux**：终端执行 `./start.sh`

脚本会自动创建 `.venv`、安装依赖、复制配置模板，并**自动探测可用端口**（Windows 上 Hyper-V / WSL / Docker 可能保留部分端口，直接写死 8000 会报 `WinError 10013`）。启动后自动打开 `http://localhost:<端口>`。

**停止服务**：运行 `stop.bat`（结束所有以 `app.main` 启动的 Python 进程）。

**手动启动**：

```bash
# 1. 安装依赖（建议虚拟环境）
pip install -r requirements.txt

# 2. 配置账号
cp config/accounts.example.yaml config/accounts.yaml
# 编辑 accounts.yaml，填入你的 GitHub 用户名和 Token

# 3. 启动（端口被占用或不可用时换一个，如 8080）
uvicorn app.main:app --reload --port 8000
```

浏览器打开提示的地址即可。也可以在页面上点「添加账号」直接录入（支持「测试连接」，不会保存无效 Token）。

## Token 权限

在 GitHub → Settings → Developer settings → Personal access tokens 创建。

**Fine-grained token**（推荐）：至少勾选 Public Repository 的 `Pull requests`、`Issues`、`Contents`、`Metadata`（只读）。

如果要用 Star / 关注 / 创建 PR 这类写操作，还需要给对应仓库的 `Contents`（读写，用于建分支 / 提交文件）与 `Pull requests`（读写）权限；只读 Token 执行写操作会返回 403。

**Classic token**：`repo:status`、`read:user`、`read:project` 即可；公开仓库用 `public_repo` 也够。

> ⚠️ `config/accounts.yaml` 已加入 `.gitignore`，请勿提交。Token 等同于账号凭证，泄露后请立即在 GitHub 上撤销。

## 配置格式

`accounts.yaml` 中的 Token **以密文存储**（Fernet 对称加密，密钥为本机生成的 `config/.secret.key`），明文不会写入文件。首次加载旧版明文配置时会自动迁移为加密格式。

```yaml
accounts:
  - label: 主账号            # 仪表盘显示名称
    username: XY-code1      # 仅用于展示，实际登录以 token 为准
    token_encrypted: gAAAAAB...   # 由程序自动生成，勿手动编辑
```

你可以继续用 `token: ghp_xxxx` 明文书写（首次启动会自动加密迁移），也可以通过页面上的「添加账号」录入。

可通过环境变量 `GAD_CONFIG_DIR` 指定配置目录（默认 `config/`）。

> 🔑 **密钥丢失 = Token 全部失效**：`config/.secret.key` 丢失后，已加密的 Token 无法解密，需要重新录入。请把它和 `accounts.yaml` 一起备份（或直接用页面上的「备份」按钮导出明文备份）。
>
> 程序检测到「有密文但密钥缺失 / 密钥不匹配」时会**拒绝启动读取**并报错，不会用新密钥覆盖旧密文。配置文件采用「临时文件 + 原子替换」写入，并保留 `accounts.yaml.bak`，避免中途崩溃导致配置损坏。

## 键盘快捷键

| 按键 | 功能 |
| --- | --- |
| `T` | 切换浅色 / 深色主题 |
| `R` | 刷新全部账号 |
| `A` | 添加账号 |
| `/` | 聚焦账号搜索框 |
| `1` ~ `6` | 切换标签页（概览 / 待处理 PR / 我的 PR / 分配 Issue / 仓库动态 / 仓库） |
| `J` / `K`（或 `↑` / `↓`） | 账号列表下移 / 上移 |
| `Enter` | 打开高亮的账号 |
| `Esc` | 关闭弹窗 / 清除搜索 |
| `Ctrl/⌘ + K` | 全局搜索 |
| `?` | 查看本说明 |
> 快捷键在输入框内不会触发。主题偏好保存在浏览器本地存储，首次访问跟随系统。

## API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/accounts` | 账号列表（脱敏），支持 `page/pageSize/query/status/type/group/tags/sort/order` |
| POST | `/api/accounts` | 新增/更新账号 |
| DELETE | `/api/accounts/{id}` | 删除账号 |
| POST | `/api/accounts/batch-refresh` | 批量刷新 |
| POST | `/api/accounts/batch-toggle-sync` | 批量启用 / 暂停同步 |
| POST | `/api/accounts/batch-tag` | 批量打标签 |
| POST | `/api/accounts/batch-group` | 批量移动分组 |
| POST | `/api/accounts/{id}/sync` | 立即同步单个账号 |
| POST | `/api/verify` | 测试 Token 有效性（不保存） |
| GET | `/api/status` | 全部账号汇总（轻量） |
| GET | `/api/status/{id}` | 单个账号完整详情 |
| GET | `/api/accounts/export` | 导出账号信息备份（**不含 Token**） |
| POST | `/api/accounts/export` | 导出凭据备份（含 Token，可加密） |
| POST | `/api/accounts/import` | 从备份导入账号 |
| POST | `/api/accounts/import-docx` | 上传 .docx 识别 Token / 用户名（`preview=true` 只预览） |
| GET | `/api/history/{id}` | 账号历史快照（时间升序） |
| POST | `/api/snapshot/{id}` | 立即记录一条快照 |
| GET | `/api/dashboard/summary` | 全局账号总览 |
| GET | `/api/dashboard/tasks` | 统一待办（支持 `priority/type/accountId` 筛选） |
| GET | `/api/dashboard/activities` | 跨账号最近动态（重复活动已聚合） |
| GET | `/api/graph` | 账号关系图谱（关注 / 共同仓库 / 共同关注） |
| POST | `/api/graph/sync` | 慢速同步粉丝 / 关注 / 代表仓库 |
| GET | `/api/profiles` | 账号画像（k-means 自动聚类） |
| POST | `/api/profiles/rebuild` | 重新聚类 |
| GET | `/api/webhooks` | 出站 Webhook 配置与事件类型 |
| POST | `/api/webhooks` | 保存出站 Webhook 配置 |
| GET | `/api/webhooks/log` | Webhook 投递日志 |
| POST | `/api/actions/star_repo` | 以指定账号 Star 仓库（幂等） |
| POST | `/api/actions/follow_user` | 以指定账号关注用户（幂等） |
| POST | `/api/actions/create_pr` | 以指定账号创建 Pull Request（同仓库 / fork 两种模式） |

### 主动操作（Star / Follow / 创建 PR）

三个写操作端点都接收 `accountId`，用该账号自己的 Token 执行，行为一致：

- 先做**配额预检**：core 剩余 < 50 次时直接拒绝（返回 403），避免写操作打穿后台同步的配额；
- **幂等**：重复 star / follow 无副作用；创建 PR 前会先查同源同目标的开 PR，存在则直接返回该 PR（`action=existing`）；
- 命中 403 / 429 时把 `Retry-After` 或 `X-RateLimit-Reset` 换算成等待秒数返回；
- 全部写入审计日志（`github.star` / `github.follow` / `github.create_pr`）。

```jsonc
// POST /api/actions/star_repo
{ "accountId": 1, "owner": "torvalds", "repo": "linux" }

// POST /api/actions/follow_user
{ "accountId": 1, "username": "octocat" }

// POST /api/actions/create_pr（fork 模式：自动 fork + 建分支 + 提交文件）
{
  "accountId": 1,
  "repo": "owner/repo",
  "title": "修复 xxx",
  "base": "main",          // 可选，默认取仓库默认分支
  "fork": true,            // true=自动 fork 后提 PR；false=同仓库分支（需写权限）
  "branch": "fix/xxx",     // 与 filePath/fileContent 一起用时必填
  "filePath": "README.md",
  "fileContent": "新内容",
  "draft": false
}
```

MCP 工具 `star_repo` / `follow_user` / `create_pr` 与这三个端点一一对应。

### MCP Server（给 AI 助手调用）

exe 内置 MCP 服务，可让 Claude / Cursor 直接操作运维中心：

```bat
GitHub仪表盘.exe --mcp
```

把它配置成 MCP 客户端的 stdio 服务即可。支持的工具：`list_accounts`、`get_todos`、`get_summary`、`get_account_detail`、`sync_account`、`sync_all`、`batch_tag`、`get_audit`、`mcp_status`，以及写操作 `star_repo`（参数 `accountId` / `owner` / `repo`）、`follow_user`（`accountId` / `username`）、`create_pr`（`accountId` / `repo` / `title`，可选 `base` / `head` / `body` / `draft` / `fork` / `branch` / `filePath` / `fileContent`）。写操作同样先做配额预检并记审计。

MCP 调用有**令牌桶限流**：`sync_all` / `sync_account` / `create_pr` 属于重量工具，用更小更慢的桶（默认 2 次容量、每 10 秒补充 1 个）；其余工具共用普通桶（默认 8 次容量、每 4 秒补充 1 个）。触发限流时工具返回 `isError`，文本里给出建议等待秒数与桶状态；`mcp_status` 可随时查看配置与剩余令牌。
| GET | `/api/tasks` | 同步引擎进度与配置 |
| POST | `/api/sync/settings` | 配置并发 / 间隔 / 退避 / 错峰 / 冷却 / 自适应 |
| POST | `/api/sync/run` | 立即触发全量同步 |
| GET | `/api/audit` | 审计日志 |
| GET | `/api/email/settings` | 邮箱面板配置 + 已关联邮箱的账号 |
| POST | `/api/email/settings` | 保存邮箱面板配置 |
| GET | `/api/email/messages` | 本地邮件列表（`accountId` / `q` 筛选） |
| POST | `/api/email/import` | 导入邮件 JSON（重复 message_id 跳过） |
| POST | `/api/email/inboxes/parse` | 解析邮笺导出的邮箱清单并匹配账号（预览） |
| POST | `/api/email/inboxes/import` | 把邮箱清单按账号回填（只更新关联邮箱） |
| POST | `/api/email/refresh` | 按配置拉取邮件（需先开启并配置取件链接） |
| DELETE | `/api/email/messages` | 删除邮件（`ids` 或 `accountId`） |

## 邮箱面板

侧栏新增「邮箱」页，用来查看与账号关联的邮箱收到的通知 / 安全邮件 / 登录验证码。**只做展示**：验证码会高亮并提供「复制」按钮，不会自动填到任何外部网站。

- 账号可在「添加账号 / 关联邮箱」填写**邮箱地址或完整取件链接**，列表与详情会展示，邮箱页可按账号筛选。
- 邮件缓存在本地 SQLite（`email_messages` 表），重复 `message_id` 自动跳过；支持手动「导入 JSON」与「刷新邮件」。
- **导入邮箱清单**：邮箱页点「导入邮箱」，粘贴或上传「邮笺」导出的文本（每行 `邮箱 链接`，或邮箱与链接各占一行；也支持 JSON），系统解析后预览并**按邮箱地址 / 邮箱名 / 用户名匹配现有账号**，勾选确认后只回填账号的「关联邮箱」字段（不动 Token 等）。
- 验证码提取：默认在「验证码 / verification / code」等关键词附近找 4-8 位数字或含数字的 6-8 位混合码，可用 `email.code_pattern` 自定义正则。

已与「邮笺」取件接口打通（`GET https://inbox.123456.world/i/{token}/json`，无需额外请求头）：

- 账号「关联邮箱」填 **token**（推荐，如 `kRDIX23C21-gAc8HqopiuZrkY-diBDzrNqTClRypYvw`）或**完整取件链接**；填 token 时按 `email.api_base` 模板拼成 `/i/{token}/json`。
- 响应结构：`{ok, email, verificationCode, latestMessage, messages[], total, page, pageSize, hasMore, expiresAt}`；顶层 `verificationCode` 会作为「当前验证码」展示，列表页顶部与高亮卡片都会显示。
- 自动翻页：按 `hasMore` 依次请求 `?page=2...`，最多 `email.max_pages` 页（默认 5）。
- 邮件字段按 `id/from/subject/body/date` 解析（兼容 `sender/title/text/html/created_at` 等别名），重复 `message_id` 自动跳过。

配置（`config/settings.yaml` 的 `email` 段）：

```yaml
email:
  enabled: true
  site_url: https://inbox.123456.world
  api_base: https://inbox.123456.world/i/{email}/json   # {email} 会被 URL 编码替换
  token: ""                     # 预留：如接口后续需要 Bearer 令牌
  fetch_interval_seconds: 300
  max_pages: 5
  code_pattern: ""              # 自定义验证码正则，留空用内置规则
  highlight_context: true
```

## 反风控策略

为降低被 GitHub 风控的概率，同步调度与 MCP 调用都做了限速设计，**原有 ETag 增量同步与失败指数退避保持不变**。

**同步调度**（`config/settings.yaml` 的 `sync` 段，也可通过 `POST /api/sync/settings` 调整）：

```yaml
sync:
  interval_seconds: 900              # 基础间隔
  concurrency: 5
  quota_floor: 100                   # 单账号剩余低于该值时本轮跳过
  min_sync_interval_seconds: 60      # 单账号冷却：该秒内不重复拉取
  stagger_seconds: 15                # 错峰：同轮内每个账号随机延迟 0~15s 再发请求
  max_interval_seconds: 3600         # 自适应间隔上限
  adaptive_interval: true            # 跳过占比 ≥ quota_low_ratio 时下一轮间隔翻倍
  quota_low_ratio: 0.5
```

**MCP 限流**（`config/settings.yaml` 的 `mcp` 段，改完自动热加载，无需重启 exe）：

```yaml
mcp:
  enabled: true
  bucket_capacity: 8                 # 普通工具桶容量
  refill_per_second: 0.25            # 普通工具桶补充速率（个/秒）
  heavy_tools: [sync_all, sync_account, create_pr]
  heavy_capacity: 2
  heavy_refill_per_second: 0.1
```

**请求行为**：Python 与 Go 双端使用统一的 `User-Agent: github-multi-account/<版本>`；列表类端点（`review_prs` / `my_prs` / `issues` / `events`）一律带 `If-None-Match` 条件请求，304 不计配额；同一账号在冷却期内不会被重复拉取（MCP 的 `sync_account` / `sync_all` 同样受冷却约束）。

## 技术栈

- 后端：FastAPI + httpx（汇总 / 详情接口分离；多账号并发限流；后台同步引擎）
- 存储：SQLite（`sync_state` 同步状态、`rate_limits` 配额、`audit_log` 审计、`account_meta` 元数据、`summaries` 汇总缓存、`snapshots` 历史快照）+ Fernet 加密（Token 落盘）
- 前端：原生 HTML/CSS/JS，GitHub 原生浅色 / 深色主题，四个一级页面 + 二级账号详情
- 测试：pytest（账号分页/搜索/筛选/排序、聚合、失败隔离、Token 不外泄、加密、迁移、API、CSRF）

## 开发

```bash
pip install -r requirements-dev.txt
python -m pytest          # 运行测试套件
```

代码结构：

| 文件 | 职责 |
| --- | --- |
| `app/main.py` | FastAPI 路由、启动生命周期 |
| `app/service.py` | 汇总 / 详情聚合、容错、并发限流 |
| `app/github.py` | GitHub REST 客户端（并发分页） |
| `app/config.py` | 账号配置读写、加密迁移、导入导出、内存缓存 |
| `app/crypto.py` | Fernet 加解密与本机密钥管理 |
| `app/store.py` | SQLite 快照存储、节流、清理 |
| `app/db.py` | 同步状态 / 配额 / 审计 / 元数据 / 汇总缓存 |
| `app/sync.py` | 后台同步引擎（并发限制、失败隔离、指数退避、错峰、自适应间隔、单账号冷却） |
| `app/email.py` | 邮箱面板（配置、取件 URL 构造、验证码提取、邮件解析与拉取预留） |
| `app/dashboard.py` | 全局聚合（总览 / 统一待办 / 最近活动） |
| `app/query.py` | 账号列表服务端查询（分页 / 搜索 / 筛选 / 排序） |
| `app/notify.py` | 外部通知渠道（Bark / Server酱 / 企业微信 / Webhook） |
| `app/docx.py` | 从 .docx 识别 GitHub Token 与用户名（解压 + XML 解析） |
| `static/app.js` | 前端全部逻辑 |
| `find_port.py` | Windows 端口保留段探测 |
| `Start-Hidden.vbs` | 无控制台窗口启动（双击运行） |
| `stop.bat` | 停止后台服务 |
| `.github/workflows/build.yml` | 自动构建 exe（push 时触发，产物可直接下载） |

## 常见问题

**动态为空？** `received_events` 只返回公开可见事件，且未关注/未参与的仓库动态不会出现。这是 GitHub API 的固有行为。

**触发速率限制？** 每个账号独立配额（认证后 5000 次/小时，搜索 30 次/分钟）。侧栏汇总每个账号 4 个请求（1 用户 + 3 搜索）；只有打开某个账号详情时才额外拉仓库 / 粉丝 / 动态。多账号同时刷新时留意搜索配额。

**端口冲突？** 换一个端口即可，例如 `uvicorn app.main:app --port 8080`，或运行 `start.bat 8080` 指定端口。

**Windows 报 `WinError 10013`（访问套接字的权限不允许）？** 这不是端口被占用（那是 10048），而是 Hyper-V / WSL / Docker 预留了 TCP 端口段。用 `start.bat` 会自动跳过被保留的端口；也可用 `netsh interface ipv4 show excludedportrange protocol=tcp` 查看保留范围，或以管理员身份执行 `netsh int ipv4 add excludedportrange protocol=tcp startport=8000 numberofports=1` 释放 8000。

**备份文件安全吗？** 页面导出分两种：**账号信息备份默认不含凭据**（只含用户名 / 别名 / 标签等）；如需备份 Token，用「导出凭据（加密）」，会要求设置密码（PBKDF2 + AES-GCM）。日常使用时 Token 是加密存储的。

**几百个账号会不会打爆 GitHub？** 不会。所有数据由后台同步引擎定时写入本地缓存（`data/dashboard.db` 的 `summaries` 表），页面只读缓存。默认 15 分钟同步一次、并发 5，并带错峰（同轮随机延迟）、按配额自适应拉长间隔、单账号冷却，详见「反风控策略」。

**历史数据存在哪？** `data/dashboard.db`（SQLite），已在 `.gitignore` 中。启动时会自动清理：超过 90 天或每个账号超过 1000 条的旧快照会被删除。删除该文件会清空所有历史趋势，但不影响账号配置。

**关于安全边界**：应用默认只监听 `127.0.0.1`，仅本机可访问。**不要**用 `--host 0.0.0.0` 把它暴露到局域网或公网——备份导出接口会返回明文 Token，且应用本身没有登录认证。

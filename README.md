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

- **后台同步引擎**：定时同步所有账号（默认 15 分钟、并发 3），失败隔离 + 指数退避重试，结果写入本地缓存
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
- **主题切换** —— 浅色 / 深色一键切换，默认跟随系统偏好，自动记忆
- **键盘快捷键** —— 刷新、切标签页、账号导航等全流程键盘操作

后端代理所有 GitHub API 请求，**Token 不暴露给浏览器**，只以脱敏形式展示。

## 快速开始

**单文件 exe 版（推荐分发给别人）**：

> ⚠️ exe 目前仍是**单账号详情版**（Go 后端尚未同步运维中心的新接口），可独立分发使用；Python 版已包含全部新功能。

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
| GET | `/api/accounts` | 账号列表（脱敏） |
| POST | `/api/accounts` | 新增/更新账号 |
| DELETE | `/api/accounts/{id}` | 删除账号 |
| POST | `/api/verify` | 测试 Token 有效性（不保存） |
| GET | `/api/status` | 全部账号汇总（轻量，侧栏用） |
| GET | `/api/status/{id}` | 单个账号完整详情 |
| GET | `/api/accounts/export` | 导出账号备份（含明文 Token） |
| POST | `/api/accounts/import` | 从备份导入账号 |
| GET | `/api/history/{id}` | 账号历史快照（时间升序） |
| GET | `/api/snapshot/{id}` | 立即记录一条快照 |
| GET | `/api/dashboard/summary` | 全局账号总览 |
| GET | `/api/dashboard/tasks` | 统一待办（按优先级排序） |
| GET | `/api/dashboard/activities` | 跨账号最近动态 |
| GET | `/api/accounts` | 账号列表，支持 `page/pageSize/query/status/type/group/tags/sort/order` |
| POST | `/api/accounts/batch-refresh` | 批量刷新 |
| POST | `/api/accounts/{id}/sync` | 立即同步单个账号 |
| GET | `/api/tasks` | 同步引擎进度与配置 |
| POST | `/api/sync/settings` | 配置并发 / 间隔 / 退避 |
| POST | `/api/sync/run` | 立即触发全量同步 |
| GET | `/api/audit` | 审计日志 |

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
| `app/sync.py` | 后台同步引擎（并发限制、失败隔离、指数退避） |
| `app/dashboard.py` | 全局聚合（总览 / 统一待办 / 最近活动） |
| `app/query.py` | 账号列表服务端查询（分页 / 搜索 / 筛选 / 排序） |
| `app/notify.py` | 外部通知渠道（Bark / Server酱 / 企业微信 / Webhook） |
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

**几百个账号会不会打爆 GitHub？** 不会。所有数据由后台同步引擎定时写入本地缓存（`data/dashboard.db` 的 `summaries` 表），页面只读缓存。默认 15 分钟同步一次、并发 3，可在「设置」里调整。

**历史数据存在哪？** `data/dashboard.db`（SQLite），已在 `.gitignore` 中。启动时会自动清理：超过 90 天或每个账号超过 1000 条的旧快照会被删除。删除该文件会清空所有历史趋势，但不影响账号配置。

**关于安全边界**：应用默认只监听 `127.0.0.1`，仅本机可访问。**不要**用 `--host 0.0.0.0` 把它暴露到局域网或公网——备份导出接口会返回明文 Token，且应用本身没有登录认证。

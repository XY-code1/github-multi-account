# GitHub 多账号状态仪表盘

一个本地 Web 仪表盘，用多个 GitHub PAT Token 管理多个账号，一屏查看每个账号的：

- **登录有效性** —— Token 是否有效、是否过期、速率限制剩余
- **数据概览** —— 粉丝 / 关注 / 总 Star 统计，仓库 Star 排行与分布可视化
- **历史趋势** —— 自动记录状态快照（10 分钟节流），折线图看粉丝 / Star / PR 变化，支持导出 CSV
- **备份恢复** —— 一键导出 / 导入账号备份，Token 加密存储
- **变化提醒** —— 可选开启桌面通知，PR / Issue 增加时提醒
- **账号搜索** —— 侧栏按显示名 / 用户名实时筛选账号，账号多时快速定位
- **待处理 PR** —— 等待你 Review 的开放 PR
- **我的 PR** —— 你发起的开放 PR 状态
- **分配 Issue** —— 分配给你的开放 Issue
- **仓库动态** —— received events（被 @、仓库被 Star、关注的更新等）
- **仓库列表** —— 最近更新的公开仓库
- **账号搜索** —— 侧栏按显示名 / 用户名实时筛选账号，账号多时快速定位

后端代理所有 GitHub API 请求，**Token 不暴露给浏览器**，只以脱敏形式展示。

## 快速开始

**一键启动（推荐）**：

- Windows：双击 `start.bat`
- macOS / Linux：终端执行 `./start.sh`

脚本会自动创建 `.venv`、安装依赖、复制配置模板，并**自动探测可用端口**（Windows 上 Hyper-V / WSL / Docker 可能保留部分端口，直接写死 8000 会报 `WinError 10013`）。启动后按提示打开对应的 `http://localhost:<端口>`。

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
| POST | `/api/snapshot/{id}` | 立即记录一条快照 |

## 技术栈

- 后端：FastAPI + httpx（异步并发；汇总 / 详情接口分离；多账号并发限流）
- 存储：SQLite（历史快照，保留 90 天 / 每账号 1000 条）+ Fernet 加密（Token 落盘）
- 前端：原生 HTML/CSS/JS，GitHub 暗色主题，刷新间隔可配置（30 秒 ~ 10 分钟）
- 测试：pytest（34 项，覆盖加密 / 迁移 / 导入导出 / 快照 / 容错 / API / CSRF）

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
| `static/app.js` | 前端全部逻辑 |
| `find_port.py` | Windows 端口保留段探测 |

## 常见问题

**动态为空？** `received_events` 只返回公开可见事件，且未关注/未参与的仓库动态不会出现。这是 GitHub API 的固有行为。

**触发速率限制？** 每个账号独立配额（认证后 5000 次/小时，搜索 30 次/分钟）。侧栏汇总每个账号 4 个请求（1 用户 + 3 搜索）；只有打开某个账号详情时才额外拉仓库 / 粉丝 / 动态。多账号同时刷新时留意搜索配额。

**端口冲突？** 换一个端口即可，例如 `uvicorn app.main:app --port 8080`，或运行 `start.bat 8080` 指定端口。

**Windows 报 `WinError 10013`（访问套接字的权限不允许）？** 这不是端口被占用（那是 10048），而是 Hyper-V / WSL / Docker 预留了 TCP 端口段。用 `start.bat` 会自动跳过被保留的端口；也可用 `netsh interface ipv4 show excludedportrange protocol=tcp` 查看保留范围，或以管理员身份执行 `netsh int ipv4 add excludedportrange protocol=tcp startport=8000 numberofports=1` 释放 8000。

**备份文件安全吗？** 页面导出的备份包含 **Token 明文**，请像密码一样保管（建议放在加密盘 / 密码管理器里）。日常使用时 Token 是加密存储的，明文只在导出备份时出现。

**历史数据存在哪？** `data/dashboard.db`（SQLite），已在 `.gitignore` 中。启动时会自动清理：超过 90 天或每个账号超过 1000 条的旧快照会被删除。删除该文件会清空所有历史趋势，但不影响账号配置。

**关于安全边界**：应用默认只监听 `127.0.0.1`，仅本机可访问。**不要**用 `--host 0.0.0.0` 把它暴露到局域网或公网——备份导出接口会返回明文 Token，且应用本身没有登录认证。

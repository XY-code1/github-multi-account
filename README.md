# GitHub 多账号状态仪表盘

一个本地 Web 仪表盘，用多个 GitHub PAT Token 管理多个账号，一屏查看每个账号的：

- **登录有效性** —— Token 是否有效、是否过期、速率限制剩余
- **待处理 PR** —— 等待你 Review 的开放 PR
- **我的 PR** —— 你发起的开放 PR 状态
- **分配 Issue** —— 分配给你的开放 Issue
- **仓库动态** —— received events（被 @、仓库被 Star、关注的更新等）
- **仓库列表** —— 最近更新的公开仓库

后端代理所有 GitHub API 请求，**Token 不暴露给浏览器**，只以脱敏形式展示。

## 快速开始

```bash
# 1. 安装依赖（建议虚拟环境）
pip install -r requirements.txt

# 2. 配置账号
cp config/accounts.example.yaml config/accounts.yaml
# 编辑 accounts.yaml，填入你的 GitHub 用户名和 Token

# 3. 启动
uvicorn app.main:app --reload --port 8000
```

浏览器打开 <http://localhost:8000>。

也可以在页面上点「添加账号」直接录入（支持「测试连接」，不会保存无效 Token）。

## Token 权限

在 GitHub → Settings → Developer settings → Personal access tokens 创建。

**Fine-grained token**（推荐）：至少勾选 Public Repository 的 `Pull requests`、`Issues`、`Contents`、`Metadata`（只读）。

**Classic token**：`repo:status`、`read:user`、`read:project` 即可；公开仓库用 `public_repo` 也够。

> ⚠️ `config/accounts.yaml` 已加入 `.gitignore`，请勿提交。Token 等同于账号凭证，泄露后请立即在 GitHub 上撤销。

## 配置格式

```yaml
accounts:
  - label: 主账号            # 仪表盘显示名称
    username: XY-code1      # 仅用于展示，实际登录以 token 为准
    token: ghp_xxxx
  - label: 小号
    username: yuzhoulab
    token: ghp_xxxx
```

可通过环境变量 `GAD_CONFIG_DIR` 指定配置目录（默认 `config/`）。

## API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/accounts` | 账号列表（脱敏） |
| POST | `/api/accounts` | 新增/更新账号 |
| DELETE | `/api/accounts/{id}` | 删除账号 |
| POST | `/api/verify` | 测试 Token 有效性（不保存） |
| GET | `/api/status` | 全部账号状态摘要 |
| GET | `/api/status/{id}` | 单个账号完整状态 |

## 技术栈

- 后端：FastAPI + httpx（异步并发拉取）
- 前端：原生 HTML/CSS/JS，GitHub 暗色主题，每 2 分钟自动刷新

## 常见问题

**动态为空？** `received_events` 只返回公开可见事件，且未关注/未参与的仓库动态不会出现。这是 GitHub API 的固有行为。

**触发速率限制？** 每个账号独立配额（认证后 5000 次/小时，搜索 30 次/分钟）。仪表盘对每个账号会发起 1 次用户 + 3 次搜索 + 3 次其他请求，多账号同时刷新时留意搜索配额。

**端口冲突？** `--port` 换一个即可，例如 `--port 8080`。

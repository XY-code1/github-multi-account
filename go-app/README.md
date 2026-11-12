# GitHub 多账号状态仪表盘 · Go 单文件版

> ⚠️ **版本说明**：Python 版已升级为「多账号运维中心」（后台同步引擎、全局聚合、服务端分页、四个一级页面）。Go 版目前仍是单账号详情仪表盘，后端尚未同步这些新接口，因此**保持现有的旧前端**，exe 可正常独立运行。如需在 exe 上使用新功能，需要把 `/api/dashboard/*`、`/api/accounts`（分页）、`/api/tasks`、`/api/audit` 等接口移植到 Go。

用 Go 重写的后端 + 内嵌前端，**打包成单个 `.exe`**：无需 Python、无需安装依赖，双击即用。

## 使用

1. 把 `GitHub仪表盘.exe` 放到**项目根目录**（和 `config\` 文件夹同级），这样它和 Python 版共用同一份配置
2. **双击 exe** —— 自动选端口、启动服务、打开浏览器，**没有黑色控制台窗口**
3. 首次使用在页面右上角「添加账号」录入 Token，或直接编辑 `config/accounts.yaml`

> **关于端口**：程序优先沿用上次成功的端口（记录在 `data/server.port`）；若固定候选端口被占用、或被系统保留（Windows 上 Hyper-V / WSL / Docker 常预留端口段），会自动让操作系统分配一个空闲端口，因此不会再出现「access permissions」启动失败。

### 从 Python 版迁移

exe 放在项目根目录时，直接读取项目 `config\` 里的 `accounts.yaml` 和 `.secret.key`，**Token 不用重新录入**。放在别的目录时，把这两个文件复制到 exe 同目录的 `config\` 里。

```
config\accounts.yaml      （含加密后的 Token）
config\.secret.key        （本机密钥，必须一起复制）
```

> ⚠️ 没有 `.secret.key` 就无法解密已有 Token，程序会明确报错。两个文件要一起备份。

### 停止服务

程序在后台运行，停止方式：

- 任务管理器 → 找到 `GitHub仪表盘.exe` → 结束任务
- 或运行 `stop.bat`（如果 exe 放在项目根目录）

## 关于 SmartScreen 与代码签名

未签名的 exe 首次运行会被 Windows SmartScreen 拦截，弹出蓝色「Windows 已保护你的电脑」，需要点「更多信息 → 仍要运行」。这是 Windows 的默认行为，**不是病毒**。

如果想彻底消除这个提示，需要给 exe 做**代码签名**：

1. 向 CA 机构购买 **OV 代码签名证书**（如 DigiCert、Sectigo，约 $100–300/年；EV 证书更贵但立即生效，OV 需要积累信誉）
2. 用证书签名：
   ```bat
   build-signed.bat <证书指纹>
   ```
   脚本会自动构建并签名，输出 `GitHub仪表盘.exe`

> 没有证书时可继续使用未签名版本，只需在 SmartScreen 提示时选择「仍要运行」。

## 特性

- 单文件 exe，无控制台窗口，自动打开浏览器
- 端口智能选择：沿用上次端口 → 固定候选 → 系统分配（绕过 Hyper-V/WSL 保留段）
- 单实例检测：重复双击只打开浏览器，不启动第二个服务
- Token 加密存储（Fernet，与 Python 版兼容）
- 备份导出支持可选密码加密（PBKDF2 + AES-GCM，与 Python 版互通）
- 历史快照 + 趋势图 + CSV 导出
- 账号对比、账号分组、全局搜索（Ctrl+K）、列表过滤
- 主题切换、键盘快捷键、变化桌面提醒
- 版本号显示 + 更新检查

## 外部通知

即使**关闭浏览器**，也能在 PR / Issue 增加时收到推送。顶栏「通知」里配置渠道，可点「发送测试通知」验证。

支持四种渠道（`config/settings.yaml`，配置后自动生效）：

| type | 说明 | url 示例 |
| --- | --- | --- |
| `bark` | iOS Bark | `https://api.day.app/<你的key>` |
| `webhook` | 通用 Webhook（POST JSON `{title, body, url}`） | `https://example.com/hook` |
| `serverchan` | Server酱 | `https://sctapi.ftqq.com/<SENDKEY>.send` |
| `wecom` | 企业微信机器人 | `https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=<key>` |

程序后台每 5 分钟检查一次，只在数量**增加**时推送。配置文件已 gitignore。

## 从源码构建

需要 [Go 1.22+](https://go.dev/dl/)。

```bat
build.bat
```

或手动：

```bash
# Windows exe（无控制台窗口）
GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui -s -w" -o "GitHub仪表盘.exe" .

# 本机测试
go build -o github-dashboard .
GAD_BASE_DIR=/path/to/test ./github-dashboard
```

## 与 Python 版的差异

| 项目 | Python 版 | Go 版 |
| --- | --- | --- |
| 运行依赖 | Python 3 + pip 依赖 | 无（单 exe） |
| 配置文件 | `config/accounts.yaml` | 同左（兼容） |
| 加密 | Fernet | Fernet（兼容，双向验证过） |
| 历史数据 | `data/dashboard.db` (SQLite) | 同左（兼容） |
| 端口 | 自动探测 | 自动探测（同一组候选端口） |

## 目录结构

```
go-app/
├── main.go                 启动流程：环境准备、端口探测、开浏览器
├── assets/                 内嵌前端（由 build.bat 从上层 static/ 与 templates/ 复制）
├── internal/
│   ├── config/             账号配置、Fernet 加解密、导入导出、缓存
│   ├── github/             GitHub REST API 客户端
│   ├── service/            汇总 / 详情聚合、容错、并发限流
│   ├── store/              SQLite 历史快照
│   └── server/             HTTP 路由、CSRF 防护、静态资源
├── build.bat               Windows 构建脚本
└── README.md
```

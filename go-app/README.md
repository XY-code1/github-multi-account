# GitHub 多账号状态仪表盘 · Go 单文件版

用 Go 重写的后端 + 内嵌前端，**打包成单个 `.exe`**：无需 Python、无需安装依赖，双击即用。

## 使用

1. 把 `GitHub仪表盘.exe` 放到一个文件夹里（首次运行会自动创建 `config/` 和 `data/`）
2. **双击 exe** —— 自动选端口、启动服务、打开浏览器，**没有黑色控制台窗口**
3. 首次使用在页面右上角「添加账号」录入 Token，或直接编辑 `config/accounts.yaml`

### 从 Python 版迁移

把 Python 版目录下的这两个文件复制到 exe 所在目录的 `config\` 里即可，**Token 不用重新录入**（Fernet 加密完全兼容）：

```
config\accounts.yaml      （含加密后的 Token）
config\.secret.key        （本机密钥，必须一起复制）
```

> ⚠️ 没有 `.secret.key` 就无法解密已有 Token，程序会明确报错。两个文件要一起备份。

### 停止服务

程序在后台运行，停止方式：

- 任务管理器 → 找到 `GitHub仪表盘.exe` → 结束任务
- 或运行 `stop.bat`（如果 exe 放在项目根目录）

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

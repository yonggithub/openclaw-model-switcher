# openclaw-model-switcher

> **语言 / Language:** [English](README.md) | [中文](README.zh-CN.md)

面向 [OpenClaw](https://github.com/anthropics/openclaw) Gateway 的 Web 端模型切换与管理面板。

在同一页面管理多家 LLM 提供商、选择主模型与回退链、连通性测试、配置 diff 预览，并热更新写入 `openclaw.json`。

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Vue](https://img.shields.io/badge/Vue-3.4-4FC08D?logo=vuedotjs&logoColor=white)
![SQLite](https://img.shields.io/badge/SQLite-003B57?logo=sqlite&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-2496ED?logo=docker&logoColor=white)
![License](https://img.shields.io/badge/License-MIT-yellow.svg)

## 界面截图

| 总览 | 提供商与模型 |
|:----:|:------------:|
| ![主界面总览](img/v1.png) | ![提供商与模型](img/v2.png) |

| 配置与路由 | 网关与工具 |
|:----------:|:----------:|
| ![配置侧栏](img/v3.png) | ![网关及相关界面](img/v4.png) |

## 功能特性

- **提供商管理** — 增删改 LLM 提供商（OpenAI 兼容 / Anthropic），内置 DeepSeek、NVIDIA、SiliconFlow 等预设。
- **模型发现** — 一键从提供商 API（`/v1/models`）拉取可用模型。
- **模型选择** — 单选/批量勾选/反选，或仅保留连通性测试通过的模型。
- **连通性测试** — 单模型或按提供商批量并发测试，实时进度。
- **主模型与回退** — 配置主模型及有序回退链。
- **多 Agent** — 为不同 Agent 指定模型，或跟随主模型。
- **配置 Diff 预览** — 应用前逐行预览对 `openclaw.json` 的变更（Myers 算法）。
- **自动备份** — 每次应用生成带时间戳的备份（`openclaw.json.bak.YYYYMMDDHHMMSS`）。
- **热重载策略** — 网关重载行为：`hybrid` / `hot` / `restart` / `off`，可设防抖延迟。
- **网关管理** — 在面板中查看网关状态并重启。
- **未保存提示** — 有待应用变更时 Apply 按钮高亮提示。
- **界面语言** — 中文 / English 切换，偏好保存在 localStorage。

## 快速开始

### 环境要求

- Go 1.25+

### 编译与运行

```bash
# 克隆
git clone https://github.com/yonggithub/openclaw-model-switcher.git
cd openclaw-model-switcher

# 编译
go build -o openclaw-model-switcher .

# 运行
./openclaw-model-switcher
```

浏览器访问 **http://localhost:8356**。

### 构建脚本

```bash
# Linux (amd64)
./script/build-linux.sh

# Windows (amd64)
./script/build-windows.sh

```

### Docker 部署

#### 前提

- 已安装 Docker 与 Docker Compose
- 已生成 Linux 二进制：`build/OpenClawSwitch-linux`（先执行 `./script/build-linux.sh`）

#### 使用 Docker Compose 启动

```bash
# 1. 构建 Linux 二进制
./script/build-linux.sh

# 2. 启动容器
docker compose up -d
```

浏览器访问 **http://localhost:8356**。

#### 数据卷

| 容器内路径 | 宿主机路径 | 说明 |
|-----------|-----------|------|
| `/data` | `./data` | SQLite 数据库（`openclawswitch.db`）等持久化数据 |
| `/root/.openclaw` | `/root/.openclaw` | OpenClaw Gateway 配置目录 |

启动后请在面板中将配置文件路径设为 `/root/.openclaw/openclaw.json`，以便读写宿主机上的 OpenClaw 配置。

#### 宿主机进程与网关重启

容器启用 `pid: "host"` 与 `privileged: true`，用于在宿主机上发现进程、通过 `nsenter` 在宿主机文件系统启动 Gateway，以及发送信号完成重启，从而支持面板中的 **网关状态** 与 **重启网关**。

> **安全提示**：`pid: host` 与 `privileged` 会赋予容器较强的宿主机访问能力，请在可信环境中使用。

#### 常用 Docker 命令

```bash
# 启动
docker compose up -d

# 查看日志
docker compose logs -f

# 停止
docker compose down

# 更新二进制后重建镜像
docker compose up -d --build
```

## 使用说明

1. **添加提供商** — 点击「添加」，填写名称 / Base URL / API Key，或使用预设。
2. **拉取模型** — 展开提供商卡片，点击「拉取模型」。
3. **选择模型** — 勾选要用的模型；可用「批量测试」验证连通性，再「仅保留通过」。
4. **路由配置** — 在侧栏选择主模型，并可配置回退链。
5. **应用配置** — 点击「应用配置」，查看 diff 并确认；会写入 `openclaw.json` 并自动备份。

### 进程管理脚本

```bash
./script/start.sh    # 后台启动（PID 写入 app.pid）
./script/stop.sh     # 按 PID 停止
./script/status.sh   # 查看运行状态
```

## 架构

```
┌─────────────────────────────────────┐
│           Browser (SPA)             │
│   Vue 3 + Tailwind CSS (CDN)       │
└──────────────┬──────────────────────┘
               │ HTTP/JSON
┌──────────────▼──────────────────────┐
│         Go HTTP Server (:8356)      │
│  ┌──────────┐  ┌──────────────────┐ │
│  │ Handlers  │  │ Config Engine    │ │
│  │ (REST API)│  │ (R/W openclaw    │ │
│  │           │  │  .json + diff)   │ │
│  └─────┬─────┘  └────────┬────────┘ │
│        │                 │          │
│  ┌─────▼─────────────────▼────────┐ │
│  │     SQLite (providers,         │ │
│  │     models, settings)          │ │
│  └────────────────────────────────┘ │
└─────────────────────────────────────┘
```

## API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/` | 返回仪表盘 SPA |
| `GET` | `/api/providers` | 列出提供商 |
| `POST` | `/api/providers` | 创建提供商 |
| `PUT` | `/api/providers/{id}` | 更新提供商 |
| `DELETE` | `/api/providers/{id}` | 删除提供商及其模型 |
| `POST` | `/api/providers/{id}/fetch` | 从提供商 API 拉取模型 |
| `GET` | `/api/models` | 列出所有模型 |
| `PUT` | `/api/models/{id}/toggle` | 切换模型选中状态 |
| `POST` | `/api/models/batch-select` | 批量选中/取消 |
| `POST` | `/api/models/test` | 单模型连通性测试 |
| `GET` | `/api/agents` | 从配置读取 Agent 列表 |
| `GET` | `/api/config` | 读取当前 openclaw.json |
| `GET` | `/api/config/path` | 配置文件路径与状态 |
| `POST` | `/api/config/path` | 设置配置文件路径 |
| `POST` | `/api/config/preview` | 预览配置变更（diff） |
| `POST` | `/api/config/apply` | 应用配置到文件 |
| `GET` | `/api/config/reload` | 读取重载相关设置 |
| `GET` | `/api/gateway/status` | 网关进程状态 |
| `POST` | `/api/gateway/restart` | 重启网关进程 |

## 技术栈

| 层次 | 技术 |
|------|------|
| 后端 | Go（标准库 `net/http`） |
| 前端 | Vue 3.4（CDN）+ Tailwind CSS 2.x |
| 数据库 | SQLite（[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)，纯 Go，无 CGO） |
| 嵌入资源 | `//go:embed` 单二进制分发 |

## 项目结构

```
.
├── main.go              # 入口与路由注册
├── handlers.go          # HTTP 处理
├── config.go            # 配置读写与 diff
├── provider.go          # 提供商 API 与模型测试
├── db.go                # SQLite 初始化与表结构
├── go.mod / go.sum      # Go 依赖
├── README.md            # 英文说明（GitHub 默认展示）
├── README.zh-CN.md      # 中文说明
├── Dockerfile           # 镜像定义（Alpine）
├── docker-compose.yml   # Compose 编排
├── img/                 # README 截图
├── templates/
│   └── index.html       # 单页前端（构建时嵌入）
└── script/
    ├── build-linux.sh   # Linux 构建
    ├── start.sh         # 后台启动
    ├── stop.sh          # 停止
    └── status.sh        # 状态检查
```

## 配置说明

面板管理 OpenClaw Gateway 的 `openclaw.json`，主要结构示例：

```jsonc
{
  "models": {
    "providers": { /* baseUrl、apiKey、models 等 */ }
  },
  "agents": {
    "defaults": {
      "model": {
        "primary": "provider/model-id",
        "fallbacks": ["provider/model-id-2"]
      }
    },
    "list": [ /* Agent 定义 */ ]
  },
  "gateway": {
    "reload": { "mode": "hybrid", "debounceMs": 300 }
  }
}
```

## 许可证

本项目采用 [MIT License](LICENSE)。

## 参与贡献

欢迎 Issue 与 Pull Request。

1. Fork 本仓库
2. 新建功能分支（`git checkout -b feature/amazing-feature`）
3. 提交更改（`git commit -m 'Add amazing feature'`）
4. 推送分支（`git push origin feature/amazing-feature`）
5. 发起 Pull Request

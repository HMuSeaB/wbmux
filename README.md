<div align="center">

```text
██     ██ ██████  ███    ███ ██    ██ ██   ██
██     ██ ██   ██ ████  ████ ██    ██  ██ ██ 
██  █  ██ ██████  ██ ████ ██ ██    ██   ███  
██ ███ ██ ██   ██ ██  ██  ██ ██    ██  ██ ██ 
 ███ ███  ██████  ██      ██  ██████  ██   ██
```

### **WorkBuddy 开发者工作区协同套件 · 本地环境迁移 · 会话归档 · 调试代理**

连接多端开发环境，支持历史会话跨 IDE 归档导出、环境配置平滑迁移与本地开发调试代理

<p align="center">
  <b>简体中文</b> • <a href="README_EN.md">English</a>
</p>

<p align="center">
  <a href="https://github.com/HMuSeaB/wbmux/releases/latest"><img src="https://img.shields.io/github/v/release/HMuSeaB/wbmux?color=10b981&label=Release&style=flat-square" alt="Release"></a>
  <a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go" alt="Go Version"></a>
  <a href="https://github.com/HMuSeaB/wbmux/releases"><img src="https://img.shields.io/badge/Platform-Windows%20%7C%20Linux%20%7C%20macOS-3b82f6?style=flat-square" alt="Platform"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-amber?style=flat-square" alt="License"></a>
  <a href="#-基准与指标"><img src="https://img.shields.io/badge/Single%20Binary-Zero%20Config-8b5cf6?style=flat-square" alt="Single Binary"></a>
</p>

<p align="center">
  <a href="#-设计背景与概览">设计背景</a> •
  <a href="#-系统架构拓扑">架构拓扑</a> •
  <a href="#-核心工程特性">核心特性</a> •
  <a href="#-快速上手">快速上手</a> •
  <a href="#-三方-ide-与外部工具接入">生态接入</a> •
  <a href="#-参与贡献与反馈">参与贡献</a>
</p>

</div>

---

## 📖 设计背景与概览

`wbmux` 是专为现代开发者打造的高性能本地多环境协同配置工具与工作区开发套件。

在使用 WorkBuddy（国内版 `CodeBuddy` 与国际版 `WorkBuddy AI`）及各类主流 AI 编程助手时，开发者常常面临跨端环境割裂、历史会话难以统一管理、配置重复拷贝等痛点：
- **多端环境隔离**：不同发行版在本地拥有独立的存储结构与会话库，两端历史无法互通；
- **会话难以归档**：Cursor、Qoder、ZCode 等工具中的宝贵排障排错与架构对话分散在各个 SQLite 库中，难以一键导出为 Markdown 项目交接件；
- **配置迁移成本高**：在两端不同安装之间手动搬运自定义模型、技能和记忆繁琐易错。

`wbmux` 采用纯 Go 语言编写，编译为单一无依赖可执行文件，为开发者提供开箱即用的工作区协同支持：
- **历史会话中心**：跨 IDE 自动归组解析历史对话，支持一键导出 Markdown 移交件；
- **环境迁移与体检**：非侵入式读取安装状态，一键同步双端技能、记忆与配置；
- **标准调试代理**：内置轻量 OpenAI 兼容开发代理，支持自定义模型接入与本地延迟度量；
- **配置备份与导出**：支持当前开发配置的安全备份与多设备一键迁移。

---

## 🏛️ 系统架构拓扑

```mermaid
graph LR
    subgraph Clients["开发者工作流 (Clients)"]
        CB["WorkBuddy 客户端<br/>(IDE 编程工作区)"]
        IDE["三方 IDE / 外部助手<br/>(Cursor / Continue / Cline)"]
    end

    subgraph wbmux["wbmux 本地守护进程 (:8000)"]
        direction TB
        GW["本地开发调试代理<br/>(OpenAI 兼容端点)"]
        CONF["环境配置与会话管理<br/>(迁移 / 归档 / 备份)"]
        ROUTER["多环境适配路由<br/>(自适应连接管理)"]
        AUDIT["内存环形审计流 (RingBuffer)<br/>(调用耗时度量 · 调试看板)"]
        
        GW --> ROUTER
        ROUTER --> CONF
        GW -.-> AUDIT
    end

    subgraph Upstream["目标环境与模型服务 (Upstream)"]
        ENV_A["主流环境端点"]
        ENV_B["扩展环境端点"]
        BYOK["自定义模型端点 (BYOK / OneAPI)"]
    end

    CB -->|"本地回路"| GW
    IDE -->|"标准协议"| GW
    ROUTER -->|"环境适配"| ENV_A
    ROUTER -->|"端点调度"| ENV_B
    ROUTER -->|"直连反代"| BYOK
```

---

## ✨ 核心工程特性

### 1. 📚 跨 IDE 会话中心与 Markdown 移交件导出
* **多源历史会话归组**：支持原地索引并结构化解析本地 Cursor、Qoder、ZCode 等多种 AI 编程工具的历史会话；
* **Markdown 一键导出**：支持将长对话、代码块与思路决策一键导出为标准 Markdown 文档，极速生成项目交接件与复盘记录。

### 2. 🔄 多安装环境配置平滑迁移与健康体检 (Doctor)
* **安全迁移机制**：支持在不同安装实例之间安全同步配置、自定义技能与记忆内容，绝不覆盖已有数据；
* **一键环境体检**：内置 `wbmux doctor`，秒级诊断当前客户端安装路径、配置完整性与连通性。

### 3. 🔌 本地开发调试代理与自定义模型接入 (BYOK)
* **标准 OpenAI 协议兼容**：提供本地回环补全端点，方便第三方桌面工具（如 Cherry Studio、NextChat、Cursor）统一接入；
* **自定义模型映射**：支持将自备 API Key（如 DeepSeek、Moonshot、Ollama 等端点）映射注入至客户端模型列表，统一交互入口。

### 4. 📊 实时请求调试看板与链路耗时度量
* **内存环形审计流**：采用定长 1000 条轻量环形缓冲区，原地复用，零垃圾回收开销；
* **度量指标透明**：毫秒级度量首字响应延迟 (TTFT)、总耗时与 Token 统计，提供现代可视化调试看板。

### 5. 🛡️ 配置文件安全导出与灾备备份
* **交互式导出管理**：Web 界面支持按账号单选或全选一键导出为官方格式 `.json` 备份文件；
* **跨设备平滑还原**：重装系统或切换开发设备时，一键载入配置，快速恢复工作区状态。

### 6. 🚀 极致单二进制交付 (Zero Dependency)
* **零运行时依赖**：无 Node.js、Python 或外部动态库依赖，双击即可运行；
* **全内嵌现代化 WebUI**：前端静态资源通过 Go `embed` 编译进单一可执行文件，极低资源占用。

---

## 🚀 快速上手

### 1. 下载与启动

在 [Releases](https://github.com/HMuSeaB/wbmux/releases) 页面下载对应系统的压缩包，解压后直接运行：

```bash
# 启动图形管理界面（Windows 下双击 wbmux.exe 效果相同）
wbmux gui

# 检查当前系统环境与配置完整性
wbmux doctor

# 查看本机探测到的客户端安装
wbmux list
```

### 2. 常用命令速查

```bash
# 启动图形界面（托盘常驻，退出点界面右下角或托盘菜单）
wbmux gui

# 执行系统与配置体检
wbmux doctor

# 浏览与归组跨 IDE 历史会话
wbmux sessions

# 迁移多环境配置与技能记忆（带 --yes 实际执行，默认只读预览）
wbmux migrate

# 在桌面创建快捷启动方式
wbmux shortcut
```

---

## 🛠️ 三方 IDE 与外部工具接入

由于内置调试代理完全兼容标准 OpenAI 协议规范，您可以直接将三方开发工具指向本地回路：

> **端口说明**：wbmux 的监听端口**不固定**（首次启动随机分配，之后复用已保存的值）。
> 以界面「本地 OpenAI 兼容代理接口」卡片里显示的 `Base URL` 为准，
> 或直接打开界面地址 `http://127.0.0.1:<端口>/?t=<令牌>` 获取。
> 下文统一写作 `http://127.0.0.1:<端口>`。

| 客户端 | 接口地址 (Base URL) | API Key | 推荐模型名 |
|---|---|---|---|
| **Cherry Studio** | `http://127.0.0.1:<端口>/v1` | 界面上的安全 Token | `deepseek-v4.1-flash` / `auto` |
| **NextChat** | `http://127.0.0.1:<端口>` | 界面上的安全 Token | `deepseek-v4.1-flash` |
| **Cursor / Continue** | `http://127.0.0.1:<端口>/v1` | 界面上的安全 Token | `auto` |

### 🌐 全网实时联网搜索开发代理 (Web Search Proxy)

`wbmux` 内置了官方标准全网实时联网搜索反代端点，无需额外申请第三方搜索引擎 Key，直接借用客户端已登录的会话额度，为现代 AI 工具链提供零成本的联网检索能力：

| 端点类型 | 接口路径 | 支持协议 | 适用客户端 / 场景 |
|---|---|---|---|
| **Agent 工具调用端点** | `POST /v1/search/messages` | Anthropic Messages 协议（兼容 `web_search` 工具协议） | **ZCode**、**Claude Code**、**Cline** 等支持工具调用的 AI 编程助手 |
| **极简 JSON 搜索端点** | `GET /v1/search?q=<关键词>&n=<条数>` | 原生结构化 JSON | Python / Node.js 脚本、Raycast、Alfred 插件及自定义扩展 |

**认证说明**：

- 凭据放在 `x-api-key: <Token>` 或 `Authorization: Bearer <Token>` 均可，两者都认；
- 来自本机回环地址（`127.0.0.1` / `::1`）的请求**完全免密**，连认证头都不必带
  ——所以对接不支持填 Key 的客户端时，直接留空即可；
- 非回环来源（局域网访问）必须携带正确 Token，否则 403。

**为什么搜索不受"限时免费"影响**：

搜索走的是上游 `/agenttool/v1/search`，与对话的 `/v2/chat/completions` 是
**两条独立链路**，只借用账号的登录凭据（Token + UID），不经过任何模型。
所以：

- 与 `deepseek-v4.1-flash` 是否在限免期**无关**，也不消耗该模型的额度；
- 与 `GPT-6.x / Pro Only` 的档位限制也无关；
- 账号积分耗尽时，搜索通常仍可用（它不计入对话 credits）。

### 🔌 Anthropic Messages 网关模式（把 wbmux 当主端点用）

上面的搜索端点**只处理搜索请求**——普通聊天打到它上面会被 400 拒绝。
而 ZCode / Claude Code / Cline 这类客户端只认一个端点（`ANTHROPIC_BASE_URL` /
自定义模型 Base URL），聊天和联网搜索的旁路请求发往同一地址。

网关模式把这个 Gap 补上：新增 `POST /v1/messages`，按请求内容分流——

```text
POST /v1/messages
   ├─ tools 声明 web_search  → wbmux 免费联网搜索（复用已登录凭据）
   └─ 其余                   → 字节级透传到 WBMUX_GATEWAY_UPSTREAM 配置的上游
```

上游预期为 **Anthropic Messages 兼容端点**（StepFun / GLM / DeepSeek /
Anthropic 官方均是），透传不做协议转换，流式响应原样转发，客户端自带的
认证头替换为配置的上游 Key。这样任意自定义模型都能零成本获得原生
`web_search`，普通对话完全不受影响。

**配置（环境变量，随 wbmux 一起启动）**：

| 变量 | 说明 |
|---|---|
| `WBMUX_GATEWAY_UPSTREAM` | 聊天上游 Base URL，如 `https://api.stepfun.com/step_plan` |
| `WBMUX_GATEWAY_API_KEY` | 上游 API Key（默认以 `x-api-key` 发送） |
| `WBMUX_GATEWAY_BEARER` | 设为 `1` 时上游 Key 改以 `Authorization: Bearer` 发送 |

未配置上游时，非搜索请求返回 503 并附带配置说明，搜索功能不受影响。

**客户端接入**（以 ZCode 自定义模型为例）：

| 配置项 | 值 |
|---|---|
| API 地址 | `http://127.0.0.1:<端口>` |
| API Key | 任意占位值（会被替换为 `WBMUX_GATEWAY_API_KEY`） |
| 模型能力 → 原生联网搜索 | **开启** |

**协议完备性**（2026-10-28 对齐 Anthropic 真实行为）：

- `stream: true` 时合成标准 SSE 事件序列（`ping` → `message_start` →
  `server_tool_use` → `web_search_tool_result` → `message_delta` →
  `message_stop`），按流式解析的客户端可直接消费；
- 响应含 `server_tool_use` 块（携带搜索词）与
  `usage.server_tool_use.web_search_requests` 计费字段；
- 尊重 `web_search` 工具声明里的 `max_uses`（结果条数）与
  `allowed_domains` / `blocked_domains`（域名过滤，blocked 优先）。


---

## 🤝 参与贡献与反馈

欢迎提交 Issue 与 Pull Request！在贡献代码时，请遵循以下规范：
- 保持单一可执行文件交付哲学，避免引入重型外部运行时依赖；
- 所有修改需通过自动化测试：`go test ./...`；
- 遵循最小修改原则，确保对现有本地安装目录的零侵入。

---

## 📄 开源许可证与免责声明

本项目基于 [MIT 许可证](LICENSE) 开源。

**免责声明**：本项目为独立的第三方辅助工具，旨在方便开发者管理本地工作区与迁移历史配置，与腾讯、WorkBuddy 或 CodeBuddy 无从属或商业背书关系。

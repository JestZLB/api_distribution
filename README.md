# API Distribution

一个面向 LLM 应用的本地 API 网关桌面应用，基于 [Wails v2](https://wails.io) 构建。
对外暴露 **OpenAI 兼容协议**（`/v1/chat/completions` 等），统一代理多个上游（OpenAI、Anthropic、Azure、Gemini、Ollama、任意 OpenAI 兼容端点），支持模型别名、客户端鉴权、流式 token 计量，以及实时流量与统计面板。

![Dashboard Preview](https://trae-api-cn.mchost.guru/api/ide/v1/text_to_image?prompt=screenshot%20of%20a%20native%20desktop%20application%20window%2C%20modern%20API%20gateway%20analytics%20dashboard%2C%20Linear%20Vercel%20style%20developer%20tool%20UI%2C%20dark%20slate%20background%2C%20deep%20indigo%20primary%20accent%2C%20four%20KPI%20cards%20on%20top%20showing%20token%20consumption%20numbers%20with%20percentage%20change%20indicators%2C%20server%20status%20panel%20with%20pulsing%20online%20indicator%20and%20base%20URL%2C%20stacked%20area%20chart%20of%20hourly%20request%20traffic%2C%20list%20of%20client%20keys%20with%20traffic%20volume%2C%20sidebar%20navigation%2C%20professional%20SaaS%20aesthetic%2C%20clean%20typography&image_size=landscape_16_9)

## ✨ 核心功能

- **OpenAI 兼容代理**：默认监听 `127.0.0.1:8080`，对外暴露 `/v1/chat/completions`、`/v1/completions`、`/v1/models`、`/healthz`，对接 OpenAI 生态客户端零成本。
- **多上游路由**：内置 6 种 provider 类型，覆盖任何 OpenAI 兼容端点（vLLM、LM Studio、自建网关等）。
- **Anthropic 协议自动转换**：当上游为 Anthropic 时，请求体自动转为 `messages` API，响应体（JSON / SSE）反向翻译为 OpenAI 格式；**完整保留** `tool_use` / `tool_result` / `image_url` 等内容块，不再扁平化为纯文本。
- **模型别名**：将任意公开别名（如 `gpt-4o`、`claude-sonnet`）映射到具体 provider + providerModel，可在 UI 中自由增删改。
- **客户端密钥鉴权**：多 Key + Label + Enabled 开关；空 Key 列表 = 开放模式（适合本地开发）。鉴权使用 `crypto/subtle.ConstantTimeCompare`，按 Key 长度排序扫描避免时序侧信道。
- **实时流量与统计**：每 2s 通过 Wails `EventsEmit` 推送 `stats:changed` / `logs:changed` / `config:changed` 事件，Dashboard / Logs 页面订阅刷新。后端带 100ms 进程内 stats 缓存（`SetOnChange` 失效钩子），并发心跳共享一次重算。
- **多维聚合**：当日 vs 昨日、上 7 天 / 上 30 天 / 全时，每个都附带上一窗口对比用于百分比展示；按别名 / provider / client-key 维度切片；近 30 天小时桶曲线 + 按 alias 的 token 量曲线。
- **流式 token 计量**：自动注入 `stream_options.include_usage=true`，保证 OpenAI 流式响应尾部带 `usage`；Anthropic 路径同步回填。token 数 `clampToInt32` 防溢出污染日聚合。
- **上游连接池**：每个 provider 缓存一个 `http.Client`（含连接池），5 分钟空闲回收（`internal/upstream.ReapIdle`）；shutdown 时强制 `ReapIdle(0)` 释放 idle TCP。
- **系统集成**：开机自启动（Windows `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`）、关闭最小化到托盘、托盘 Show/Quit 菜单（与 UI 语言同步）。
- **配置导入导出**：导出时对所有 API Key 做全 `*` 脱敏（保留 8+ 字符长度）。
- **SQLite 持久化**：[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) 纯 Go（无 CGO）单一数据库，日志与按日聚合同库；旧版 `logs.json` / `stats/*.json` 会在首次启动时自动迁移。

## 🧱 技术栈

| 层 | 技术 | 版本 |
|---|---|---|
| 桌面壳 | [Wails](https://wails.io) | v2.14.0 |
| 后端 | Go | 1.25 |
| 前端框架 | React | 19 |
| 前端语言 | TypeScript | 5 |
| UI 库 | [antd](https://ant.design) | 6 |
| 图表 | [@ant-design/charts](https://charts.ant.design/)（动态 import） | 2 |
| 路由 | react-router-dom | 7 |
| 状态管理 | [zustand](https://github.com/pmndrs/zustand) | 5 |
| 样式 | Tailwind CSS | 4 |
| 构建工具 | Vite | 8 |
| 图标 | react-icons / @phosphor-icons/react | — |
| 持久化 | SQLite（`modernc.org/sqlite`，纯 Go / 无 CGO） | 1.57.0 |

## 🌐 HTTP API

所有 `/v1/*` 路径在配置了 ClientKeys 时强制 `Authorization: Bearer ...` 鉴权（常数时间比较）；`/healthz` 始终开放。请求体大小受 `Config.MaxRequestBodyMB` 限制（默认 256 MiB，仅作内存安全网，**不**充当 IDE 级上下文长度闸门）。

| 路由 | 方法 | 说明 |
|---|---|---|
| `/v1/chat/completions` | POST | Chat Completions 主入口，支持 `stream: true` SSE。请求体中的 `model` 字段视为公开别名，代理会改写为 provider 侧真实 model ID 后转发。 |
| `/v1/completions` | POST | 旧版 Completions 别名，与 `/v1/chat/completions` 走同一条转发路径。 |
| `/v1/models` | GET | 返回当前已启用的 model aliases（`id` / `object` / `owned_by`）。 |
| `/healthz` | GET | `{status, providers_total, providers_enabled, aliases_total, aliases_enabled, uptime_seconds}`，供健康探针使用。 |

`/healthz` 始终返回 JSON；`/v1/models` 鉴权失败返回 401 `{"error":{"message":"unauthorized","type":"auth_error"}}`。

## 📂 目录结构

```
api_distribution/
├── app.go                      # Wails App 绑定 + 生命周期（含 7 步确定性 shutdown）
├── main.go                     # 进程入口 + wails.Run 配置 + 关闭到托盘回调
├── wails.json                  # Wails 项目配置
├── go.mod / go.sum             # Go 模块
├── build/                      # Wails 平台构建资源 (icon, installer, manifests)
├── frontend/
│   ├── src/
│   │   ├── components/         # 复用组件 (layout / ui / settings / charts)
│   │   ├── pages/              # Dashboard / Providers / Models / Logs / Settings
│   │   ├── store/              # zustand stores (config, system, theme, locale, toast)
│   │   ├── lib/                # 工具 (format, clipboard, antdTheme, wails)
│   │   ├── i18n/               # 多语言 (zh-CN / en-US / ja-JP / ko-KR)
│   │   ├── types/              # 前端 TS 类型 (从 wailsjs 透传)
│   │   ├── App.tsx / main.tsx  # React 入口
│   │   └── style.css           # 全局样式 + CSS 变量
│   ├── wailsjs/                # Wails 生成的 TS 绑定 (运行时生成，不入库)
│   ├── package.json
│   └── vite.config.ts
├── internal/
│   ├── anthropicconv/          # OpenAI <-> Anthropic Messages 协议转换（保留 tool_use/tool_result/image）
│   ├── config/                 # 配置持久化 (atomic write + Apply 模式防并发丢失)
│   ├── ioextra/                # I/O 辅助 (WrapCloser 等)
│   ├── server/                 # HTTP 代理 (routing, auth, streaming, SSE reader pool)
│   ├── store/                  # SQLite 日志 + 按日聚合 + 增量 dirty-write 落盘
│   ├── system/                 # 平台集成 (tray, autostart, safeGo)
│   ├── types/                  # 共享数据模型
│   └── upstream/               # 上游 HTTP 客户端池 (cache + idle reap)
└── *_test.go                   # Go 测试 (server / store / upstream / lifecycle / 重启 round-trip / shutdown 竞态)
```

## 🚀 开发

### 前置依赖

- Go ≥ 1.25
- Node.js ≥ 20
- [Wails CLI](https://wails.io/docs/gettingstarted/installation)：`go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- Windows：WebView2 Runtime（Win11 默认已装，Win10 需手动安装）
- macOS：Xcode Command Line Tools
- Linux：`webkit2gtk-4.0-dev`、`libgtk-3-dev`、`libayatana-appindicator3-dev`

### 启动开发模式

```bash
# 1. 安装前端依赖（统一使用 npm）
cd frontend && npm ci && cd ..

# 2. 启动 wails dev（自动监听 Go + 前端 HMR）
wails dev
```

开发模式会同时启动：

- Vite dev server（端口 5173，前端 HMR）
- Wails Go 后端 + WebView 窗口
- 浏览器调试：`http://localhost:34115`（可在 DevTools 调用 Go 方法）

### 构建生产二进制

```bash
wails build
# 输出：build/bin/api_distribution (.exe on Windows)
```

可执行文件已嵌入前端 dist，单文件分发。

### 运行测试

```bash
# 后端
go test ./...

# 前端类型检查
cd frontend && npm run typecheck

# 前端构建（typecheck + vite build）
cd frontend && npm run build
```

后端覆盖以下回归用例：HTTP 转发、`rewrite.go` 的 `model` 字段就地改写、SSE reader pool、Anthropic ↔ OpenAI 转换、配置并发 Apply、SQLite 重启 round-trip、shutdown 竞态、内存审计与并发硬化等。

## ⚙️ 配置 & 数据目录

应用首次启动时自动创建两个目录：

- **配置目录**（Windows：`%USERPROFILE%\.api_distribution\`；macOS / Linux：`~/.api_distribution/`）
- **数据目录**：可执行文件旁的 `data/` 子目录

```
~/.api_distribution/
└── config.json            # Providers / ModelAliases / ClientKeys / 服务端设置 / 系统偏好
                          # ServerHost / ServerPort / LogRetention / ShutdownTimeoutSec /
                          # MaxRequestBodyMB / CloseToTray / AutoStart / TrayEnabled / Locale

<exe>/data/
└── api_distribution.db    # 单一 SQLite 文件，含 daily_stats + logs 两张表
```

> ⚠️ **备份建议**：`config.json` 中包含明文 API Key；定期备份后请妥善保管，或在使用导出功能后删除本地副本。

### 关键默认值（`types.DefaultConfig`）

| 字段 | 默认 | 含义 |
|---|---|---|
| `ServerHost` | `127.0.0.1` | 监听地址 |
| `ServerPort` | `8080` | 监听端口 |
| `LogRetention` | `30` | 日志保留天数；`persistLogsLoop` 每 5s 按该窗口执行 `PurgeOlderThan` |
| `ShutdownTimeoutSec` | `5` | `server.Stop` 等待 SSE 流排空的最长秒数（1-60） |
| `MaxRequestBodyMB` | `256` | 请求体与缓冲型响应体的内存安全网（1-1024） |
| `Locale` | `en-US` | UI 语言（zh-CN / en-US / ja-JP / ko-KR） |

## 🗄️ 持久化模型

`internal/store` 把日志与按日统计折叠到 **一张 SQLite 数据库**里：

- **`logs(id PK, ts, ...)`**：环形缓冲最多 2000 条；`dirtyIdx` / `dirtyStart` 两个 atomic 计数器驱动**增量** flush，写路径只在 `[dirtyStart, dirtyIdx)` 区间工作，未变更时 5s 心跳完全跳过 SQL。
- **`daily_stats(date PK, requests, input_tokens, output_tokens, errors, latency_sum, by_model JSON, by_provider JSON, by_client_key JSON, by_hour JSON)`**：日历日聚合，含按 provider / alias / client-key 的切片以及 30 天 × 24 小时桶（请求量 + 错误数 + 输入/输出 token + 按 alias 的总 token）。
- **G-006 增量计数**：`Append` 在写入日志的同时维护 `recentByClientKey` / `todayByModel` / `todayByProvider` / `monthCounts`，让 `StatsWithComparison` 在 30 天数据下走**单遍** `s.days` 排序快照即可回答 Dashboard 的全部热问题。
- **100ms `statsCache`**：进程内 TTL 缓存（`statsCache.invalidate` 由 `SetOnChange` 触发），让并发 `stats:changed` 心跳共享一次重算。

写入路径有三重保证：

1. **`persistLogsLoop`**：每 5s 检查 `dirty` 标志，flush 一次 `SaveLogs + SaveDaily`（`flushMu` 串行化）。
2. **`scheduleFlush`**：每次 `Append` 触发 3s 防抖，只启动首个 timer 把突发写入折叠成一次落盘（`flushArmed.CompareAndSwap`）。
3. **`shutdown`**：关闭前在 200ms 等待预算内给后台 loop 一次收尾机会，再做一次序列化 final flush，然后才 `store.Close()` —— 避免 Windows 上 SQLite 文件锁残留导致下次启动"database is locked"。

`MigrateLegacyJSON(cfgDir)` 在 `store` 启动时运行：把旧版 `logs.json` + `stats/*.json` 自动导入到 SQLite 后保留原文件（**幂等**，已存在数据时跳过）。

## 🧩 内置 Provider 类型

| 类型 | 默认 BaseURL | 鉴权方式 |
|---|---|---|
| `openai` | `https://api.openai.com/v1` | `Authorization: Bearer ...` |
| `anthropic` | `https://api.anthropic.com/v1` | `x-api-key: ...` |
| `azure` | （用户填写） | `api-key: ...` |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta/openai` | `Authorization: Bearer ...` |
| `ollama` | `http://127.0.0.1:11434/v1` | 无（可选 Bearer） |
| `custom` | （用户填写） | `Authorization: Bearer ...` |

`TestProvider` 端点会按类型差异探测：`OpenAI / Gemini / Custom` 走 `GET /models`；`Anthropic` 走 `HEAD /`；`Azure` 走 `GET /openai/models?api-version=2024-10-21`；`Ollama` 走 `GET /api/tags`。

## 🤖 Anthropic 转换说明

`internal/anthropicconv` 在不依赖任何第三方 SDK 的前提下完成 OpenAI ↔ Anthropic Messages 互转：

- **完整保留内容块**：`text` / `image_url` / `tool_use` / `tool_result` 全部翻译，不再扁平化。
- **流式响应翻译**：将 Anthropic 的 `message_start` / `content_block_start` / `content_block_delta` / `message_delta` / `message_stop` 事件翻译回 OpenAI 的 `data: {...}` 帧，并把最终 `usage` 字段回填到 `LogEntry`。
- **`stream_options.include_usage` 自动注入**：在转发 OpenAI 流式请求前确保服务端返回 `usage` 字段。
- **客户端断连归类**：SSE 中途被客户端 cancel（`broken pipe` / `connection reset` 等）不会被记为请求错误，避免 Dashboard 错误率虚高。

## 🌍 多语言

支持 4 种语言（zh-CN / en-US / ja-JP / ko-KR），运行时按浏览器语言首选项选择，可在 TopBar → Language 切换并持久化到 `Config.Locale`。托盘菜单同步跟随 UI 语言。

## 🔒 安全注意事项

- **API Key 明文存储**：当前版本将 provider API key 与 client key 明文保存在 `config.json`；攻击模型下 core dump 或取证可读出。建议生产环境使用 OS 凭据库（Windows DPAPI / macOS Keychain）。
- **导出脱敏**：使用 Settings → 导出配置功能时，所有 API key 会被全 `*` 掩码（保留 8 字符以上长度），不会泄露原始字符。
- **关闭到托盘**：启用该选项后，关闭主窗口不会退出进程；如需彻底退出，使用托盘菜单 Quit 或在 Settings 页面点击 Quit App。
- **自启动**：写入 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`；如需关闭，前往 Settings 关闭自动启动或运行 `regedit` 手动清理。
- **客户端密钥**：网关密钥长度至少 8 字符；鉴权使用 `crypto/subtle.ConstantTimeCompare`，且按 Key 长度排序扫描，避免时序侧信道。
- **Token 计数防溢出**：上游返回的 `prompt_tokens` / `completion_tokens` 在写入 `LogEntry`（int32 字段）前会被 clamp 到 `[0, math.MaxInt32]`，防止恶意值污染日聚合。

## 🛠️ 故障排查

- **二进制无法启动 WebView**：安装 [WebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/) 后重试。
- **端口被占用**：修改 Settings → 服务端 → 端口，或停止占用 8080 的进程。
- **托盘图标不显示**：重启 `explorer.exe`，或前往 Settings 关闭/重新开启托盘开关。
- **统计不刷新**：检查 2s 心跳是否被防火墙拦截（前端 → Go IPC 通过 Wails 内部通道，不受系统代理影响）。控制台日志里 `statsComputeCountForTest` 是后端重计算次数，配合前端 `subscribeStatsEvents` 排查事件链路。
- **重启后数据丢失**：检查 `<exe>/data/api_distribution.db` 是否可写；进程被强制结束时最多丢失最近 3 秒的写入（`scheduleFlush` 防抖 + 5s ticker 双保险）。
- **重启报 "database is locked"**：上一个进程尚未完全退出，Windows 上 SQLite 文件锁残留；等待约 1 秒让前一个进程完成 `shutdown` 中的 `store.Close()`。

## 🤝 贡献

1. Fork & clone
2. 创建 feature 分支：`git checkout -b feat/your-feature`
3. 提交前运行 `go test ./...` 与 `cd frontend && npm run typecheck`
4. Commit message 推荐格式：`feat: xxx` / `fix: xxx` / `refactor: xxx`
5. PR 中附上：变更说明、关联 issue、UI 截图（如有 UI 变更）

## 📄 许可证

本仓库源码以项目所有方约定为准。
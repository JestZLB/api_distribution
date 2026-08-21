# API Distribution

一个面向 LLM 应用的本地网关，基于 [Wails v2](https://wails.io) 构建，统一对外暴露 OpenAI 兼容协议，支持多上游（OpenAI、Anthropic、Azure、Gemini、Ollama、自定义 OpenAI 兼容端点）路由、模型别名、客户端鉴权、实时流量与统计面板。

![Dashboard Preview](https://trae-api-cn.mchost.guru/api/ide/v1/text_to_image?prompt=modern%20desktop%20dashboard%20interface%20with%20api%20gateway%20analytics%2C%20clean%20minimal%20design%2C%20green%20accent%20color&image_size=landscape_16_9)

## ✨ 核心功能

- **OpenAI 兼容代理**：默认监听 `127.0.0.1:8080`，对外暴露 `/v1/chat/completions`、`/v1/completions`、`/v1/models`、`/healthz`，对接 OpenAI 生态客户端零成本。
- **多上游路由**：内置 6 种 provider 类型，支持任意 OpenAI 兼容端点（vLLM、LM Studio、自建网关等）。
- **Anthropic 自动转换**：以 Anthropic 为上游时，请求体自动转 `messages` API、响应体（JSON / SSE）反向翻译为 OpenAI 格式，客户端无感。
- **模型别名**：将任意公开别名（如 `gpt-4o`、`claude-sonnet`）映射到具体 provider + providerModel，可在 UI 中自由增删改。
- **客户端密钥鉴权**：多 Key + Label + Enabled 开关；空 Key 列表 = 开放模式（适合本地开发）。
- **实时流量与统计**：每 2s 心跳推送 `stats:changed` / `logs:changed` 事件，Dashboard 与 Logs 页面订阅刷新；按 24h / 7d / 总计多维聚合。
- **系统集成**：开机自启动（Windows Run key）、关闭最小化到托盘、托盘 Show/Quit 菜单。
- **配置导入导出**：导出时对所有 API Key 做全 `*` 脱敏。

## 🧱 技术栈

| 层 | 技术 | 版本 |
|---|---|---|
| 桌面壳 | [Wails](https://wails.io) | v2.14.0 |
| 后端 | Go | 1.25 |
| 前端框架 | React | 19 |
| 前端语言 | TypeScript | 5 |
| UI 库 | [antd](https://ant.design) | 6 |
| 路由 | react-router-dom | 7 |
| 状态管理 | [zustand](https://github.com/pmndrs/zustand) | 5 |
| 样式 | Tailwind CSS | 4 |
| 构建工具 | Vite | 8 |
| 图标 | react-icons / phosphor-icons | — |

## 📂 目录结构

```
api_distribution/
├── app.go                      # Wails App 绑定 + 生命周期
├── main.go                     # 进程入口 + wails.Run 配置
├── wails.json                  # Wails 项目配置
├── go.mod / go.sum             # Go 模块
├── build/                      # Wails 平台构建资源 (icon, installer, manifests)
├── frontend/
│   ├── src/
│   │   ├── components/         # 复用组件 (layout, ui, settings, charts)
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
└── internal/
    ├── anthropicconv/          # OpenAI <-> Anthropic Messages 协议转换
    ├── config/                 # 配置持久化 (atomic 写入)
    ├── ioextra/                # I/O 辅助 (WrapCloser 等)
    ├── server/                 # HTTP 代理 (routing, auth, streaming, SSE)
    ├── store/                  # 日志 ring buffer + 按日聚合 + dirty-write 落盘
    ├── system/                 # 平台集成 (tray, autostart, safeGo)
    ├── types/                  # 共享数据模型
    └── upstream/               # 上游 HTTP 客户端池 (cache + reap)
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

## ⚙️ 配置目录

应用首次启动时自动创建配置目录（Windows：`%USERPROFILE%\.api_distribution\`，macOS/Linux：`~/.api_distribution/`），可从 Settings 页面查看完整路径。

```
~/.api_distribution/
├── config.json          # 主配置 (ServerHost/Port, Providers, ModelAliases, ClientKeys, ...)
├── logs.json            # 最近 2000 条请求日志快照
└── stats/               # 按日聚合的统计
    └── 2026-08-21.json
```

> ⚠️ **备份建议**：`config.json` 中包含明文 API Key；定期备份后请妥善保管或在使用导出功能后删除本地副本。

## 🔒 安全注意事项

- **API Key 明文存储**：当前版本将 provider API key 与 client key 明文保存在 `config.json`；攻击模型下 core dump 或取证可读出。建议生产环境使用 OS 凭据库（Windows DPAPI / macOS Keychain）。
- **导出脱敏**：使用 Settings → 导出配置功能时，所有 API key 会被全 `*` 掩码（保留 8 字符以上长度），不会泄露原始字符。
- **关闭到托盘**：启用该选项后，关闭主窗口不会退出进程；如需彻底退出，使用托盘菜单 Quit 或在 Settings 页面点击 Quit App。
- **自启动**：写入 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`；如需关闭，前往 Settings 关闭自动启动或运行 `regedit` 手动清理。
- **客户端密钥**：网关密钥长度至少 8 字符；在生成后请立即保存到安全位置，再次查看只能通过 UI 显式展开。

## 🧩 内置 Provider 类型

| 类型 | 默认 BaseURL | 鉴权方式 |
|---|---|---|
| `openai` | `https://api.openai.com/v1` | `Authorization: Bearer ...` |
| `anthropic` | `https://api.anthropic.com/v1` | `x-api-key: ...` |
| `azure` | （用户填写） | `api-key: ...` |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta/openai` | `Authorization: Bearer ...` |
| `ollama` | `http://127.0.0.1:11434/v1` | 无（可选 Bearer） |
| `custom` | （用户填写） | `Authorization: Bearer ...` |

## 🌍 多语言

支持 4 种语言，运行时自动按浏览器语言首选项选择（zh-CN / en-US / ja-JP / ko-KR），可在 TopBar → Language 切换并持久化到 localStorage。

## 🛠️ 故障排查

- **二进制无法启动 WebView**：安装 [WebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/) 后重试。
- **端口被占用**：修改 Settings → 服务端 → 端口，或停止占用 8080 的进程。
- **托盘图标不显示**：重启 explorer.exe，或前往 Settings 关闭/重新开启托盘开关。
- **统计不刷新**：检查 30s 心跳是否被防火墙拦截（前端 → Go IPC 通过 Wails 内部通道，不受系统代理影响）。

## 🤝 贡献

1. Fork & clone
2. 创建 feature 分支：`git checkout -b feat/your-feature`
3. 提交前运行 `go test ./...` 与 `cd frontend && npm run typecheck`
4. Commit message 推荐格式：`feat: xxx` / `fix: xxx` / `refactor: xxx`
5. PR 中附上：变更说明、关联 issue、UI 截图（如有 UI 变更）

## 📄 许可证

本仓库源码以项目所有方约定为准。
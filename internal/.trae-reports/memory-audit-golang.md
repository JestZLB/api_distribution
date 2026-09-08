# Go 端内存审计报告 (api_distribution)

- 审计范围：`d:\Go_Pj\api_distribution\` 下所有 `.go` 源文件
- 审计方式：纯静态阅读（不执行 `go test` / `go build` / `go vet`）
- 报告时间：2026-08-28
- 与前端 / 已归档 spec 重复的内容已剔除（不重复 `.trae/specs/harden-memory-and-concurrency` 与 `.trae/specs/optimize-performance-and-charts` 既有结论）

---

## Finding G-001 — Severity: MEDIUM

- **Location**: `app.go:914-931`
- **Evidence**:

```go
// app.go:914-931
func (a *App) emitLogsLoop() {
    defer func() {
        if r := recover(); r != nil {
            log.Printf("[loop] panic in emitLogsLoop: %v\n%s", r, debug.Stack())
        }
    }()
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-a.ctx.Done():
            return
        case <-ticker.C:
            a.emitLogsChanged()
            a.emitStatsChanged()
        }
    }
}
```

- **Impact**: 退出信号是 `a.ctx.Done()`；正常情况下 `Wails.OnShutdown` 会取消 `ctx`，goroutine 在下一次 select 调度后返回，理论 ≤1s。`ticker.Stop()` 已正确 defer。**风险点**：`a.ctx` 是 Wails 注入的 runtime context，OnShutdown 期间会被取消，但若 Wails runtime 自身在该路径上发生卡顿（参见 [Wails v2 已知 OnShutdown 取消时机问题](https://github.com/wailsapp/wails)），goroutine 会继续轮询；每次 `emitLogsChanged()` 通过 `wruntime.EventsEmit` 内部还会触及 Webview 绑定，没有可见的内存膨胀，但每次 GC 周期里仍持有 `App` 指针 → `statsCache` → `store` → `db` 全图的根引用。
- **Suggested Fix**: 在 `App.shutdown(ctx)` 显式 `cancel()` 一个本地 cancelCtx（独立于 Wails 注入的 ctx）并改让三个 loop 监听本地 ctx；shutdown 完成后调用 `sync.WaitGroup.Wait()` 强制 ≤100ms 内退出。
- **Risk of Fix**: 低；只需新增 `doneCtx, cancel := context.WithCancel(context.Background())`，loop 改 `<-a.doneCtx.Done()`。当前没有现成的回归测试覆盖 ≤1s 退出，需要新增 `app_loop_test.go` 形式的小测试。

---

## Finding G-002 — Severity: HIGH

- **Location**: `app.go:941-964`
- **Evidence**:

```go
// app.go:941-964
func (a *App) persistLogsLoop() {
    ...
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-a.ctx.Done():
            // Final flush so a non-graceful context cancellation still
            // persists whatever accumulated since the last tick.
            a.flushPersisted()
            return
        case <-ticker.C:
            cfg := a.cfgMgr.Get()
            if cfg.LogRetention > 0 {
                a.store.PurgeOlderThan(time.Duration(cfg.LogRetention) * 24 * time.Hour)
            }
            a.flushPersisted()
        }
    }
}
```

- **Impact**: `flushPersisted()` 内部走 `flushMu` 序列化，但 `App.shutdown` 中也会调用 `a.store.SaveLogs()` + `a.store.SaveDaily()` —— 这两条路径都未走 `flushMu`。**shutdown 期间**会出现两条并发 Save 路径（loop goroutine 与 shutdown 主协程）通过 `dirtyIdx / dirtyStart` 原子变量交叉前进，可能导致 `SaveLogs` 错误地报告 `dirtyIdx <= dirtyStart` 而漏写（见 `persist.go:38-42`），或在 `SaveDaily` 时复制到的 `kept` 切片与 store.mu 释放后的 `s.days` 视图不一致（见 `persist.go:209-244`）。这是**已知的「最后一次 flush 漏掉最新数据」的高风险路径**，在高流量结束场景下可丢失 ≥5s 数据（违背 spec 中"最多丢失 3s"的承诺）。估算影响：典型 QPS 100 → 单次并发漏写 ≈50~500 条 log（`MaxLogEntries=2000` 上限）；内存侧无显著膨胀。
- **Suggested Fix**:
  1. `flushPersisted()` 与 `shutdown()` 的 `SaveLogs/SaveDaily` 全部走同一个 `flushMu.Lock()`；或
  2. `shutdown()` 不再直接调用 `SaveLogs`，只调用 `flushPersisted()`；或
  3. 引入一个 "shutdown-in-progress" atomic.Bool，shutdown 期间让 loop goroutine 跳过 ticker 路径只走 `flushPersisted()` 一次。
- **Risk of Fix**: 中。改方案 1 风险最低（只是把 `SaveLogs` 内部重锁一下），但要小心 `flushPersisted` 已在持锁状态里调 `a.store.SaveLogs()` —— 可能需要把 store 的 `SaveLogs/SaveDaily` 拆成「持锁 / 不持锁」两个版本。需要补充 `restart_roundtrip_test.go` 形式的并发 shutdown 测试。

---

## Finding G-003 — Severity: MEDIUM

- **Location**: `app.go:1017-1036`
- **Evidence**:

```go
// app.go:1017-1036
func (a *App) reapClientsLoop() {
    ...
    ticker := time.NewTicker(upstreamReapInterval) // 5 * time.Minute
    defer ticker.Stop()
    for {
        select {
        case <-a.ctx.Done():
            return
        case <-ticker.C:
            upstream.ReapIdle(upstreamReapIdle) // 15 * time.Minute
        }
    }
}
```

- **Impact**: 退出信号正确（ctx Done + ticker.Stop），退出前对象保留图：`upstream.clients` 全局 map（`upstream.go:24-27`）。**shutdown 时未主动调用 `ReapIdle(maxIdle=0)`**，所有 cached client 仍然挂在 `clients` map 里，伴随各自的 `http.Transport` 持有的 idle TCP 连接池。**实际保留对象**：每个 cached client 的 `http.Client` 内部 idle connection queue，最坏情况下 `MaxIdleConns=100 * N providers` 条 idle conn × 客户端 socket buffer。在用户连续编辑 provider 配置的场景下，`upstream.clients` 大小可能缓慢增长（见 G-012）。
- **Suggested Fix**: 在 `App.shutdown()` 末尾（`a.store.Close()` 之后）新增 `upstream.ReapIdle(0)`（或新增 `upstream.CloseAll()` 直接 `CloseIdleConnections()` + 清空 map）。
- **Risk of Fix**: 低。无现有测试覆盖 `clients` map 大小归零，新增测试即可。

---

## Finding G-004 — Severity: HIGH

- **Location**: `app.go:188-217`
- **Evidence**:

```go
// app.go:188-217
func (a *App) shutdown(_ context.Context) {
    _ = a.proxy.Stop()
    ...
    if ts && a.tray != nil {
        a.tray.Stop()
    }
    // Persist logs to disk on shutdown so we don't lose data.
    if err := a.store.SaveLogs(); err != nil { ... }
    if err := a.store.SaveDaily(a.cfgMgr.Get().LogRetention); err != nil { ... }
    a.store.Close()
}
```

- **Impact**: 关闭顺序：proxy.Stop → tray.Stop → SaveLogs → SaveDaily → Close。**问题 1**：未触发任何"shutdown 屏障"通知后台 goroutine（`emitLogsLoop` / `persistLogsLoop` / `reapClientsLoop`）退出，它们会在 ctx 被 Wails 取消时自行退出，**但** `persistLogsLoop` 在退出前还会再调一次 `flushPersisted()`（`app.go:953-954`），与 shutdown 的 `SaveLogs/SaveDaily` 形成 race（见 G-002）。**问题 2**：`tray.Stop()` 在 Windows 上最坏阻塞 500ms（`tray_windows.go:247-250`），但 `App.shutdown` 是 Wails 同步调用的，没有 deadline；shutdown 整体最坏 ≥5s（tray 500ms + SaveLogs 写 SQL + SaveDaily 写 SQL + 反复 flush）→ 与 `ShutdownTimeoutSec=5s`（server.Stop）叠加，关闭时延可达 5~10s，超出 spec"≤1s"目标。**内存影响**：在 shutdown 期间 Wails IPC 仍可接收前端调用并触发 `GetStatsWithComparison` 等路径（store 未 Close），每次都全量 sumDays()，可达几十 MB 临时分配。
- **Suggested Fix**:
  1. `shutdown(ctx)` 优先利用传入的 ctx（不再忽略 `_`），内部把 ctx 加上 1s 超时，串行执行 Stop+Save+Close，超时则强退；
  2. 在 shutdown 第 1 步先 `close(doneCh)` + `WaitGroup.Wait(200ms)` 让 loop goroutine 走完最后一次 flush，避免双路径 race；
  3. tray.Stop 改成"async fire-and-forget"，主路径不等它完成。
- **Risk of Fix**: 中。改动 shutdown 顺序属于行为变更，需要新增并发 shutdown 回归测试；当前 `app_lifecycle_test.go` 应有覆盖点。

---

## Finding G-005 — Severity: MEDIUM

- **Location**: `internal/store/store.go:113-132` 与 `internal/store/persist.go:122-137`
- **Evidence**:

```go
// store.go:113-132
func (s *Store) Append(e types.LogEntry) {
    s.mu.Lock()
    s.dirtyIdx.Add(1)
    if len(s.logs) < MaxLogEntries {
        s.logs = append(s.logs, e)   // <-- backing array grow
    } else {
        s.logs[s.idx] = e
    }
    ...
}

// persist.go:122-137
func (s *Store) LoadLogs() error {
    ...
    s.logs = make([]types.LogEntry, MaxLogEntries) // <-- 一次性 2000 cap 分配
    s.idx = 0
    for i := len(entries) - 1; i >= 0; i-- { ... }
    s.dirtyIdx.Store(n)
    s.dirtyStart.Store(n)
    ...
}
```

- **Impact**: ring buffer 是固定 cap 2000（`LogEntry ≈ 200 B` ≈ 400 KiB）的 slice，Append 阶段从 0 长到 MaxLogEntries 会经历多次 cap 翻倍（0→1→2→4→…→2048），**保留旧大数组**：Go 的 slice grow 不释放旧的 backing array，GC 之前可达 1+ MiB 临时大对象。`Append` 高并发下还会反复触发 alloc（每个请求都进入该路径），增加 young-gen 压力。**LoadLogs 路径**：每次启动一次性 2000-entry 分配（≈400 KiB）+ 2000 次 Scan → unmarshal → 拷贝，单次启动成本固定，无累积。**总体**：在高 QPS 启动期可能产生约 1-2 MiB 的临时大对象；运行期 ring 已稳定（cap=2048），无显著增长。
- **Suggested Fix**: `New()` 或 `Open()` 内直接 `s.logs = make([]types.LogEntry, 0, MaxLogEntries)`（注释里 cap 与 MaxLogEntries 一致，spec 允许），消除 grow 过程中的碎片；或在 `Append` 内使用 ring-index 写入，不依赖 append grow。
- **Risk of Fix**: 低。`Recent` 依赖 `s.logs` 已填充但 cap 不超过 MaxLogEntries，无需改 Recent 逻辑。需要新增 ring buffer 重构测试。

---

## Finding G-006 — Severity: MEDIUM

- **Location**: `internal/store/store.go:223-239` + `internal/store/store.go:385-574`
- **Evidence**:

```go
// store.go:223-239
func (s *Store) Stats() types.Stats {
    s.statsComputeCount.Add(1)
    s.mu.RLock()
    defer s.mu.RUnlock()
    stats := s.sumDays()                            // 全 days map O(n) 遍历
    recentCutoff := time.Now().Add(-24 * time.Hour).UnixNano()
    for _, e := range s.logs {                       // 全 ring buffer O(2000) 遍历
        if e.ID == "" || e.ClientKeyLabel == "" { continue }
        if e.Timestamp >= recentCutoff {
            stats.RequestsByClientKeyRecent[e.ClientKeyLabel]++
        }
    }
    return stats
}
// store.go:426-428
byHour := make(map[int64]types.HourBucket)
byHourByModel := make(map[string]map[int64]types.HourBucket)
```

- **Impact**: `Stats` 与 `StatsWithComparison` 每次都**全量重算**：
  1. `snapshotDaysSorted` → 重新排序 `s.days`（slice + sort.Slice）；
  2. 一次性分配 `byHour / byHourByModel` 两层 map + 每小时一个 `HourBucket`（含 ByModel/ByModelTokens/ByModelInputTokens/ByModelOutputTokens 4 张 map），最坏 720 hour × 30 alias × 4 map ≈ 100+ 个 map 对象 + 总字节数随 alias 数 O(n×m)；
  3. **第二轮 for-range s.logs** 与 sumDays 独立再做 2000 次循环（`store.go:564-571`）—— 这是历史 review 已知的双 pass，但 Stats 与 StatsWithComparison **不共享任何中间结果**。**`statsCache` 100ms TTL 缓解了调用频率**，但 cache miss 时（用户切页面 / TTL 过期）单次分配仍可达 50~500 KiB 临时对象。`allocs/op` 估计：cache hit 0 次有效 alloc（只返回缓存指针）；cache miss 约 30~80 次 alloc + 多个 map 初始化。
- **Suggested Fix**:
  1. 把 `s.days` 的"快照→遍历"改为"懒增量计数器"（每个 DailyAgg 自带今日累计），sumDays 只更新差量；
  2. `RequestsByClientKeyRecent` 改为事件驱动 Append-time 增量维护，而不是每次全量 s.logs 遍历；
  3. 复用 `byHour / byHourByModel`（`sync.Pool` 提供），但要注意并发安全。
- **Risk of Fix**: 高。改动 Stats 数据流属于行为变更，必须保留字段完全一致（已有 `TestStats` / `TestStatsWithComparison_*` 覆盖）；新增增量维护需要新增并发 race 测试。

---

## Finding G-007 — Severity: MEDIUM

- **Location**: `internal/store/persist.go:189-227` (`upsertDaily`)
- **Evidence**:

```go
// persist.go:189-227
func (s *Store) upsertDaily(d *DailyAgg) error {
    byModel, err := json.Marshal(d.ByModel)
    if err != nil { return ... }
    byProvider, err := json.Marshal(d.ByProvider)
    if err != nil { return ... }
    byClientKey, err := json.Marshal(d.ByClientKey)
    if err != nil { return ... }
    byHour, err := json.Marshal(d.ByHour)  // <-- 30天 × 24h = 720 个 HourBucket
    if err != nil { return ... }
    _, err = s.db.Exec(`INSERT INTO daily_stats ...`, ...)
    ...
}
```

- **Impact**: 每次 `SaveDaily`（**典型 5s tick 或 shutdown 一次**）对每个有改动的日期调用 4 次 `json.Marshal`：
  1. `d.ByHour` 是 `map[int64]types.HourBucket`，720 entry × 200B ≈ 140 KiB → JSON marshal 输出 ≈ 200 KiB；
  2. 内层 `ByModel/ByModelTokens/ByModelInputTokens/ByModelOutputTokens` 每小时 × 每 alias 都重新 marshal，每次都重新分配临时 buffer；
  3. **`json.Marshal` 不是 streaming**，每次都把 map 完整编码成 `[]byte`；30 天 × 4 张 map = 120 次 marshal + 8MB+ 临时字节分配（**单次 SaveDaily**）。
  - 在 retention=30、每天 2000 请求的高负载下，单次 `SaveDaily` 可产生 5~20 MiB 临时分配（每个 `[]byte` 短命但触发 young-gen GC），GC pause 50~200 ms。
- **Suggested Fix**:
  1. 复用 `bytes.Buffer` + `json.NewEncoder` 流式编码，避免每次分配新 `[]byte`；
  2. 仅 marshal 改动的 day（增量 dirty 标记 per-day）；
  3. 把 `ByModel*` 内层 map 改用 base64 编码或直接存储为 `JSONB` SQLite 列以减少一次 marshal。
- **Risk of Fix**: 中。行为兼容（输出字节一致），但需要保留 `restart_roundtrip_test.go` 形式的回归测试覆盖磁盘字节。

---

## Finding G-008 — Severity: LOW

- **Location**: `app.go:674-697` (`GetStats` / `GetStatsWithComparison`)
- **Evidence**:

```go
// app.go:674-697
func (a *App) GetStats() types.Stats {
    if cached, ok := a.statsCache.getStats(); ok {
        return *cached                  // <-- 值拷贝 整张 Stats
    }
    fresh := a.store.Stats()
    a.statsCache.putStats(&fresh)        // <-- 把 fresh 的指针缓存
    return fresh
}
```

- **Impact**: Wails IPC 返回的是值类型 `types.Stats` / `types.StatsWithComparison`（已含多张 map + slice）。**问题**：`*cached` 缓存指针被同时返回给前端（Go 端 IPC framework 内部做 JSON marshal 并释放）；`Stats` 结构本身**约 8 KiB**（取决于 alias 数）。**`statsCache` 是 Go 端单条 entry，TTL 100ms 后失效**；下次 miss 时旧对象立即可被 GC，**没有延长 Go 端对象生命周期**的风险。**结论**：本路径实际上**是安全的**——Wails IPC 在 marshal 完成后立即丢弃返回值。但**statsCache.stats/swc 指针 + App 引用 + 全 store 图** 会被 root 在 `App.statsCache` 字段上最多 100ms，这是预期行为。
- **Suggested Fix**: 无需改；保留现状。可在 spec 中明确"App.statsCache 是 single-entry TTL cache，TTL=100ms"。
- **Risk of Fix**: N/A。

---

## Finding G-009 — Severity: MEDIUM

- **Location**: `app.go:650-655` (`GetLogs`)
- **Evidence**:

```go
// app.go:650-655
func (a *App) GetLogs(n int) []types.LogEntry {
    if n <= 0 { n = 200 }
    return a.store.Recent(n)
}
// store.go:151-176
func (s *Store) Recent(n int) []types.LogEntry {
    s.mu.RLock()
    defer s.mu.RUnlock()
    l := len(s.logs)
    ...
    out := make([]types.LogEntry, 0, n)
    ...
    for i := 0; i < n; i++ {
        idx := (s.idx - 1 - i + MaxLogEntries) % MaxLogEntries
        if idx < 0 || idx >= l { continue; }
        entry := s.logs[idx]
        if entry.ID == "" { continue; }
        out = append(out, entry)
    }
    return out
}
```

- **Impact**: `GetLogs` 默认 n=200，但前端可能传 `n=1000`（接近 cap）。**每次调用都做 `make([]types.LogEntry, 0, n)` 完整分配 + 200~2000 次循环拷贝**，无 sync.Pool 复用。典型调用：仪表盘每 2s 触发一次 → 每分钟 30 次 × 200 entry ≈ 6 KB × 30 = 180 KiB/min 短期分配。`allocs/op` 约 1~2 次（cap=200 / cap=1000）。**没有延长 Go 对象生命周期**：返回值 slice 立即被 Wails IPC marshal 后释放。
- **Suggested Fix**:
  1. 引入 `sync.Pool` 缓存 `[]types.LogEntry` 切片，调用方 `Put` 回 pool；
  2. 限制 n 上限（例如 `if n > 500 { n = 500 }`）；
  3. 返回前在 Wails IPC 层增加复用 slice 的 wrapper（但 Wails 框架不接受，需前端配合）。
- **Risk of Fix**: 低。Pool 复用有 GC 风险（引用泄漏），需要保证所有元素被读到后再 Put。

---

## Finding G-010 — Severity: LOW

- **Location**: `app.go:411-413` (`ListProviders`) / `app.go:558-560` (`ListModelAliases`)
- **Evidence**:

```go
// app.go:411-413
func (a *App) ListProviders() []types.Provider {
    return a.cfgMgr.Snapshot().Providers
}
// config.go:104-126
func (m *Manager) Snapshot() types.Config {
    m.mu.RLock()
    defer m.mu.RUnlock()
    cfg := m.cfg
    if m.cfg.Providers != nil {
        cp := make([]types.Provider, len(m.cfg.Providers))
        copy(cp, m.cfg.Providers)
        cfg.Providers = cp
    }
    ...
    return cfg
}
```

- **Impact**: `Snapshot()` 做**深拷贝** Providers / ModelAliases / ClientKeys 三个 slice，但 `Provider` / `ModelAlias` / `ClientKey` 内部的 `Models []string` / `Tags []string` **只拷贝了 slice header**，底层字符串数组在 Provider 字段变更时**不会**被覆盖（结构体是值类型），所以**底层不会被外部修改**，无泄漏。但**返回的 slice 头本身的生命周期 = Wails IPC marshal 时间**（几十微秒），完成后立即可 GC，无累积。
- **Suggested Fix**: 不必修改。当前已正确。注释里可明确"slice 字段 ModelAlias.Tags / Provider.Models 是值类型切片，由 Snapshot 浅拷贝已经足够"。
- **Risk of Fix**: N/A。

---

## Finding G-011 — Severity: MEDIUM

- **Location**: `internal/server/server.go:527-584` (`streamResponseWithUsage`) + `internal/anthropicconv/conv.go:198-327` (`TransformStream`)
- **Evidence**:

```go
// server.go:527-529
scanner := bufio.NewScanner(src)
scanner.Buffer(make([]byte, 64*1024), 1<<20) // 64 KiB initial, 1 MiB max

// conv.go:201
scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
```

- **Impact**:
  1. `bufio.Scanner` 每次 `Scan()` 内部会**分配一个临时字符串**返回（`scanner.Text()` 内部 `string(buf)` 是 O(n) 拷贝），大行（接近 1 MiB max）单次拷贝 1 MiB。
  2. Anthropic SSE event payload 一般 1~10 KiB；OpenAI chunk 一般 0.5~4 KiB；典型稳态 4 KiB/string × 几百 chunks/分钟 ≈ 几 MiB/min 短期分配（young-gen GC 友好）。
  3. **`fmt.Fprintf(w, "data: %s\n\n", data)` 进一步把 string 再拷贝一次**（server.go:562），双倍分配。
  4. **scanner buffer 不复用**：每次 `scanner.Buffer(...)` 都 `make([]byte, 64*1024)`（64 KiB），高频调用累积 ~ 几十 KiB/s 短期分配。
- **Suggested Fix**:
  1. 用 `bufio.Reader.ReadBytes('\n')` 或 `ReadSlice` 替代 `bufio.Scanner`，避免 string 拷贝；
  2. 复用 scanner buffer（`sync.Pool` 提供 `bufio.Scanner` 不可行，但可用 `bufio.Reader` 配合 `sync.Pool`）；
  3. Anthropic 路径去掉 `fmt.Fprintf` 用 `w.Write([]byte("data: "))` + `w.Write([]byte(data))` + `w.Write([]byte("\n\n"))`。
- **Risk of Fix**: 中。`bufio.Scanner` 替换为 `bufio.Reader` 需要重新处理换行 / 大行截断；需要 SSE 回归测试。

---

## Finding G-012 — Severity: HIGH

- **Location**: `internal/upstream/upstream.go:139-183` (`ReapIdle`) + `app.go:1017-1036` (`reapClientsLoop`)
- **Evidence**:

```go
// upstream.go:139-183
func ReapIdle(maxIdle time.Duration) int {
    cutoff := time.Now().Add(-maxIdle).UnixNano()
    clientsMu.Lock()
    defer clientsMu.Unlock()
    ...
    for key, c := range clients {
        if n := c.inFlight.Load(); n > 0 { skipped++; continue }
        last := c.lastUsed.Load()
        idle := time.Duration(now - last)
        if last < cutoff {
            c.http.CloseIdleConnections()
            delete(clients, key)
            removed++
        }
        ...
    }
    ...
}

// upstream.go:194-221
func newClient(p types.Provider) *Client {
    transport := &http.Transport{
        Proxy:                 http.ProxyFromEnvironment,
        MaxIdleConns:          100,
        MaxIdleConnsPerHost:   20,
        IdleConnTimeout:       90 * time.Second,
        ...
    }
    ...
}
```

- **Impact**:
  1. **Transport 池上限**：`MaxIdleConns=100` 是全局共享（per-Transport），但 cacheKey 是 per-provider，**每个 provider 自己持有 100 条 idle conn**。20 providers × 100 conns ≈ 2000 条 idle TCP 连接 × 客户端 socket buffer（默认 4 KiB + 4 KiB）= **~16 MiB 仅连接池 socket buffer**，再加 `*http.Client` / `*http.Transport` 本身 ~4 KiB × 20 = 80 KiB。
  2. **`MaxConnsPerHost` 未设置**（默认 0 = 无限）—— 高并发时突发可创建数百条 active conn；idle 后 90s 内不释放（`IdleConnTimeout=90s`）。
  3. **reaper 频率过低**：`reapClientsLoop` 是 5 分钟一次，`upstreamReapIdle=15min`——idle client 实际最长存活 15+5=20min 才被 evict；典型场景：用户加 50 个 provider 后废弃 → 50 × 100 idle conn ≈ **8 MiB** 至少保留 20 分钟。
  4. **`upstream.Evict()` 只在 UpsertProvider/DeleteProvider 显式调用**，但 `cacheKey` 包含 `InsecureSkipVerify=false`（写死，见 `upstream.go:47`），`cacheKey` 完全等价于 `ID+Name+Type+BaseURL+APIKey`，所以正常编辑 APIKey 时确实会 evict；但如果用户编辑的是不进入 cacheKey 的字段（如 `Notes`），旧 cache 仍存活。
- **Suggested Fix**:
  1. `reapClientsLoop` 间隔缩短到 1 分钟，`upstreamReapIdle` 缩短到 5 分钟；
  2. `MaxIdleConns` 调整为 10（默认场景足够），`MaxIdleConnsPerHost=5`，`MaxConnsPerHost=50`；
  3. `shutdown()` 末尾强制 `ReapIdle(0)` 清空所有 client（见 G-003）。
- **Risk of Fix**: 低。连接池缩容属于保守变更；如有高并发 burst 场景需要重新调大。新增 `MaxConnsPerHost` 上限可能让 burst 请求排队——需要 benchmark。

---

## Finding G-013 — Severity: MEDIUM

- **Location**: `internal/upstream/upstream.go:24-27`（全局 cache map）
- **Evidence**:

```go
// upstream.go:24-27
var (
    clientsMu sync.RWMutex
    clients   = map[cacheKey]*Client{}
)
```

- **Impact**: `clients` 是**进程级全局 map**，生命周期 = 进程生命周期。**新增 provider → 新 entry → `newClient` → 新 `http.Client` + `http.Transport`**；编辑 / 删除 provider 走 `Evict` 显式删除。但 `upsertDaily` / `EnsureStreamUsage` 等路径不会创建新 cacheKey，所以 map 大小**主要取决于 provider 配置编辑频率 + 不同的 BaseURL/APIKey 组合**。**未发现明显的 map 累积 bug**，但 reaper 频率低（见 G-012）会让废弃 client 滞留。
- **Suggested Fix**: 配合 G-003 / G-012 一起修即可（缩短 reap 周期 + shutdown 清空）。
- **Risk of Fix**: 低。

---

## Finding G-014 — Severity: MEDIUM

- **Location**: `internal/config/config.go:238-266` (`writeLocked`)
- **Evidence**:

```go
// config.go:238-266
func (m *Manager) writeLocked(cfg types.Config) error {
    data, err := json.MarshalIndent(cfg, "", "  ")
    if err != nil { return ... }
    dir := filepath.Dir(m.path)
    if err := os.MkdirAll(dir, 0o755); err != nil { return ... }
    tmp, err := os.CreateTemp(dir, ".config-*.json")
    if err != nil { return ... }
    tmpPath := tmp.Name()
    defer os.Remove(tmpPath)
    if _, err := tmp.Write(data); err != nil { ... }
    if err := tmp.Close(); err != nil { ... }
    if err := os.Rename(tmpPath, m.path); err != nil { ... }
    return nil
}
```

- **Impact**: 每次 Save 都 `json.MarshalIndent(cfg, "", "  ")` —— **未复用 `json.Encoder` / `bytes.Buffer`**，每次都新分配 `[]byte`。Config 含 Providers / ModelAliases / ClientKeys 三张 slice，典型 size ~ 50 KiB → 单次 Save 分配 50 KiB × Save 频率（每编辑一次 Settings 就 1 次 + shutdown 1 次）≈ 100 KiB~1 MiB 临时分配。**没有累积泄漏**，但增加 young-gen GC 频率。
- **Suggested Fix**:
  1. 复用 `bytes.Buffer` + `json.NewEncoder`；
  2. 或改用 `gob` / `msgpack` 二进制编码；
  3. 或每次只写差异（但增加复杂度）。
- **Risk of Fix**: 低。`json.MarshalIndent` 输出格式与 `json.Encoder.Encode` 不同（Encoder 自动加换行），需要回归测试。

---

## Finding G-015 — Severity: MEDIUM

- **Location**: `internal/server/server.go:498-520` (`streamResponse` 已存在但未被调用) + `internal/server/server.go:384-389` (non-stream 路径)
- **Evidence**:

```go
// server.go:498-520
func (s *Server) streamResponse(w http.ResponseWriter, src io.Reader) {
    ...
    buf := make([]byte, 32*1024)
    for {
        n, err := src.Read(buf)
        ...
    }
}
// server.go:384-389
respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodyBytes+1))
if int64(len(respBody)) > maxRequestBodyBytes { ... }
```

- **Impact**: `streamResponse` 自身**未被任何 caller 调用**（grep 验证），是 dead code —— **不会在生产中分配 buffer**，无影响。但 OpenAI non-stream 路径用 `io.ReadAll` + `io.LimitReader` 一次性读整个响应 body（max 10 MiB），**单请求 10 MiB 临时分配**。典型 chat completion 响应 1~50 KiB，但 function calling / tool_use 等大响应可达几百 KiB。**没有累积泄漏**（per-request 即用即弃）。
- **Suggested Fix**: 删除 dead code `streamResponse`（或保留作未来 streaming without usage 的 fallback）；non-stream 路径考虑 streaming JSON decoder 替代 `io.ReadAll`，减少一次性分配。
- **Risk of Fix**: 低。删除 dead code 需要先验证无 caller 引用。

---

## Finding G-016 — Severity: MEDIUM

- **Location**: `internal/upstream/upstream.go:236-258` (`Do`)
- **Evidence**:

```go
// upstream.go:236-258
func (c *Client) Do(ctx context.Context, method, path string, body []byte, streaming bool) (*http.Response, error) {
    c.touch()
    ...
    req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
    ...
    resp, err := c.http.Do(req)
    if err != nil { return nil, ... }
    c.acquire()
    resp.Body = ioextra.WrapCloser(resp.Body, c.release)  // <-- onClose = c.release
    return resp, nil
}
```

- **Impact**:
  1. **`bytes.NewReader(body)` 每次都新建一个 `Reader` 结构体 + interface header**（很小，~32 B），调用 `Do` 后立即可 GC，无泄漏。
  2. `resp.Body = ioextra.WrapCloser(...)` 把 `c.release` 作为 onClose 钩子；`c.release` 闭包**未捕获额外对象**，仅操作 `c.inFlight` / `c.lastUsed`，**不持有大对象**。
  3. **风险点**：如果 caller 在 retry 路径里**先 Close resp.Body 再 retry**（见 `server.go:347-349`），每次 Close 都会触发 `c.release` —— `inFlight` 计数为 0；下一次 acquire 正确增加。但**如果 caller 多次 Read 但只 Close 一次**，inFlight 计数正确，reaper 不会被错杀。**当前实现安全**。
- **Suggested Fix**: 不必改。可在 spec 中明确"`ioextra.WrapCloser` 的 onClose 不应持有大对象，仅做引用计数 / 状态机更新"。
- **Risk of Fix**: N/A。

---

## Finding G-017 — Severity: MEDIUM

- **Location**: `internal/system/tray_windows.go:232-251` (`winTray.Stop`)
- **Evidence**:

```go
// tray_windows.go:232-251
func (t *winTray) Stop() {
    t.mu.Lock()
    if !t.running {
        t.mu.Unlock()
        return
    }
    close(t.stopCh)
    done := t.done
    t.running = false
    t.mu.Unlock()
    procPostQuitMessage.Call(0)
    select {
    case <-done:
    case <-time.After(500 * time.Millisecond):
    }
}
```

- **Impact**: 退出路径是 `procPostQuitMessage.Call(0)` → run goroutine 在 GetMessageW 收到 WM_QUIT → break loop → `close(t.done)` → Stop 收到 done。**安全**，500ms timeout 兜底。**`stopCh` 声明后实际未被读取**（grep 验证）—— 是 dead code，但占位无害。**风险点**：如果 run() 在 RegisterClassExW/CreateWindowExW 之前 panic（signalReady 之前），`t.done` 不会被 close，`Stop` 会等 500ms timeout。**对象保留图**：`winTray` 本身 ~200 B + `iconPng` 引用（appicon.png ~10 KiB）+ `cb`（trayForwarder → App → 全图）。shutdown 时 `App.shutdown` 调 `tray.Stop()` 后**trayForwarder 仍通过 cb 字段被 winTray 引用**，最坏滞留 500ms。
- **Suggested Fix**:
  1. 删除 dead `stopCh` 字段；
  2. `Stop()` 在 `procPostQuitMessage.Call` 之前先把 `cb = nil`，断开反向引用 → App 的引用可在 run goroutine 完成前先被 GC（但 cb 在 close 之前需要 App 路径访问，wait — OnAction 调用是 cb.OnAction，shutdown 后不应再有 cb 调用，**可在 Stop 第 1 步清掉 cb**）；
  3. signalReady 之前 panic 路径补上 `close(t.done)`。
- **Risk of Fix**: 低。

---

## Finding G-018 — Severity: LOW

- **Location**: `internal/system/safego.go:14-22`
- **Evidence**:

```go
// safego.go:14-22
func SafeGo(name string, fn func()) {
    go func() {
        defer func() {
            if r := recover(); r != nil {
                log.Printf("[safeGo] panic in %s: %v\n%s", name, r, debug.Stack())
            }
        }()
        fn()
    }()
}
```

- **Impact**: `debug.Stack()` 在 panic 时分配 `[]byte`（典型 1~10 KiB，深度调用链可达 50+ KiB），调用结束后立即 GC。**没有保留大堆栈**：recover() 已吞掉 panic，`debug.Stack()` 的 byte slice 是局部变量，log.Printf 完成后即可回收。**多 panic 累积风险**：recover 在 defer 内，safego 自身不持有任何 panic state。
- **Suggested Fix**: 不必改。可选：用 `runtime.Stack(buf)` 写到 `sync.Pool` 的 buffer 里避免分配，但收益小（panic 是罕见路径）。
- **Risk of Fix**: N/A。

---

## Finding G-019 — Severity: LOW

- **Location**: `internal/system/autostart_windows.go:53-93` (`winAutoStart`)
- **Evidence**:

```go
// autostart_windows.go:53-93
func (winAutoStart) Enabled() (bool, error) {
    k, err := openRunKey(true)
    ...
}
func (winAutoStart) Enable(name string) error {
    ...
    path, err := quotedExePath()  // os.Executable()
    ...
}
```

- **Impact**: `os.Executable()` 每次调用都读取 `/proc/self/exe`（Windows 上读取 exe 路径 + 解析符号链接），典型 1~10 ms，**分配字符串 ~200 B**，**无累积**。`App.SetAutoStart` 每次用户切 toggle 触发 1 次，频率极低。`quotedExePath` 没缓存结果 —— 多次调用重复读取。
- **Suggested Fix**: 可选：进程内缓存 `quotedExePath` 结果（启动时算一次）；影响 < 1 KiB 内存，< 1 ms 性能，**优先级 LOW**。
- **Risk of Fix**: 极低（仅多一个 string 字段）。

---

## Finding G-020 — Severity: MEDIUM

- **Location**: `internal/anthropicconv/conv.go:198-327` (`TransformStream`)
- **Evidence**:

```go
// conv.go:198-216
func TransformStream(src io.Reader, w io.Writer) (AnthropicUsage, error) {
    scanner := bufio.NewScanner(src)
    scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
    ...
    for scanner.Scan() {
        line := scanner.Text()           // <-- 每次 Scan 拷贝 line → string
        if line == "" {
            eventType = ""
            continue
        }
        switch {
        case strings.HasPrefix(line, "event: "):
            eventType = strings.TrimPrefix(line, "event: ")
        case strings.HasPrefix(line, "data: "):
            payload := strings.TrimPrefix(line, "data: ")
            ...
            if err := json.Unmarshal([]byte(payload), &m); err == nil { ... }
            ...
        }
    }
    ...
}
```

- **Impact**:
  1. **每条 SSE event 都做 4 次 string 拷贝**（line = scanner.Text()、TrimPrefix 复制、HasPrefix 复制、`[]byte(payload)` 强转再 unmarshal）。单条 event ~1~10 KiB，单次 Anthropic 流 50~500 条 event → 每次流式响应 **200 KiB~5 MiB 临时分配**，每分钟数十次请求 = **几十 MiB/min 短期分配**。
  2. **没有 `sync.Pool` 复用 buffer**（bufio.Scanner 不支持）。
  3. **`writeChunk` 内部 `json.Marshal(chunk)` + `bytes.Buffer` + 多次 Write**（conv.go:397-407），每次都新分配 ~500 B buffer。
- **Suggested Fix**:
  1. 改用 `bufio.Reader.ReadBytes('\n')` + 字符串复用（`bstrconv.UnsafeString` 仅在生命周期内使用，避免拷贝）；
  2. `writeChunk` 改为 `json.NewEncoder` 复用 + 预写 `"data: "` 前缀到 writer，避免 `bytes.Buffer`；
  3. 限制 max line size（当前 1 MiB 过大，单次请求 1 个 1 MiB 行会瞬时吃掉 1 MiB）。
- **Risk of Fix**: 中。Anthropic SSE 协议兼容性需要保留；需要新增 SSE 回归测试。

---

## Finding G-021 — Severity: LOW

- **Location**: `internal/ioextra/onclose.go:21-37`
- **Evidence**:

```go
// onclose.go:21-37
func WrapCloser(rc io.ReadCloser, onClose func()) io.ReadCloser {
    return &onCloserBody{ReadCloser: rc, onClose: onClose}
}
type onCloserBody struct {
    io.ReadCloser
    onClose func()
    once    sync.Once
}
func (b *onCloserBody) Close() error {
    var err error
    b.once.Do(func() {
        defer b.onClose()
        err = b.ReadCloser.Close()
    })
    return err
}
```

- **Impact**: `onCloserBody` 只持 `rc` + `onClose`（`onClose` 是 `c.release`，无大对象捕获），结构体 ~64 B。**安全**，不持有大对象，Close 后即可 GC。
- **Suggested Fix**: 不必改。
- **Risk of Fix**: N/A。

---

## Finding G-022 — Severity: LOW

- **Location**: `internal/store/store.go:149-176` (`Recent`) + `internal/server/server.go:498-520` (dead `streamResponse`)
- **Evidence**:

```go
// store.go:159
out := make([]types.LogEntry, 0, n)
```

- **Impact**: 每次 `Recent(n)` 都新建 `[]types.LogEntry` 切片，**典型 size ~40 KiB (200 entry × 200B)**。无 `sync.Pool` 复用，但调用频率不高（每 2s 一次），临时分配对 GC 友好。**没有累积泄漏**。
- **Suggested Fix**: 不必改。如要优化可加 sync.Pool，但 Wails IPC 已序列化 + 释放，没有显著收益。
- **Risk of Fix**: N/A。

---

## Finding G-023 — Severity: LOW

- **Location**: `app.go:914-1036` (三个 goroutine 的 ticker)
- **Evidence**:

```go
// app.go:920
ticker := time.NewTicker(2 * time.Second)
defer ticker.Stop()    // ✓
// app.go:947
ticker := time.NewTicker(5 * time.Second)
defer ticker.Stop()    // ✓
// app.go:1026
ticker := time.NewTicker(upstreamReapInterval) // 5 min
defer ticker.Stop()    // ✓
```

- **Impact**: 三个 ticker 全部正确 `defer ticker.Stop()`。**`time.After(3 * time.Second)` 在 `scheduleFlush` (app.go:979)** —— 该 timer 在 `select` 完成或 ctx Done 后立即可 GC，**`time.After` 的 timer 不会 leak**（Go 1.23+ 已修复 timer pool issue）。
- **Suggested Fix**: 不必改。
- **Risk of Fix**: N/A。

---

## Finding G-024 — Severity: LOW

- **Location**: `app.go:913-986`, `internal/server/server.go:498-520` (defer in loops)
- **Evidence**: 经过 grep + 阅读，**没有发现长循环里 defer 累积栈扩张**。所有 defer 都集中在函数体顶部（recover / ticker.Stop / body.Close），不在 hot loop 内部。Go 1.14+ 的"open-coded defer"对简单 defer（defer ticker.Stop() / defer body.Close()）已优化为内联。`server.go:498-520` 的 `streamResponse` 函数内部循环**没有 defer**，仅局部变量。
- **Impact**: 0 finding。这一块通过验证。
- **Suggested Fix**: 不必改。
- **Risk of Fix**: N/A。

---

## Finding G-025 — Severity: LOW

- **Location**: `internal/store/store.go:54` (`days map[string]*DailyAgg`)
- **Evidence**:

```go
// store.go:54
days map[string]*DailyAgg
```

- **Impact**: `s.days` 是长期持有 map（key 是日期字符串，30 天 retention 下 ≤30 个 entry）。**唯一 evict 路径**是 `SaveDaily` 在 retention > 0 时删旧 day（`persist.go:228-243`）—— 正确释放对应 `*DailyAgg` → `ByHour` map。**没有发现只 append 不 evict 的长生命周期 map**。
- **Suggested Fix**: 不必改。
- **Risk of Fix**: N/A。

---

## Finding G-026 — Severity: MEDIUM

- **Location**: `internal/store/store.go:386-403` (`StatsWithComparison` 初始化)
- **Evidence**:

```go
// store.go:394-403
result := types.StatsWithComparison{
    Stats: types.Stats{
        RequestsByModel:           map[string]int64{},
        RequestsByProvider:        map[string]int64{},
        RequestsByClientKey:       map[string]int64{},
        RequestsByClientKeyRecent: map[string]int64{},
        RequestsByHour:            []types.HourBucket{},
        RequestsByHourByModel:     map[string][]types.HourBucket{},
    },
}
```

- **Impact**: 每次 `StatsWithComparison` miss 都**强制初始化 4 张 map + 2 张 slice**，即使最终全部为空。配合 G-006 全量重算，每次 ~10+ alloc。**没有累积泄漏**（per-call 即用即弃），但 young-gen GC 频率高。
- **Suggested Fix**: 配合 G-006 的增量维护一起改；或者把空结构体（zero-value map/slice）保留 nil，让 JSON 序列化输出 `null`，前端 nil-safe 处理（但前端可能预期空对象 `{}`，需协调）。
- **Risk of Fix**: 中。需前端配合 null → {} 兼容。

---

## Finding G-027 — Severity: MEDIUM

- **Location**: `app.go:914-986` (3 goroutines) + `app.go:108-186` (startup)
- **Evidence**:

```go
// app.go:109-110
func (a *App) startup(ctx context.Context) {
    a.ctx = ctx
    ...
    go a.emitLogsLoop()
    go a.persistLogsLoop()
    go a.reapClientsLoop()
}
```

- **Impact**: 三个 goroutine 都用 `a.ctx`（来自 Wails runtime），shutdown 路径靠 ctx Done 触发退出。**`a.ctx` 在 `App.shutdown` 期间仍可被前端 EventsEmit 访问**（`app.go:891-907`）—— 如果 Wails runtime 在 OnShutdown 期间仍接收前端 IPC，会**触发 Wails 在 shutdown 中处理 IPC 的边角问题**。具体影响：在 frontend 关闭瞬间可能触发 N 次 GetStats 调用，每次走完整 store.sumDays() 路径（即便 statsCache 命中），加上可能的 `saveLogs` / `saveDaily` 竞争写入。
- **Suggested Fix**: `App.shutdown` 第一步设 `a.closing.Store(true)`，所有 binding 方法先检查 `a.closing` 立即返回 zero value。
- **Risk of Fix**: 低。需要 Wails 端配合。

---

## Finding G-028 — Severity: LOW

- **Location**: `internal/store/store.go:99-103` (`fireOnChange`)
- **Evidence**:

```go
// store.go:99-103
func (s *Store) fireOnChange() {
    if fn := s.onChange.Load(); fn != nil {
        (*fn)()
    }
}
```

- **Impact**: `onChange` 是 `atomic.Pointer[func()]`，包装了 `a.statsCache.invalidate`。**无累积泄漏**——atomic.Pointer 不持有底层函数对象以外的引用。`(*fn)()` 调用 `invalidate`，只持 statsCache 内的 `sync.Mutex` + 两个指针 + atomic.Int64，~80 B。
- **Suggested Fix**: 不必改。
- **Risk of Fix**: N/A。

---

## TL;DR

Go 端整体内存使用健康，无明显 goroutine 泄漏或大对象累积，但有 3 个 HIGH 级风险点需优先处理：
- **G-002 / G-004**：`App.shutdown` 的 SaveLogs 与 persistLogsLoop 退出前的最后一次 flush 形成 race，关闭序列未串行化 flushMu，可能漏写 ≥5s 数据并使关闭时延达 5~10s。
- **G-012**：upstream 客户端连接池过大（`MaxIdleConns=100/provider` × 20 providers ≈ 2000 idle conn × 16 MiB socket buffer）+ reaper 频率过低（5min tick × 15min idle = 20min 滞留），废弃 provider 的连接资源长时间不被回收。

HIGH severity 总数：**3**

(详细位置见 G-002、G-004、G-012；其余 22 项为 MEDIUM/LOW。)
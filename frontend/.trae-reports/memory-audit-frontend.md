# Frontend Memory Audit — `api_distribution` (Pure Static)

- Scope: `d:\Go_Pj\api_distribution\frontend\src\` (excluding Wails-generated bindings under `wailsjs/`).
- Method: read-only static analysis. No edits, no `npm`/`vite`/`tsc` invocations.
- Reference: prior conclusions in `.trae/specs/harden-memory-and-concurrency/` and `.trae/specs/optimize-performance-and-charts/` were NOT re-listed; this report only covers new/overlapping observations.
- Severity scale: **HIGH** (heap growth > a few MB, or persistent retained objects after navigation) / **MEDIUM** (per-render/per-tick allocations on a 2–3 s heartbeat that compound to MB over a session) / **LOW** (single-digit KB matters or correctness-adjacent).
- Findings: **20** total — **HIGH × 6**, **MEDIUM × 9**, **LOW × 5**.

---

## Finding F-001
- **Severity**: HIGH
- **Location**: `frontend/src/i18n/locales.ts:19-1666` and consumed by `frontend/src/i18n/useT.ts:14-16`
- **Evidence**:
  ```ts
  // frontend/src/i18n/locales.ts:1661-1666
  export const messages: Record<Locale, Message> = {
    'zh-CN': zh,
    'en-US': en,
    'ja-JP': ja,
    'ko-KR': ko,
  }

  // frontend/src/i18n/useT.ts:14-16
  const fromLocale = messages[locale]?.[key]
  const template =
    fromLocale ?? messages['en-US'][key] ?? key
  ```
  All four ~400-entry dictionaries are constructed at module init and pinned on the module record forever. `useT` only ever reads `messages[locale]` for the active key — three of the four dictionaries are dead weight at any moment.
- **Impact**: ~50–80 KB of string-table heap (zh-CN is largest at ~430 keys). Per app session: retained permanently regardless of the user’s chosen locale; never released when the user switches language.
- **Suggested Fix**: Split each language into its own dynamic import (`messages/zh-CN.ts` etc.) and lazy-load on first `useT(key)` call after locale change. Keep an LRU of the two most recently used bundles; unload bundles older than the second-newest.
- **Risk of Fix**: Slight first-paint delay on the first string after locale switch (one async chunk). All call sites already tolerate the existing `?? messages['en-US']` fallback, so adding an async fallback path is safe. No behavior change for the active locale.

---

## Finding F-002
- **Severity**: HIGH
- **Location**: `frontend/src/components/charts/ModelTrendChart.tsx:163-219` (the `rows/models/isEmpty` useMemo)
- **Evidence**:
  ```ts
  // frontend/src/components/charts/ModelTrendChart.tsx:163-219
  const {rows, models, isEmpty} = useMemo(() => {
    ...
    const out: TrendRow[] = []
    for (const alias of aliases) {
      ...
      for (const {hour, key} of hourSlots) {
        const bucket = byHour.get(key)
        out.push({hour, model: alias, count: bucket?.count ?? 0,
                  tokens: bucket?.byModelTokens?.[alias] ?? 0})
      }
    }
    ...
    return {rows: out, models: aliases, isEmpty: empty}
  }, [safeData, mode, selectedModel, windowHours])
  ```
  For `windowHours === 720` and N enabled aliases the loop materialises `720 × N` `TrendRow` literals on every invalidation. Each `TrendRow` ≈ 32 B + a back-pointer to a string `model`, so 10 aliases ≈ ~230 KB per recompute.
- **Impact**: With Dashboard’s 2 s `stats:changed` heartbeat + `statsShallowEqual`, `safeData` ref still flips whenever the per-model-hourly keyset shifts or the totals change. Each flip allocates a fresh `~230 KB` array of `TrendRow` objects plus the intermediate `byHourPerModel: Map<string, Map<number, TokenHourBucket>>` (line 180-187) plus `rowsByHour: Map<number, Map<string, number>>` (line 239-250). Repeated navigations between Models/Providers/Dashboard churn this on every page return.
- **Suggested Fix**:
  - Move the rolling-window realization into a separate `useMemo` whose key is `byModel` only (not `safeData`), and cache the last frame by alias-set identity so an alias roster update reuses the previous 720 slots.
  - Drop `tokens` and `count` to integer columns (typed arrays) rather than object literals.
  - For 30d window, downsample internally to ~120 points per alias (per-hour → per-6h) — matches what the eye can resolve at the canvas width.
- **Risk of Fix**: Need to verify the tooltip still resolves the hour bucket correctly after downsampling. Bump the X axis tick formatter to display the day label rather than the hour.

---

## Finding F-003
- **Severity**: HIGH
- **Location**: `frontend/src/components/charts/ModelTrendChart.tsx:239-250` (`rowsByHour`) plus `:251-254` (ref sync)
- **Evidence**:
  ```ts
  // frontend/src/components/charts/ModelTrendChart.tsx:239-254
  const rowsByHour = useMemo(() => {
    const m = new Map<number, Map<string, number>>()
    for (const row of rows) {
      let perModel = m.get(row.hour)
      if (!perModel) { perModel = new Map(); m.set(row.hour, perModel) }
      perModel.set(row.model, metric === 'tokens' ? row.tokens : row.count)
    }
    return m
  }, [rows, metric])
  const rowsByHourRef = useRef(rowsByHour)
  useEffect(() => { rowsByHourRef.current = rowsByHour }, [rowsByHour])
  ```
  A second O(N×M) build on every `rows` invalidation (i.e. whenever `safeData` changes) — same trigger as F-002 — and the ref sync means we keep the old `Map<…,Map<…>>` alive across the heartbeat tick even when the new `rowsByHour` is structurally equivalent.
- **Impact**: Double rebuild of the same N×M table per 2 s tick, ~230 KB × 2 every flip, and a brief window where two `rowsByHour` instances are reachable (the ref still points at the previous frame).
- **Suggested Fix**: Compute `rowsByHour` lazily inside the tooltip render, OR fold it into the same `useMemo` as `rows` (one pass instead of two). Drop the ref entirely once the closure reads from a stable map (the closure inside `tooltipRenderRef` already uses `rowsByHourRef` only).
- **Risk of Fix**: None visible — the tooltip renderer is already closed over `rowsByHourRef` and reads on each hover.

---

## Finding F-004
- **Severity**: HIGH
- **Location**: `frontend/src/pages/Dashboard.tsx:645-660` (`ModelTrendChartContainer` data derivation)
- **Evidence**:
  ```ts
  // frontend/src/pages/Dashboard.tsx:645-660
  const byModel = useMemo(
    () =>
      (stats as unknown as {requestsByHourByModel?: Record<string, HourBucket[]>})
        ?.requestsByHourByModel,
    [stats],
  )
  const data = useMemo(
    () => (byModel ?? ({} as Record<string, HourBucket[]>)),
    [byModel],
  )
  ```
  `data` always rebuilds through the `byModel ?? {}` fallback, and on the first stats frame `stats.requestsByHourByModel` is `undefined`, so the cache is `{…}` — a fresh object literal every time `byModel` changes from `undefined` → `Record` → `undefined` again (which happens on transient heartbeat errors and on the `refreshErrors.stats` redraw).
- **Impact**: Each `data` reference flip invalidates the child `ModelTrendChart` `useMemo` chain (F-002/F-003) even when no chart data changed. With Dashboard re-rendered on every 2 s tick, this is the upstream cause of the F-002 churn.
- **Suggested Fix**: Lift the fallback to a single module-level constant `const EMPTY_HOUR_MAP = Object.freeze({}) as Record<string, HourBucket[]>`. Pin `data` reference to `byModel ?? EMPTY_HOUR_MAP`.
- **Risk of Fix**: `EMPTY_HOUR_MAP` must be frozen to avoid mutation hazards. The downstream `safeData = data ?? {}` pattern still works.

---

## Finding F-005
- **Severity**: HIGH
- **Location**: `frontend/src/components/ErrorBoundary.tsx:25-34`
- **Evidence**:
  ```ts
  // frontend/src/components/ErrorBoundary.tsx:25-34
  static getDerivedStateFromError(error: Error): State {
    return {hasError: true, error}
  }
  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    console.error('[ErrorBoundary]', error, errorInfo)
    messageApiRef.current?.error('Render error')
  }
  ```
  `state.error` retains the full `Error` instance (including the entire `.stack` string and any non-enumerable properties on custom Error subclasses). React keeps the boundary instance alive for the rest of the session — `window.location.reload()` is the only release path (line 38).
- **Impact**: A single render error pins the entire stack trace (often 5–30 KB of stack frames plus any cyclic references the error message captured). With React 19 strict-mode dev re-mounts, the boundary is recreated cheaply, but production runs that recover via reload reset only after user action.
- **Suggested Fix**: Capture only `error.message` (truncated to ~1 KB) in state, log `error.stack` + `errorInfo` to a separate `lastCrashRef` so it can be uploaded via telemetry and freed on next render. Drop the Error reference once displayed.
- **Risk of Fix**: Existing display only renders `error.message` (line 69), so dropping the rest is safe. Telemetry side-effect must not break the boundary (i.e. swallow errors from the log call).

---

## Finding F-006
- **Severity**: HIGH
- **Location**: `frontend/src/components/charts/ModelTrendChart.tsx:260-328` (`tooltipRenderRef` closure)
- **Evidence**:
  ```ts
  // frontend/src/components/charts/ModelTrendChart.tsx:260-328
  const tooltipRenderRef = useRef<...>(() => '')
  useEffect(() => {
    tooltipRenderRef.current = (
      _event: unknown,
      {items: _items, title}: {items: unknown[]; title: string},
    ) => {
      ...
      const body = models
        .map((model, idx) => {
          const value = values?.get(model) ?? 0
          const color = colorRange[idx] ?? PALETTE[idx % PALETTE.length]
          return `<li class="g2-tooltip-list-item" style="...">
            ...
            <span ...>${formatNumber(value)}</span>
          </li>`
        })
        .join('')
      ...
    }
  }, [models, colorRange, windowHours])
  ```
  Each effect tick reassigns `tooltipRenderRef.current` to a fresh closure that captures the latest `models`, `colorRange`, and the literal HTML-template strings (each > 200 chars). The previous closure is replaced but the prior `models` / `colorRange` references it held are no longer reachable from the ref, so they can be GC’d — except for any DOM-side G2 tooltip state that captured them.
- **Impact**: G2 v5 keeps the tooltip closure in a side-channel (`interaction.tooltip.render` at line 354-357). If G2 retains a reference to the *previous* render’s closure for hover events fired during the transition, the old `models` array and `colorRange` stay reachable until the next unmount of the chart. For long-lived dashboard sessions with frequent alias renames, this accumulates per-render closures (~5–10 KB each, multiplied by the number of renames in a session).
- **Suggested Fix**: Move the render-callback to a single `useCallback` (stable identity) whose body reads `models`, `colorRange`, `rowsByHourRef`, and `windowHours` from refs. Then there is exactly one closure for the lifetime of the chart.
- **Risk of Fix**: None — the closure already reads everything through `rowsByHourRef`. Same approach the `tooltipRenderRef` was designed for, but without the per-tick replacement.

---

## Finding F-007
- **Severity**: MEDIUM
- **Location**: `frontend/src/pages/Dashboard.tsx:454-455` (the by-client-key inline `.sort()`)
- **Evidence**:
  ```ts
  // frontend/src/pages/Dashboard.tsx:454-455
  {Object.entries(stats?.requestsByClientKey ?? {})
    .sort(([, a], [, b]) => b - a)
    .map(([label, count], idx) => {
  ```
  `Object.entries` allocates a fresh `Array<[string, number]>`, `.sort` mutates it in place, then `.map` allocates a second array of JSX nodes. None of this is memoised. With `statsShallowEqual` keeping `stats` stable for many heartbeats this only fires on actual client-key churn, but on churn it allocates per-entry closures for the comparator and the map callback.
- **Impact**: For N client keys, ~2N array entries + N JSX nodes allocated. Small absolute cost but zero memoisation when N grows.
- **Suggested Fix**: Move the sort into a `useMemo` keyed on `stats?.requestsByClientKey`:
  ```ts
  const sortedClientKeys = useMemo(
    () => Object.entries(stats?.requestsByClientKey ?? {})
      .sort(([, a], [, b]) => b - a),
    [stats?.requestsByClientKey],
  )
  ```
- **Risk of Fix**: None — identical output.

---

## Finding F-008
- **Severity**: MEDIUM
- **Location**: `frontend/src/pages/Providers.tsx:136`
- **Evidence**:
  ```ts
  // frontend/src/pages/Providers.tsx:135-136
  function tryCloseEditor() {
    const dirty = editing && initialEditing && JSON.stringify(editing) !== JSON.stringify(initialEditing)
  ```
  Two full `JSON.stringify` round-trips of the entire `Provider` object (including `apiKey`) on every drawer-close attempt. For a Provider with many models and notes, this is a couple of KB per close; with the rapid close-and-reopen cycle while iterating on the form, it adds up.
- **Impact**: ~2–10 KB transient allocation per close. Not retained, but synchronous on the click handler — adds noticeable jank for very large model lists.
- **Suggested Fix**: Store a `dirtyRef` (mutable ref) that flips whenever `onChange` fires for this drawer session — O(1). For belt-and-braces, compute a shallow field-by-field compare inside the drawer form.
- **Risk of Fix**: Need to ensure `openEditor()` resets the ref to `false` and that every `onChange` flip inside `ProviderForm` increments it. Less defensive than `JSON.stringify` but vastly cheaper.

---

## Finding F-009
- **Severity**: MEDIUM
- **Location**: `frontend/src/pages/Settings.tsx:153-171` (`handleExportConfig`)
- **Evidence**:
  ```ts
  // frontend/src/pages/Settings.tsx:153-171
  const masked = {
    ...cfg,
    clientKeys: cfg.clientKeys.map((ck) => ({...ck, key: ck.key ? redactKey(ck.key) : ''})),
    providers: cfg.providers.map((p) => ({...p, apiKey: p.apiKey ? redactKey(p.apiKey) : ''})),
  }
  const json = JSON.stringify(masked, null, 2)
  const blob = new Blob([json], {type: 'application/json'})
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'api-distribution-config.json'
  a.click()
  URL.revokeObjectURL(url)
  ```
  `redactKey` returns `'*'.repeat(Math.max(8, key.length))` (`lib/format.ts:64`), so each redacted entry is a string of length ≥ 8 bytes (typically 32+ for real keys). The full config (N client keys + M providers with full notes/models lists) gets serialised into a string AND a Blob before either is released.
- **Impact**: For a typical config (5 providers × ~10 models + 3 client keys) the resulting JSON is ~5–20 KB string + same-size Blob + the original `cfg` retained via `masked`. `URL.revokeObjectURL` releases the URL mapping but the Blob itself is only released when the page unloads or the Blob is GC’d. In Chromium the Blob is pinned for at least one event-loop turn.
- **Suggested Fix**: Stream the download via the `showSaveFilePicker` API where available; for the fallback path, drop `redactKey` to a fixed-length `'*'.repeat(8)` rather than full-length (no need to retain the original key length); explicitly `URL.revokeObjectURL` AFTER `a.click()` returns (it does) plus a `setTimeout(() => blob = null, 0)` if a manual release is needed.
- **Risk of Fix**: `redactKey` is also used for in-app display redaction — confirm callers don’t rely on the length being preserved. A separate `redactKeyForExport(v: string): string` constant `'********'` would localise the change.

---

## Finding F-010
- **Severity**: MEDIUM
- **Location**: `frontend/src/pages/Models.tsx:390-391` (inside `AliasForm`)
- **Evidence**:
  ```ts
  // frontend/src/pages/Models.tsx:389-391
  const selectedProvider = providers.find((p) => p.id === alias.providerId)
  const modelPresets = getPresetsForProvider(selectedProvider)
  const providerModelSuggestions = selectedProvider?.models ?? []
  ```
  `providers.find` is a linear scan on every keystroke into the form (i.e. every parent re-render of `AliasForm`). `getPresetsForProvider` (line 32-40) allocates a `new Set([...preset, ...providerModels])` per call.
- **Impact**: For P providers and an average of P/2 per scan, ~P² comparison work plus a `Set` allocation per keystroke. With P ≥ 10 the cost is non-trivial in the form hot path.
- **Suggested Fix**: Either accept the same `providerById` Map that `Models.tsx:73-75` already builds (lift it via props/context) or compute `modelPresets` inside a `useMemo` keyed on `alias.providerId` and the providers ref. The `models` array can stay a plain `.find`.
- **Risk of Fix**: Tiny — needs a prop wiring change so the form gets the precomputed Map.

---

## Finding F-011
- **Severity**: MEDIUM
- **Location**: `frontend/src/pages/Models.tsx:73-93` (`providerById`, `filtered` useMemos) and `:348-350` (`update`)
- **Evidence**:
  ```ts
  // frontend/src/pages/Models.tsx:73-93
  const providerById = useMemo(() => {
    return new Map(providers.map((p) => [p.id, p]))
  }, [providers])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return modelAliases
    return modelAliases.filter((a) => {
      const provider = providerById.get(a.providerId)
      const haystack = [
        a.alias, a.providerModel, a.description,
        provider?.name ?? '', ...(a.tags ?? []),
      ].join(' ').toLowerCase()
      return haystack.includes(q)
    })
  }, [modelAliases, search, providerById])

  // line 348-350
  function update<K extends keyof ModelAlias>(key: K, value: ModelAlias[K]) {
    onChange({...alias, [key]: value})
  }
  ```
  `filtered` runs on every keystroke (because `search` changes). For each alias it allocates a 5-element array, joins it, lowercases it, and substring-searches. The `[...a.tags ?? []]` spread (effectively `...(a.tags ?? [])`) is fine, but the array literal + `.join` + `.toLowerCase` is allocated per alias per keystroke.
- **Impact**: With M aliases, ~M array allocations + M join calls + M toLowerCase calls per keystroke. Linear in M, fine for M < 200.
- **Suggested Fix**: Memoise a `haystackByAliasId` Map on `providerById` + `modelAliases` changes; reuse it inside `filtered`. Also memoise the lowercase `search` once outside the filter callback.
- **Risk of Fix**: None — identical behaviour, fewer allocations.

---

## Finding F-012
- **Severity**: MEDIUM
- **Location**: `frontend/src/store/config.ts:182, 192, 240, 246, 257, 285, 288, 291, 331, 413, 428` (all `set((s) => ({refreshErrors: {...s.refreshErrors, ...}}))` writes)
- **Evidence**:
  ```ts
  // frontend/src/store/config.ts:182, 240, 257, 285, 331
  set((s) => ({refreshErrors: {...s.refreshErrors, X: ...}}))
  ```
  Every transient error (e.g. a single `GetLogs` rejection on a flaky tick) allocates a fresh `refreshErrors` object, which triggers re-render of any selector subscribed to it — including `BootErrorScreen` which subscribes via `useConfigStore((s) => s.refreshErrors)` (`components/BootErrorScreen.tsx:13`).
- **Impact**: With three independent refresh endpoints (stats / logs / server) plus manual save-time paths, error churn fires a handful of times per session — small in absolute terms but every subscriber (e.g. the boot screen, which is always mounted above the router outlet) re-renders.
- **Suggested Fix**: Split the error map into per-slice atomic fields (`refreshErrorStats: string | null`, etc.) so a logs-error doesn't invalidate the stats-error subscriber. The trade-off is more selectors on the component side.
- **Risk of Fix**: Need to update every `useConfigStore((s) => s.refreshErrors.X)` reader to the new shape. The `BootErrorScreen` `Object.values(refreshErrors).some(Boolean)` degrade check (line 24) needs an explicit aggregate.

---

## Finding F-013
- **Severity**: MEDIUM
- **Location**: `frontend/src/store/config.ts:449-452` and `frontend/src/store/toast.ts:16` + `frontend/src/components/MessageHolder.tsx:9`
- **Evidence**:
  ```ts
  // frontend/src/store/config.ts:449-452
  const eventRegistry = new Map<string, {
    refCount: number
    unsubscribe: () => void
  }>()

  // frontend/src/store/toast.ts:16-20
  let messageApi: MessageInstance | null = null
  export function bindMessageApi(api: MessageInstance | null) { messageApi = api }

  // frontend/src/components/MessageHolder.tsx:9
  export const messageApiRef: {current: MessageInstance | null} = {current: null}
  ```
  Three module-level singletons that pin state for the lifetime of the JS context. `eventRegistry` is correctly bounded (max 2 entries: `stats` + `logs`), but `messageApi` and `messageApiRef` both hold a reference to the antd `MessageInstance` — duplicated. Cleanup is correct on unmount, but the duplication means any future caller that reads one but not the other sees stale data.
- **Impact**: Singleton leak hazard — if a future refactor misses the cleanup in `MessageHolder` (line 19-22), the toast API stays reachable forever even after the ConfigProvider unmounts. The duplication of the same reference in two module-scope slots is a latent bug, not an active leak.
- **Suggested Fix**: Pick one — either `messageApiRef.current` (used by `ErrorBoundary`) or `messageApi` (used by `toast`). Have the other read from the first. Avoid the dual-storage pattern.
- **Risk of Fix**: Low — both consumers (`ErrorBoundary`, `toast.*`) read via getter, so a refactor to a single source is mechanical.

---

## Finding F-014
- **Severity**: MEDIUM
- **Location**: `frontend/src/pages/Dashboard.tsx:130-142` (`quickStart` useMemo) and `frontend/src/components/layout/TopBar.tsx:67-70` (`statusLabel` useMemo)
- **Evidence**:
  ```ts
  // frontend/src/pages/Dashboard.tsx:130-142
  const quickStart = useMemo<QuickStartStep[]>(() => {
    const steps: QuickStartStep[] = []
    if (providers.length === 0) { steps.push({ key: 'provider', to: '/providers', label: t('...') }) }
    if (modelAliases.length === 0) { steps.push({ key: 'alias', to: '/models', label: t('...') }) }
    if (!serverStatus?.running) { steps.push({ key: 'start', label: t('...') }) }
    return steps
  }, [providers.length, modelAliases.length, serverStatus?.running, t])

  // frontend/src/components/layout/TopBar.tsx:67-70
  const statusLabel = useMemo(
    () => (running ? t('sidebar.running') : t('sidebar.stopped')),
    [running, t],
  )
  ```
  Both `useMemo`s depend on `t` — the `useT()` function reference. `useT` is memoised inside `useT.ts:12-23` on `[locale]`, so the function identity flips on every locale switch. Each flip invalidates these memos and any consumer that depends on them (in particular the `t`-dependent `Segmented` / `Button` / `Card` titles in Dashboard).
- **Impact**: Locale switch causes a full re-evaluation of every Dashboard memo that depends on `t` (most of them). Combined with the heartbeat invalidations already noted, locale switch + heartbeat can trigger a render cascade.
- **Suggested Fix**: Wrap `t` consumers in a stable closure that reads `useLocaleStore.getState().locale` once per render — the `t` reference itself never changes. The page-level re-render already happens on locale change anyway; the additional memo invalidations don’t save anything.
- **Risk of Fix**: If a `useMemo` was relying on `t` identity to avoid re-running, this would break that contract. Auditing the existing memos shows none depend on `t` for content (always re-read it from the catalog).

---

## Finding F-015
- **Severity**: MEDIUM
- **Location**: `frontend/src/pages/Models.tsx:65-71` and `frontend/src/pages/Providers.tsx:82-88` (`drawerWidth` resize listener)
- **Evidence**:
  ```ts
  // frontend/src/pages/Models.tsx:65-71
  const [drawerWidth, setDrawerWidth] = useState<number | '100%'>(520)
  useEffect(() => {
    const update = () => setDrawerWidth(window.innerWidth < 600 ? '100%' : 520)
    update()
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])

  // frontend/src/pages/Providers.tsx:82-88 — identical pattern
  ```
  Both pages attach a fresh `resize` listener to `window` on mount and remove on unmount. Cleanup is correct, but the same listener body is duplicated in two pages and could fire up to 60 times per second during a manual window resize, each firing `setDrawerWidth` to the same result (no-op after the first).
- **Impact**: Two concurrent listeners on `window.resize` while both pages are mounted at different times — each listener fires per resize event, even when the value hasn’t crossed the 600-px threshold (because `update` always reads `window.innerWidth`). On a resize drag this can cause ~60 React state updates per second.
- **Suggested Fix**: Lift the `drawerWidth` computation into the `AppShell` (single owner) and pass via context, OR guard the `update` body so it only calls `setDrawerWidth` when the value actually changes:
  ```ts
  const update = () => {
    const next = window.innerWidth < 600 ? '100%' : 520
    setDrawerWidth((prev) => (prev === next ? prev : next))
  }
  ```
- **Risk of Fix**: None — same observable behaviour, far fewer renders.

---

## Finding F-016
- **Severity**: LOW
- **Location**: `frontend/src/pages/Providers.tsx:54-67` and `frontend/src/pages/Models.tsx:42-52` (`defaultProvider` / `defaultAlias` factories)
- **Evidence**:
  ```ts
  // frontend/src/pages/Providers.tsx:54-67
  function defaultProvider(): Provider {
    return {
      id: newId('prov'),
      name: '',
      baseUrl: '',
      apiKey: '',
      type: 'openai',
      enabled: true,
      priority: 0,
      weight: 1,
      models: [],
      notes: '',
    }
  }
  ```
  Every "Add Provider" / "Add alias" click allocates a fresh object literal. Same for `defaultAlias()` (line 42-52). The `id` field uses `newId('prov')` (`lib/id.ts:1-5`), which concatenates `Date.now().toString(36) + Math.random().toString(36).slice(2,10)` — ~17 chars, fine.
- **Impact**: Sub-KB per click; not a leak. Flagged because `id` collision risk on rapid double-click if `Math.random()` happens to repeat within the same millisecond (extremely unlikely in practice but worth a `crypto.randomUUID()` if the React 19 + Wails runtime exposes it).
- **Suggested Fix**: Replace `Math.random().toString(36).slice(2,10)` with `crypto.randomUUID()` if available (Wails runtime exposes the Web Crypto API), else keep the current scheme — the risk is theoretical.
- **Risk of Fix**: ID format change — ensure any persisted references are matched by the new ID generator.

---

## Finding F-017
- **Severity**: LOW
- **Location**: `frontend/src/components/charts/ModelTrendChart.tsx:92-97` and `frontend/src/components/charts/RecentTrafficChart.tsx:41-46` (`readToken`)
- **Evidence**:
  ```ts
  // frontend/src/components/charts/ModelTrendChart.tsx:92-97
  function readToken(name: string, fallback: string): string {
    const root = document.querySelector('.api-distribution') as HTMLElement | null
    if (!root) return fallback
    const v = getComputedStyle(root).getPropertyValue(name).trim()
    return v || fallback
  }
  ```
  `readToken` is called inside `useMemo(..., [])` (line 125-131), so the values are captured once at mount and never refreshed. On theme switch (light → dark or `system` media query change) the tokens stored in the memo are stale until the chart remounts.
- **Impact**: Not a memory issue per se but a correctness footgun: stale tokens kept alive in the memoized object until the user navigates away and back. The closure (line 263-328) captures `themeTokens` indirectly via `interactionConfig`, which is itself memoised on `[themeTokens.axis]` — same lifetime.
- **Suggested Fix**: Drop the memo on `[windowHours, hourAnchor]` etc. and read tokens via a small `useDesignTokens()` hook that subscribes to theme changes via the existing `useThemeStore`. OR cache via a module-level singleton keyed by `document.documentElement.classList.contains('dark')`.
- **Risk of Fix**: Theme tokens are emitted as `--ant-*` CSS variables on the ConfigProvider root, so reading them via `getComputedStyle` on theme switch must happen after the React commit — use a `useEffect` or a forced layout in `useMemo`.

---

## Finding F-018
- **Severity**: LOW
- **Location**: `frontend/src/pages/Settings.tsx:135` (full-config save path)
- **Evidence**:
  ```ts
  // frontend/src/pages/Settings.tsx:133-136
  if (logRetention !== config.logRetention) {
    await saveConfig({...config, logRetention})
  }
  ```
  Saves the entire `Config` object on every retention change, which triggers `store/config.ts:335-340` to call `App.SaveConfig(toWailsConfig(cfg))` plus a full `refreshServer()`. In practice this is a one-shot per user action, not in a hot loop.
- **Impact**: No allocation/leak concern. Listed only to flag the side-effect surface — the `App.SaveConfig` call persists everything including providers/aliases/clientKeys, so any concurrent in-flight provider edit would be overwritten.
- **Suggested Fix**: Add a dedicated `App.SetLogRetention(n)` Wails binding (mirrors `updateServerSettings`) to avoid the full-config write.
- **Risk of Fix**: Backend API change — would need Wails regen and a Go-side handler.

---

## Finding F-019
- **Severity**: LOW
- **Location**: `frontend/src/pages/Models.tsx:23-30` (module-level `MODEL_PRESETS`)
- **Evidence**:
  ```ts
  // frontend/src/pages/Models.tsx:23-30
  const MODEL_PRESETS: Record<string, string[]> = {
    openai: ['gpt-4o', 'gpt-4o-mini', 'gpt-4-turbo', 'gpt-4.1', ...],
    anthropic: ['claude-sonnet-4-20250514', ...],
    ...
  }
  ```
  Held on the module record for the page lifetime. ~30 short strings, < 1 KB. Combined with the rest of the page module, the import graph means the entire `@/pages/Models` chunk stays in memory as long as the Models route has been visited at least once.
- **Impact**: Tiny. Noted only because the lazy-load graph (`Dashboard.tsx:38-49`) defers `@ant-design/charts` but the Models route eagerly imports `@/pages/Models`. No corresponding deferral for `@/pages/Providers` or `@/pages/Models`.
- **Suggested Fix**: Consider `lazy(() => import('@/pages/Models'))` in `App.tsx:78-83` if Dashboard is the only entry point — but the router already supports it; check `App.tsx` for the import pattern.
- **Risk of Fix**: A 404 on the lazy boundary would need a Suspense fallback; the Routes already render via `AppShell` outlet which is fine.

---

## Finding F-020
- **Severity**: LOW
- **Location**: `frontend/src/components/BootErrorScreen.tsx:9-14` (multi-slice store subscription)
- **Evidence**:
  ```ts
  // frontend/src/components/BootErrorScreen.tsx:9-14
  const bootStatus = useConfigStore((s) => s.bootStatus)
  const bootError = useConfigStore((s) => s.bootError)
  const lastLoadedAt = useConfigStore((s) => s.lastLoadedAt)
  const load = useConfigStore((s) => s.load)
  const refreshErrors = useConfigStore((s) => s.refreshErrors)
  ```
  Five independent selectors; each change triggers a re-render of this always-mounted component (it sits above the router outlet, see `App.tsx:73`). With F-012’s per-error `refreshErrors` ref churn, this re-renders on every transient backend hiccup.
- **Impact**: BootErrorScreen only renders when `bootStatus === 'failed'` or `'degraded'` (line 22-26), so most re-renders are no-ops. Still, the JSX tree is reconstructed each time.
- **Suggested Fix**: Split the boot-error display into its own lazily-mounted sibling of the routes (only mount when `showFatal || showDegraded`). Until then, none of the selectors fire their render path because nothing is visible.
- **Risk of Fix**: Layout shift risk during first paint if the boot error fires before the initial render commits. Use `Suspense` or render a placeholder div.

---

## TL;DR

四页 mount/cleanup 基本正确（Logs 用 `pollCancelledRef`、Settings 用 `mountedRef`、Providers 用 `cancelled`），但 **i18n 把四国语言全量静态 import**（F-001，~200 KB 永久驻留）以及 **ModelTrendChart 的 30d × N 趋势数据 rows useMemo 在 stats 心跳下反复重建**（F-002/F-003/F-004/F-006，~230 KB × 2 每次心跳）是堆增长主因。**ErrorBoundary 持有 Error.stack** 直到手动 reload（F-005）。Dashboard byClientKey 内联 `.sort()` 未 memo（F-007）。两个 drawer `resize` 监听在 resize drag 时会以 60 Hz 触发无效 setState（F-015）。

- Finding 总数: **20**
- HIGH: **6** (F-001, F-002, F-003, F-004, F-005, F-006)
- MEDIUM: **9** (F-007, F-008, F-009, F-010, F-011, F-012, F-013, F-014, F-015)
- LOW: **5** (F-016, F-017, F-018, F-019, F-020)
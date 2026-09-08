import {create} from 'zustand'
import * as App from '../../wailsjs/go/main/App'
import {EventsOn} from '../../wailsjs/runtime/runtime'
import type {Config, Provider, ModelAlias, ServerStatus, Stats, LogEntry, ClientKey} from '@/types'
import {toWailsConfig, toWailsProvider, toWailsModelAlias, toWailsClientKey} from '@/lib/wails'

/**
 * Module-scoped cache of the in-flight `refreshStatsWithComparison`
 * Promise. When the 2s `stats:changed` event fires while a manual
 * refresh is already running (or two events fire back-to-back before
 * the previous promise resolves), both callers get the same Promise
 * instead of triggering two parallel Wails IPC round-trips.
 *
 * Stored outside the Zustand store because it carries no UI state —
 * it's purely an execution dedup marker — and writing/reading it on
 * every render would force unrelated subscribers to re-render.
 */
let inflightStatsRefresh: Promise<void> | null = null
let inflightLogsRefresh: Promise<void> | null = null

interface PeriodStats {
  requests: number
  inputTokens: number
  outputTokens: number
  errors: number
  avgLatency: number
}

interface StatsWithComparison extends Stats {
  prevRequests: number
  prevInputTokens: number
  prevOutputTokens: number
  prevAvgLatency: number
  todayRequests: number
  todayInputTokens: number
  todayOutputTokens: number
  todayErrors: number
  todayAvgLatency: number
  yesterdayRequests: number
  yesterdayInputTokens: number
  yesterdayOutputTokens: number
  yesterdayErrors: number
  yesterdayAvgLatency: number
  allTime: PeriodStats
  week: PeriodStats
  prevWeek: PeriodStats
  month: PeriodStats
  prevMonth: PeriodStats
}

interface ConfigState {
  config: Config | null
  serverStatus: ServerStatus | null
  stats: Stats | null
  statsWithComparison: StatsWithComparison | null
  providers: Provider[]
  modelAliases: ModelAlias[]
  logs: LogEntry[]
  loading: boolean
  /** Boot-time connection state for the Wails backend. */
  bootStatus: 'initializing' | 'ready' | 'degraded' | 'failed'
  /**
   * Last refresh error per data slice. Previously a single
   * `refreshErrors: Record<string, string | null>` object that
   * re-rendered every reader (including BootErrorScreen) on every
   * transient error in any slice. Now four independent scalar
   * fields so a failing stats fetch doesn't flip the logs error
   * flag for subscribers that don't care about stats.
   */
  refreshErrorStats: string | null
  refreshErrorLogs: string | null
  refreshErrorServer: string | null
  refreshErrorLoad: string | null
  /** Last successful load timestamp (ms since epoch). */
  lastLoadedAt: number | null
  /** Set when the boot loader failed. */
  bootError: string | null

  load: () => Promise<void>
  refreshServer: () => Promise<void>
  refreshStats: () => Promise<void>
  refreshStatsWithComparison: () => Promise<void>
  refreshLogs: () => Promise<void>
  refreshAll: () => Promise<void>

  saveConfig: (cfg: Config) => Promise<void>
  updateServerSettings: (host: string, port: number) => Promise<void>
  setClientKeys: (keys: ClientKey[]) => Promise<void>

  upsertProvider: (p: Provider) => Promise<Provider>
  deleteProvider: (id: string) => Promise<void>

  upsertModelAlias: (m: ModelAlias) => Promise<ModelAlias>
  deleteModelAlias: (id: string) => Promise<void>

  startServer: () => Promise<void>
  stopServer: () => Promise<void>
  restartServer: () => Promise<void>
  clearLogs: () => Promise<void>

  /**
   * Subscribe to the backend's `stats:changed` event. Each emission
   * re-pulls the stats (with comparison). Returns an unsubscribe
   * function that the caller must invoke in a `useEffect` cleanup.
   */
  subscribeStatsEvents: () => () => void

  /**
   * Subscribe to the backend's `logs:changed` event. Each emission
   * re-pulls the recent log buffer only — does NOT touch stats.
   * Returns an unsubscribe function for `useEffect` cleanup.
   */
  subscribeLogsEvents: () => () => void
}

export const useConfigStore = create<ConfigState>((set, get) => ({
  config: null,
  serverStatus: null,
  stats: null,
  statsWithComparison: null,
  providers: [],
  modelAliases: [],
  logs: [],
  loading: false,
  bootStatus: 'initializing' as ConfigState['bootStatus'],
  refreshErrorStats: null,
  refreshErrorLogs: null,
  refreshErrorServer: null,
  refreshErrorLoad: null,
  lastLoadedAt: null,
  bootError: null,

  async load() {
    set({loading: true, bootStatus: 'initializing', bootError: null})
    try {
      const cfgRaw = await App.GetConfig()
      const [status, statsWC, logs, providers, aliases] = await Promise.all([
        App.GetServerStatus(),
        App.GetStatsWithComparison().catch(() => App.GetStats()),
        App.GetLogs(300),
        App.ListProviders(),
        App.ListModelAliases(),
      ])
      // Strip wails class instance methods (e.g. Config.convertValues)
      // before assigning to state — state types are plain-object types.
      const cfg: Config = {
        serverHost: cfgRaw.serverHost,
        serverPort: cfgRaw.serverPort,
        clientKeys: cfgRaw.clientKeys as unknown as ClientKey[],
        logRetention: cfgRaw.logRetention,
        providers: cfgRaw.providers as unknown as Provider[],
        modelAliases: cfgRaw.modelAliases as unknown as ModelAlias[],
      }
      const isComparison = 'prevRequests' in (statsWC as object)
      set({
        config: cfg,
        serverStatus: status,
        stats: isComparison ? {
          totalRequests: (statsWC as StatsWithComparison).totalRequests,
          totalInputTokens: (statsWC as StatsWithComparison).totalInputTokens,
          totalOutputTokens: (statsWC as StatsWithComparison).totalOutputTokens,
          avgLatencyMs: (statsWC as StatsWithComparison).avgLatencyMs,
          requestsByModel: (statsWC as StatsWithComparison).requestsByModel,
          requestsByProvider: (statsWC as StatsWithComparison).requestsByProvider,
          requestsByClientKey: (statsWC as StatsWithComparison).requestsByClientKey,
          requestsByClientKeyRecent: (statsWC as StatsWithComparison).requestsByClientKeyRecent,
          requestsByHour: (statsWC as StatsWithComparison).requestsByHour,
          requestsByHourByModel: (statsWC as StatsWithComparison).requestsByHourByModel,
        } as unknown as Stats : statsWC as Stats,
        statsWithComparison: isComparison ? statsWC as unknown as StatsWithComparison : null,
        logs,
        providers: providers as unknown as Provider[],
        modelAliases: aliases as unknown as ModelAlias[],
        loading: false,
        bootStatus: 'ready',
        bootError: null,
        lastLoadedAt: Date.now(),
        refreshErrorStats: null,
        refreshErrorLogs: null,
        refreshErrorServer: null,
        refreshErrorLoad: null,
      })
    } catch (err) {
      set({
        loading: false,
        bootStatus: 'failed',
        bootError: err instanceof Error ? err.message : String(err),
      })
      throw err
    }
  },

  async refreshServer() {
    try {
      const status = await App.GetServerStatus()
      set({serverStatus: status, refreshErrorServer: null})
    } catch (err) {
      set({refreshErrorServer: String(err)})
    }
  },

  async refreshStats() {
    try {
      const stats = await App.GetStats()
      set({stats, refreshErrorStats: null})
    } catch (err) {
      set({refreshErrorStats: String(err)})
    }
  },

  async refreshStatsWithComparison() {
    // Inflight dedup: if a previous call is still resolving, return the
    // same Promise so callers (heartbeat + manual button) share a single
    // IPC round-trip instead of stacking two.
    if (inflightStatsRefresh) return inflightStatsRefresh
    const run = async (): Promise<void> => {
      try {
        const statsWC = await App.GetStatsWithComparison()
        const nextStats: Stats = {
          totalRequests: statsWC.totalRequests,
          totalInputTokens: statsWC.totalInputTokens,
          totalOutputTokens: statsWC.totalOutputTokens,
          avgLatencyMs: statsWC.avgLatencyMs,
          requestsByModel: statsWC.requestsByModel,
          requestsByProvider: statsWC.requestsByProvider,
          requestsByClientKey: statsWC.requestsByClientKey,
          requestsByClientKeyRecent: statsWC.requestsByClientKeyRecent,
          requestsByHour: statsWC.requestsByHour,
          // Per-model hourly breakdown feeds the ModelTrendChart; without
          // this copy the chart's `data` prop is permanently undefined.
          requestsByHourByModel: statsWC.requestsByHourByModel,
        } as unknown as Stats
        // Compare against the previous stats; if nothing actually changed
        // (same totals, same hourly shape) keep the existing reference so
        // memoized selectors / useMemo caches downstream don't all
        // invalidate on every 2s heartbeat. The store-equality check
        // avoids a deep compare while still being cheap.
        const prevStats = get().stats
        const prevWC = get().statsWithComparison
        // The rolling consumption windows (7d / 30d and their previous
        // periods) can change even when the all-time totals don't (e.g.
        // the calendar day rolls over at midnight), so they are compared
        // separately to avoid a stale dashboard.
        const dayChanged =
          !prevWC ||
          prevWC.week?.requests !== statsWC.week?.requests ||
          prevWC.week?.inputTokens !== statsWC.week?.inputTokens ||
          prevWC.week?.outputTokens !== statsWC.week?.outputTokens ||
          prevWC.prevWeek?.requests !== statsWC.prevWeek?.requests ||
          prevWC.month?.requests !== statsWC.month?.requests ||
          prevWC.month?.inputTokens !== statsWC.month?.inputTokens ||
          prevWC.month?.outputTokens !== statsWC.month?.outputTokens ||
          prevWC.prevMonth?.requests !== statsWC.prevMonth?.requests
        if (statsShallowEqual(prevStats, nextStats) && !dayChanged) {
          set({refreshErrorStats: null})
        } else {
          set({
            stats: nextStats,
            statsWithComparison: statsWC as unknown as StatsWithComparison,
            refreshErrorStats: null,
          })
        }
      } catch (err) {
        // Fallback to basic stats if comparison endpoint not available
        try {
          const stats = await App.GetStats()
          const prevStats = get().stats
          if (statsShallowEqual(prevStats, stats)) {
            set({refreshErrorStats: null})
          } else {
            set({stats, refreshErrorStats: null})
          }
        } catch (innerErr) {
          set({refreshErrorStats: String(innerErr)})
        }
      }
    }
    inflightStatsRefresh = run().finally(() => {
      // Always release the slot, even on rejection, so a later call can
      // actually re-fetch instead of getting permanently stuck on a
      // dead promise.
      inflightStatsRefresh = null
    })
    return inflightStatsRefresh
  },

  async refreshLogs() {
    // Inflight dedup: the 3s logs heartbeat can fire while a manual
    // clearLogs() / refresh is still in flight; both callers share the
    // same Promise instead of triggering two IPCs.
    if (inflightLogsRefresh) return inflightLogsRefresh
    const run = async (): Promise<void> => {
      try {
        const logs = await App.GetLogs(300)
        // Keep the previous logs reference when entries are unchanged so
        // the logs table doesn't re-render every 3s poll for nothing.
        const prevLogs = get().logs
        if (logsEqual(prevLogs, logs)) {
          set({refreshErrorLogs: null})
        } else {
          set({logs, refreshErrorLogs: null})
        }
      } catch (err) {
        set({refreshErrorLogs: String(err)})
      }
    }
    inflightLogsRefresh = run().finally(() => {
      inflightLogsRefresh = null
    })
    return inflightLogsRefresh
  },

  async refreshAll() {
    try {
      const [status, statsWC, logs, providers, aliases] = await Promise.all([
        App.GetServerStatus(),
        App.GetStatsWithComparison().catch(() => App.GetStats()),
        App.GetLogs(300),
        App.ListProviders(),
        App.ListModelAliases(),
      ])
      const isComparison = 'prevRequests' in (statsWC as object)
      set({
        serverStatus: status,
        stats: isComparison ? {
          totalRequests: (statsWC as StatsWithComparison).totalRequests,
          totalInputTokens: (statsWC as StatsWithComparison).totalInputTokens,
          totalOutputTokens: (statsWC as StatsWithComparison).totalOutputTokens,
          avgLatencyMs: (statsWC as StatsWithComparison).avgLatencyMs,
          requestsByModel: (statsWC as StatsWithComparison).requestsByModel,
          requestsByProvider: (statsWC as StatsWithComparison).requestsByProvider,
          requestsByClientKey: (statsWC as StatsWithComparison).requestsByClientKey,
          requestsByClientKeyRecent: (statsWC as StatsWithComparison).requestsByClientKeyRecent,
          requestsByHour: (statsWC as StatsWithComparison).requestsByHour,
          requestsByHourByModel: (statsWC as StatsWithComparison).requestsByHourByModel,
        } as unknown as Stats : statsWC as Stats,
        statsWithComparison: isComparison ? statsWC as unknown as StatsWithComparison : null,
        logs,
        providers: providers as unknown as Provider[],
        modelAliases: aliases as unknown as ModelAlias[],
        refreshErrorLoad: null,
        lastLoadedAt: Date.now(),
      })
    } catch (err) {
      set({refreshErrorLoad: String(err)})
    }
  },

  async saveConfig(cfg) {
    // Wails types Config as a class; cast through any to bridge.
    await App.SaveConfig(toWailsConfig(cfg))
    set({config: cfg})
    await get().refreshServer()
  },

  async updateServerSettings(host, port) {
    await App.UpdateServerSettings(host, port)
    set((state) => ({
      config: state.config
        ? {...state.config, serverHost: host, serverPort: port}
        : state.config,
    }))
    await get().refreshServer()
  },

  async setClientKeys(keys) {
    const cfg = get().config
    if (!cfg) return
    await App.SaveConfig(toWailsConfig({...cfg, clientKeys: keys}))
    set({config: {...cfg, clientKeys: keys}})
    await get().refreshAll()
  },

  async upsertProvider(p) {
    const saved = await App.UpsertProvider(toWailsProvider(p))
    await get().refreshAll()
    return saved as unknown as Provider
  },

  async deleteProvider(id) {
    await App.DeleteProvider(id)
    await get().refreshAll()
  },

  async upsertModelAlias(m) {
    const saved = await App.UpsertModelAlias(toWailsModelAlias(m))
    await get().refreshAll()
    return saved as unknown as ModelAlias
  },

  async deleteModelAlias(id) {
    await App.DeleteModelAlias(id)
    await get().refreshAll()
  },

  async startServer() {
    await App.StartServer()
    await get().refreshServer()
  },

  async stopServer() {
    await App.StopServer()
    await get().refreshServer()
  },

  async restartServer() {
    await App.RestartServer()
    await get().refreshServer()
  },

  async clearLogs() {
    await App.ClearLogs()
    await get().refreshLogs()
  },

  subscribeStatsEvents() {
    // The closure must access the store via `get()` (not via captured
    // references) so it always sees the latest state/methods.
    return subscribeOnce('stats', 'stats:changed', () => {
      void get()
        .refreshStatsWithComparison()
        .catch((err) => {
          // The error is already recorded inside refreshStatsWithComparison
          // (refreshErrorStats), but record it here as well in case the
          // comparison path fails before reaching the fallback.
          const message = err instanceof Error ? err.message : String(err)
          set({refreshErrorStats: message})
        })
    })
  },

  subscribeLogsEvents() {
    // Dedicated subscription for the `logs:changed` backend event so
    // it doesn't share a single hook with `stats:changed` (which would
    // cause every 2s heartbeat to trigger a full stats *and* logs
    // refetch, doubling IPC traffic on idle pages).
    return subscribeOnce('logs', 'logs:changed', () => {
      void get()
        .refreshLogs()
        .catch((err) => {
          const message = err instanceof Error ? err.message : String(err)
          set({refreshErrorLogs: message})
        })
    })
  },
}))

// Reference-counted single-flight subscription for Wails events.
// Calling `subscribeOnce(key, eventName, cb)` returns an idempotent
// unsubscribe: the FIRST caller actually invokes EventsOn; later callers
// only bump the refcount. The unsubscribe returned to the LAST caller
// is the one that ends up calling EventsOff.
//
// Why this matters: React StrictMode runs effects twice in development
// (mount → unmount → mount) and Dashboard/page components mount/unmount
// as the user navigates. If each component mount calls EventsOn and
// each unmount calls EventsOff independently, a backend event can fire
// while the listener is in the middle of being torn down, leading to
// `Cannot read properties of null (reading 'nodes')` inside the Wails
// runtime (the listener registry briefly holds a stale entry). Sharing a
// single EventsOn per event name across the whole app sidesteps the race
// entirely.
const eventRegistry = new Map<string, {
  refCount: number
  unsubscribe: () => void
}>()

function subscribeOnce(
  key: string,
  eventName: string,
  callback: () => void,
): () => void {
  const existing = eventRegistry.get(key)
  if (existing) {
    existing.refCount++
    let released = false
    return () => {
      if (released) return
      released = true
      existing.refCount--
      if (existing.refCount === 0) {
        existing.unsubscribe()
        eventRegistry.delete(key)
      }
    }
  }
  const unsubscribe = EventsOn(eventName, callback)
  eventRegistry.set(key, {refCount: 1, unsubscribe})
  let released = false
  return () => {
    if (released) return
    released = true
    const entry = eventRegistry.get(key)
    if (!entry) return
    entry.refCount--
    if (entry.refCount === 0) {
      entry.unsubscribe()
      eventRegistry.delete(key)
    }
  }
}

/**
 * Cheap equality check for the stats shape: scalar totals + per-key
 * lengths. We don't deep-compare the per-hour buckets because the
 * hourly window is already bounded (≤168 entries / 7 days) and the
 * downstream chart already memoizes on it. Trading a tiny precision
 * loss for reference stability saves a full re-render cascade on
 * every 2s heartbeat.
 */
function statsShallowEqual(a: Stats | null, b: Stats | null): boolean {
  if (a === b) return true
  if (!a || !b) return false
  // Also include the per-model hourly map length: when a new request
  // for a brand-new alias lands, totalRequests goes up but the alias
  // count changes too; without this check the chart could miss the
  // new line on its very first request.
  return (
    a.totalRequests === b.totalRequests &&
    a.totalInputTokens === b.totalInputTokens &&
    a.totalOutputTokens === b.totalOutputTokens &&
    a.avgLatencyMs === b.avgLatencyMs &&
    a.requestsByHour.length === b.requestsByHour.length &&
    Object.keys(a.requestsByClientKey).length ===
      Object.keys(b.requestsByClientKey).length &&
    Object.keys(a.requestsByModel).length === Object.keys(b.requestsByModel).length &&
    Object.keys(a.requestsByHourByModel ?? {}).length ===
      Object.keys(b.requestsByHourByModel ?? {}).length
  )
}

/**
 * Compare two log lists by length + first/last entries. The backend
 * returns logs in newest-first order, so the first/last IDs are a
 * reasonable change signal without scanning every row.
 */
function logsEqual(a: LogEntry[], b: LogEntry[]): boolean {
  if (a === b) return true
  if (a.length !== b.length) return false
  if (a.length === 0) return true
  return a[0].id === b[0].id && a[a.length - 1].id === b[b.length - 1].id
}
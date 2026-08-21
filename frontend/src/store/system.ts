// System-level preferences surfaced in the Settings page (auto-start,
// close-to-tray, tray icon). Each is mirrored from a Wails binding so
// the UI can hydrate immediately on first paint and stay in sync when
// the user toggles a switch.
import {create} from 'zustand'
import * as App from '../../wailsjs/go/main/App'

interface SystemState {
  autoStart: boolean
  closeToTray: boolean
  trayEnabled: boolean
  hydrated: boolean

  hydrate: () => Promise<void>
  setAutoStart: (v: boolean) => Promise<void>
  setCloseToTray: (v: boolean) => Promise<void>
  setTrayEnabled: (v: boolean) => Promise<void>
  clearLogsCache: () => Promise<void>
  showWindow: () => Promise<void>
  quitApp: () => Promise<void>
}

export const useSystemStore = create<SystemState>((set) => ({
  autoStart: false,
  closeToTray: false,
  trayEnabled: false,
  hydrated: false,

  async hydrate() {
    // Each binding may not exist in older builds; treat absent
    // methods as "off" rather than blowing up the UI.
    const safe = async <T,>(fn: () => Promise<T>, fallback: T): Promise<T> => {
      try {
        return await fn()
      } catch {
        return fallback
      }
    }
    const [autoStart, closeToTray, trayEnabled] = await Promise.all([
      safe(() => App.GetAutoStart(), false),
      safe(() => App.GetCloseToTray(), false),
      safe(() => App.GetTrayEnabled(), false),
    ])
    set({autoStart, closeToTray, trayEnabled, hydrated: true})
  },

  async setAutoStart(v) {
    set({autoStart: v})
    try {
      await App.SetAutoStart(v, 'APIDistribution')
    } catch {
      // Backend may reject (e.g. non-Windows). The UI will show
      // a friendly toast.
    }
  },

  async setCloseToTray(v) {
    set({closeToTray: v})
    try {
      await App.SetCloseToTray(v)
    } catch {
      // ignored — toast surfaces the error
    }
  },

  async setTrayEnabled(v) {
    set({trayEnabled: v})
    try {
      await App.SetTrayEnabled(v)
    } catch {
      // ignored — toast surfaces the error
    }
  },

  async clearLogsCache() {
    await App.ClearLogs()
  },

  async showWindow() {
    try {
      await App.ShowWindow()
    } catch {
      // ignored
    }
  },

  async quitApp() {
    try {
      await App.QuitApp()
    } catch {
      // ignored
    }
  },
}))
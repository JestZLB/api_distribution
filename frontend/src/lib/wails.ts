// Centralised type-cast helpers for the wails-generated class types.
// The wails binding exposes each model as a TS class with convertValues
// helpers; the frontend stores plain objects via zustand. To pass a
// plain object back to a wails binding we have to assert through
// `unknown`. Routing every cast through one helper keeps the noise
// out of the store/actions and makes it trivial to swap wails for a
// different transport later.

import {types as wailsTypes} from '../../wailsjs/go/models'
import type {Config, Provider, ModelAlias, ClientKey, Stats} from '@/types'

interface StatsWithComparison extends Stats {
  prevRequests: number
  prevInputTokens: number
  prevOutputTokens: number
  prevAvgLatency: number
}

export function toWailsConfig(c: Config): wailsTypes.Config {
  return c as unknown as wailsTypes.Config
}

export function toWailsProvider(p: Provider): wailsTypes.Provider {
  return p as unknown as wailsTypes.Provider
}

export function toWailsModelAlias(m: ModelAlias): wailsTypes.ModelAlias {
  return m as unknown as wailsTypes.ModelAlias
}

export function toWailsClientKey(ck: ClientKey): wailsTypes.ClientKey {
  return ck as unknown as wailsTypes.ClientKey
}

export function toWailsStats(s: Stats): wailsTypes.Stats {
  return s as unknown as wailsTypes.Stats
}

export function toWailsStatsWithComparison(s: StatsWithComparison): wailsTypes.StatsWithComparison {
  return s as unknown as wailsTypes.StatsWithComparison
}
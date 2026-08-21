// Type re-exports from the Wails-generated models so that frontend
// imports via `@/types` stay stable. Source of truth is the Go side;
// this file is regenerated whenever wails build runs.
//
// We OMIT internal wails methods (e.g. Config.convertValues) so that
// frontend code can use plain object literals with the expected
// JSON shape.

import {types as wails} from '../../wailsjs/go/models'

export type AppInfo = wails.AppInfo
export type LogEntry = wails.LogEntry
export type Stats = wails.Stats
export type ServerStatus = wails.ServerStatus
export type HourBucket = wails.HourBucket
export type ModelAlias = wails.ModelAlias
export type Provider = wails.Provider
export type ClientKey = wails.ClientKey

// Plain-object versions of the persisted types — no class methods.
export type Config = {
  serverHost: string
  serverPort: number
  clientKeys: ClientKey[]
  logRetention: number
  providers: Provider[]
  modelAliases: ModelAlias[]
}

// Domain-specific narrow type for the `Provider.type` field.
// Wails types it as `string`; the frontend narrows it to a union.
// Mirrors the backend's KnownProviderTypes(); the generated Wails binding
// also exposes the catalog through App.ListProviderTypes when a dynamic
// list is needed.
export type ProviderType = 'openai' | 'anthropic' | 'azure' | 'gemini' | 'ollama' | 'custom'

export const PROVIDER_TYPES: {value: ProviderType; label: string; description: string}[] = [
  {value: 'openai', label: 'OpenAI', description: 'OpenAI public API (or any OpenAI-compatible vendor).'},
  {value: 'anthropic', label: 'Anthropic', description: 'Anthropic Claude — x-api-key auth.'},
  {value: 'azure', label: 'Azure OpenAI', description: 'Azure OpenAI deployments — api-key header.'},
  {value: 'gemini', label: 'Google Gemini', description: 'Gemini OpenAI-compatible endpoint.'},
  {value: 'ollama', label: 'Ollama (local)', description: 'Local Ollama server at /v1.'},
  {value: 'custom', label: 'Custom / OpenAI-compatible', description: 'Any other OpenAI-style gateway (vLLM, LM Studio, etc.).'},
]

// Suggested default base URLs for the built-in provider types. Azure
// and custom require a user-supplied endpoint, so they have no default.
// Mirrors Go's DefaultBaseURL so the UI can pre-fill when a user
// picks a preset.
const DEFAULT_BASE_URLS: Record<ProviderType, string> = {
  openai: 'https://api.openai.com/v1',
  anthropic: 'https://api.anthropic.com/v1',
  gemini: 'https://generativelanguage.googleapis.com/v1beta/openai',
  ollama: 'http://127.0.0.1:11434/v1',
  azure: '',
  custom: '',
}

// Suggested default model identifiers per provider type. These are
// only placeholders; users can override freely.
const DEFAULT_MODELS: Record<ProviderType, string> = {
  openai: 'gpt-4o-mini',
  anthropic: 'claude-3-5-sonnet-latest',
  gemini: 'gemini-1.5-flash',
  ollama: 'llama3.1',
  azure: '',
  custom: '',
}

export function defaultBaseUrl(type: ProviderType): string {
  return DEFAULT_BASE_URLS[type] ?? ''
}

export function defaultModel(type: ProviderType): string {
  return DEFAULT_MODELS[type] ?? ''
}

// Type guard for ProviderType.
export function isProviderType(v: string): v is ProviderType {
  return (
    v === 'openai' ||
    v === 'anthropic' ||
    v === 'azure' ||
    v === 'gemini' ||
    v === 'ollama' ||
    v === 'custom'
  )
}
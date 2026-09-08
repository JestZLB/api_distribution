import {useCallback, useEffect, useRef, useState} from 'react'
import {
  LuCopy,
  LuDownload,
  LuFolder,
  LuInfo,
  LuMonitor,
  LuMoon,
  LuPower,
  LuRotateCcw,
  LuSave,
  LuServer,
  LuSun,
  LuTrash2,
  LuAppWindow,
} from 'react-icons/lu'
import * as App from '../../wailsjs/go/main/App'
import {App as AntApp, Button, Card, Form, Input, InputNumber, Segmented, Space, Switch, Tag, Tooltip, Typography} from 'antd'
import {PageHeader} from '@/components/layout/PageHeader'
import {ModelConnectionsCard} from '@/pages/settings/ModelConnectionsCard'
import {ClientKeysCard} from '@/components/settings/ClientKeysCard'
import {useConfigStore} from '@/store/config'
import {useSystemStore} from '@/store/system'
import {useThemeStore, type Theme} from '@/store/theme'
import {maskKey, redactKeyForExport} from '@/lib/format'
import {copyToClipboard} from '@/lib/clipboard'
import {useT} from '@/i18n/useT'

interface AppInfoLike {
  version: string
  buildTime: string
  configDir: string
}

// Port validation — mirrors backend ValidatePort (1-65535).
function validatePort(value: number, t: (key: string) => string): string | null {
  if (!Number.isFinite(value) || value <= 0) {
    return t('settings.validate.portPositive')
  }
  if (value > 65535) {
    return t('settings.validate.portRange')
  }
  return null
}

export function Settings() {
  const {message, modal} = AntApp.useApp()
  const config = useConfigStore((s) => s.config)
  const updateServerSettings = useConfigStore((s) => s.updateServerSettings)
  const saveConfig = useConfigStore((s) => s.saveConfig)
  const setClientKeys = useConfigStore((s) => s.setClientKeys)
  const providers = useConfigStore((s) => s.providers)
  const theme = useThemeStore((s) => s.theme)
  const setTheme = useThemeStore((s) => s.setTheme)
  const system = useSystemStore()
  const t = useT()

  const [hostValue, setHostValue] = useState('')
  const [portValue, setPortValue] = useState(0)
  const [logRetention, setLogRetention] = useState(30)
  const [saving, setSaving] = useState(false)
  const [portError, setPortError] = useState<string | null>(null)

  const [appInfo, setAppInfo] = useState<AppInfoLike | null>(null)

  // Guards against setState (and duplicate saves) after the page
  // unmounts — avoids "Can't perform a React state update on an
  // unmounted component" and stray backend calls.
  const mountedRef = useRef(true)
  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  useEffect(() => {
    if (config) {
      setHostValue(config.serverHost)
      setPortValue(config.serverPort)
      setLogRetention(config.logRetention || 7)
    }
  }, [config])

  useEffect(() => {
    let cancelled = false
    // AppInfo is best-effort; ignore errors when the binding isn't ready.
    try {
      App.GetAppInfo()
        .then((info) => {
          if (!cancelled && mountedRef.current) setAppInfo(info)
        })
        .catch(() => {})
    } catch {
      // Wails binding unavailable in browser dev mode
    }
    return () => {
      cancelled = true
    }
  }, [])

  // Hydrate system-level preferences (auto-start, close-to-tray,
  // tray). Tolerates non-Wails environments where the bindings
  // are absent — the UI just shows the defaults.
  useEffect(() => {
    system.hydrate().catch(() => {})
  }, [])

  const handleSave = useCallback(async () => {
    if (!config) {
      message.error(t('toast.settingsLoadFailed'))
      return
    }
    const portIssue = validatePort(portValue, t)
    if (portIssue) {
      setPortError(portIssue)
      return
    }
    // Dirty-check: skip the round-trip when nothing changed.
    if (
      hostValue === config.serverHost &&
      portValue === config.serverPort &&
      logRetention === config.logRetention
    ) {
      message.info(t('toast.noChanges'))
      return
    }
    if (!mountedRef.current) return
    setSaving(true)
    try {
      // Save server settings (host/port).
      await updateServerSettings(hostValue, portValue)
      // Persist logRetention via the dedicated App.SetLogRetention
      // binding so providers / aliases / clientKeys are NOT
      // redundantly re-written to disk, and the proxy is not
      // restarted (log retention only affects the persistLogsLoop's
      // purge tick, never an in-flight handler).
      if (logRetention !== config.logRetention) {
        await App.SetLogRetention(logRetention)
        useConfigStore.setState((state) => ({
          config: state.config
            ? {...state.config, logRetention}
            : state.config,
        }))
      }
      if (!mountedRef.current) return
      message.success(t('toast.settingsSaved'))
    } catch (e) {
      if (!mountedRef.current) return
      message.error(`${t('toast.settingsSaveFailed')}: ${String(e)}`)
    } finally {
      if (mountedRef.current) setSaving(false)
    }
  }, [config, hostValue, portValue, logRetention, updateServerSettings, t, message])

  const handleExportConfig = useCallback(async () => {
    // Holds the most-recent export Blob so a follow-up cleanup
    // pass (or a future "Cancel export" button) can drop the
    // backing buffer. Without an explicit reference the Blob
    // outlives the click handler because the URL → Blob association
    // is kept alive by the browser's object URL table.
    const blobRef = {current: null as Blob | null}
    try {
      const cfg = await App.GetConfig()
      // Use the fixed-width `redactKeyForExport` placeholder instead
      // of `redactKey` (which pads with `*` to match the key length).
      // Long provider / client keys balloon the exported JSON for no
      // benefit — the receiver already knows the field was scrubbed.
      const masked = {
        ...cfg,
        clientKeys: cfg.clientKeys.map((ck) => ({
          ...ck,
          key: redactKeyForExport(ck.key),
        })),
        providers: cfg.providers.map((p) => ({
          ...p,
          apiKey: redactKeyForExport(p.apiKey),
        })),
      }
      const json = JSON.stringify(masked, null, 2)
      const blob = new Blob([json], {type: 'application/json'})
      blobRef.current = blob
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = 'api-distribution-config.json'
      a.click()
      // Defer revoke + blob drop one macrotask so the synchronous
      // `click()` above has finished dispatching the download. The
      // browser holds the URL → Blob association until we call
      // `revokeObjectURL`, so this is what actually frees the
      // backing buffer — without it the export Blob stays alive
      // for the lifetime of the document.
      setTimeout(() => {
        URL.revokeObjectURL(url)
        blobRef.current = null
      }, 0)
      message.success(t('toast.configExported'))
    } catch (e) {
      blobRef.current = null
      message.error(`${t('toast.configExportFailed')}: ${String(e)}`)
    }
  }, [t, message])

  const handleResetDefaults = useCallback(async () => {
    try {
      const defaults = {
        serverHost: '127.0.0.1',
        serverPort: 8080,
        clientKeys: [],
        logRetention: 30,
        providers: [],
        modelAliases: [],
      }
      await saveConfig(defaults)
      setHostValue(defaults.serverHost)
      setPortValue(defaults.serverPort)
      setLogRetention(defaults.logRetention)
      message.success(t('toast.configReset'))
    } catch (e) {
      message.error(`${t('toast.configResetFailed')}: ${String(e)}`)
    }
  }, [config, saveConfig, t, message])

  function promptResetDefaults() {
    modal.confirm({
      title: t('settings.reset.confirm'),
      content: <span>{t('providers.form.discardBody')}</span>,
      okText: t('common.confirm'),
      cancelText: t('common.cancel'),
      okButtonProps: {danger: true},
      onOk: handleResetDefaults,
    })
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('pages.title.settings')}
        description={t('pages.desc.settings')}
      />

      {/* Server */}
      <Card>
        <Typography.Title level={4} className="mt-0! mb-0! text-fg!">
          {t('settings.server')}
        </Typography.Title>
        <Typography.Paragraph className="mt-2! mb-6! text-xs! text-fg-muted!">
          {t('settings.server.desc')}
        </Typography.Paragraph>
        <Form layout="vertical">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-6">
            <Form.Item label={t('settings.host')}>
              <Input
                value={hostValue}
                onChange={(e) => setHostValue(e.target.value)}
                placeholder="127.0.0.1"
                prefix={<LuServer className="size-4 text-fg-subtle" />}
              />
            </Form.Item>
            <Form.Item
              label={t('settings.port')}
              extra={t('settings.port.hint')}
              validateStatus={portError ? 'error' : ''}
              help={portError ?? undefined}
            >
              <InputNumber
                min={1}
                max={65535}
                value={portValue || undefined}
                onChange={(v) => {
                  const next = typeof v === 'number' ? v : Number(v) || 0
                  setPortValue(next)
                  setPortError(validatePort(next, t))
                }}
                placeholder="8080"
                className="w-full!"
              />
            </Form.Item>
          </div>
          <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 pt-5">
            <Typography.Text type="secondary" className="text-xs!">
              {t('settings.restartHint')}
            </Typography.Text>
            <Button
              type="primary"
              loading={saving}
              onClick={handleSave}
              icon={<LuSave className="size-4" />}
              className="sm:ml-auto!"
            >
              {t('settings.save')}
            </Button>
          </div>
        </Form>
      </Card>

      {/* Client Keys */}
      <ClientKeysCard
        clientKeys={config?.clientKeys ?? []}
        onChange={(next) => {
          void setClientKeys(next)
        }}
      />

      {/* Model Connections */}
      <ModelConnectionsCard providers={providers} />

      {/* Persistence */}
      <Card>
        <Typography.Title level={4} className="mt-0! mb-0! text-fg!">
          {t('settings.persistence')}
        </Typography.Title>
        <Typography.Paragraph className="mt-2! mb-6! text-xs text-fg-muted!">   
          {t('settings.persistence.desc')}
        </Typography.Paragraph>
        <Space orientation="vertical" size="middle" className="w-full">
          <div>
            <div className="text-xs text-fg-muted">{t('settings.configDir')}</div>
            <Space.Compact className="mt-1! w-full">
              <Typography.Text code className="flex-1 truncate!">
                {appInfo?.configDir || '\u2014'}
              </Typography.Text>
              {appInfo?.configDir && (
                <Button
                  size="small"
                  onClick={() => copyToClipboard(appInfo!.configDir, 'Config path')}
                  icon={<LuCopy className="size-3.5" />}
                >
                  {t('common.copy')}
                </Button>
              )}
            </Space.Compact>
            {appInfo?.configDir && (
              <Button
                size="small"
                type="text"
                disabled
                icon={<LuFolder className="size-3.5" />}
                title={t('settings.reveal.soon')}
              >
                {t('settings.reveal')}
              </Button>
            )}
          </div>
          <Form layout="vertical">
            <Form.Item
              label={t('settings.logRetention')}
              extra={t('settings.logRetention.hint')}
            >
              <InputNumber
                min={1}
                max={365}
                value={logRetention}
                onChange={(v) => setLogRetention(Math.max(1, Math.min(365, typeof v === 'number' ? v : Number(v) || 7)))}
                placeholder="7"
                className="w-full sm:w-40!"
              />
            </Form.Item>
          </Form>
          <Typography.Text type="secondary" className="text-xs!">
            {t('settings.persistence.backupHint')}
          </Typography.Text>
          <Space wrap className="pt-5 w-full">
            <Button
              size="small"
              onClick={handleExportConfig}
              icon={<LuDownload className="size-3.5" />}
            >
              {t('settings.exportConfig')}
            </Button>
            <Button
              size="small"
              danger
              onClick={promptResetDefaults}
              icon={<LuRotateCcw className="size-3.5" />}
            >
              {t('settings.resetDefaults')}
            </Button>
          </Space>
        </Space>
      </Card>

      {/* System (auto-start, close-to-tray, tray, log cache) */}
      <Card>
        <Typography.Title level={4} className="mt-0! mb-0! text-fg!">
          {t('settings.system')}
        </Typography.Title>
        <Typography.Paragraph className="mt-2! mb-6! text-xs text-fg-muted!">
          {t('settings.system.desc')}
        </Typography.Paragraph>
        <div className="space-y-4">
          {/* Auto-start */}
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1">
              <div className="text-sm font-medium text-fg">{t('settings.system.autoStart')}</div>
              <div className="mt-0.5 text-xs text-fg-muted">{t('settings.system.autoStart.desc')}</div>
            </div>
            <Switch
              checked={system.autoStart}
              onChange={async (v) => {
                await system.setAutoStart(v)
                message.success(v ? t('toast.autoStartEnabled') : t('toast.autoStartDisabled'))
              }}
            />
          </div>
          <div className="border-t border-border" />
          {/* Close to tray */}
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1">
              <div className="text-sm font-medium text-fg">{t('settings.system.closeToTray')}</div>
              <div className="mt-0.5 text-xs text-fg-muted">{t('settings.system.closeToTray.desc')}</div>
            </div>
            <Switch
              checked={system.closeToTray}
              onChange={async (v) => {
                await system.setCloseToTray(v)
                message.success(t('toast.closeToTraySaved'))
              }}
            />
          </div>
          <div className="border-t border-border" />
          {/* Tray icon */}
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1">
              <div className="text-sm font-medium text-fg">{t('settings.system.tray')}</div>
              <div className="mt-0.5 text-xs text-fg-muted">{t('settings.system.tray.desc')}</div>
            </div>
            <Switch
              checked={system.trayEnabled}
              onChange={async (v) => {
                try {
                  await system.setTrayEnabled(v)
                  message.success(v ? t('toast.trayEnabled') : t('toast.trayDisabled'))
                } catch (e) {
                  message.error(`${t('toast.trayError')}: ${String(e)}`)
                }
              }}
            />
          </div>
          <div className="border-t border-border" />
          {/* Clear logs cache */}
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1">
              <div className="text-sm font-medium text-fg">{t('settings.system.clearLogsCache')}</div>
              <div className="mt-0.5 text-xs text-fg-muted">{t('settings.system.clearLogsCache.desc')}</div>
            </div>
            <Button
              size="small"
              icon={<LuTrash2 className="size-3.5" />}
              onClick={() => {
                modal.confirm({
                  title: t('settings.system.clearLogsCache'),
                  content: t('settings.system.clearLogsCache.desc'),
                  okText: t('settings.system.clearLogsCache.button'),
                  okButtonProps: {danger: true},
                  cancelText: t('common.cancel'),
                  onOk: async () => {
                    try {
                      await system.clearLogsCache()
                      message.success(t('toast.logsCacheCleared'))
                    } catch (e) {
                      message.error(`${t('toast.configResetFailed')}: ${String(e)}`)
                    }
                  },
                })
              }}
            >
              {t('settings.system.clearLogsCache.button')}
            </Button>
          </div>
          <div className="border-t border-border" />
          {/* Window controls — visible only when the app could be
           * running in the background (closeToTray or trayEnabled). */}
          {(system.closeToTray || system.trayEnabled) && (
            <Space wrap>
              <Tooltip title={t('settings.system.showWindow')}>
                <Button
                  size="small"
                  icon={<LuAppWindow className="size-3.5" />}
                  onClick={() => system.showWindow()}
                >
                  {t('settings.system.showWindow')}
                </Button>
              </Tooltip>
              <Button
                size="small"
                danger
                icon={<LuPower className="size-3.5" />}
                onClick={() => {
                  modal.confirm({
                    title: t('settings.system.quitApp'),
                    okText: t('settings.system.quitApp'),
                    okButtonProps: {danger: true},
                    cancelText: t('common.cancel'),
                    onOk: () => system.quitApp(),
                  })
                }}
              >
                {t('settings.system.quitApp')}
              </Button>
            </Space>
          )}
        </div>
      </Card>

      {/* Appearance */}
        <Card>
        <Typography.Title level={4} className="mt-0! mb-0! text-fg!">
          {t('settings.appearance')}
        </Typography.Title>
        <Typography.Paragraph className="mt-2! mb-6! text-xs text-fg-muted!">
          {t('settings.appearance.desc')}
        </Typography.Paragraph>
        <Segmented<Theme>
          value={theme}
          onChange={(v) => setTheme(v)}
          options={[
            {label: <span><LuSun className="size-4 inline mr-1" />{t('settings.theme.light')}</span>, value: 'light'},
            {label: <span><LuMoon className="size-4 inline mr-1" />{t('settings.theme.dark')}</span>, value: 'dark'},
            {label: <span><LuMonitor className="size-4 inline mr-1" />{t('settings.theme.system')}</span>, value: 'system'},
          ]}
        />
      </Card>

      {/* About */}
      <Card>
        <Typography.Title level={4} className="mt-0! mb-0! text-fg!">
          {t('settings.about')}
        </Typography.Title>
        <Typography.Paragraph className="mt-2! mb-6! text-xs text-fg-muted!">
          {t('settings.about.desc')}
        </Typography.Paragraph>
        <dl className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-5 text-sm">
          <div>
            <dt className="text-xs text-fg-subtle">{t('settings.version')}</dt>
            <dd className="mt-1!">
              <Space>
                <Typography.Text code>{appInfo?.version || '0.0.0'}</Typography.Text>
                <Tag color="blue">{t('settings.about.local')}</Tag>
              </Space>
            </dd>
          </div>
          <div>
            <dt className="text-xs text-fg-subtle">{t('settings.buildTime')}</dt>
            <dd className="mt-1!">
              <Typography.Text code>{appInfo?.buildTime || t('settings.about.unknown')}</Typography.Text>
            </dd>
          </div>
          <div>
            <dt className="text-xs text-fg-subtle">{t('settings.stack')}</dt>
            <dd className="mt-1! text-fg!">Wails + React + TypeScript</dd>
          </div>
        </dl>
        <Typography.Paragraph className="mt-4! text-xs! text-fg-muted!">
          <Space align="center">
            <LuInfo className="size-3.5" />
            <span>{t('settings.about.descText')}</span>
          </Space>
        </Typography.Paragraph>
      </Card>
    </div>
  )
}
import {useEffect, useMemo, useState} from 'react'
import {
  LuCircleCheck,
  LuCopy,
  LuPencil,
  LuPlug,
  LuPlus,
  LuTestTube,
  LuTrash2,
  LuEye,
  LuEyeOff,
} from 'react-icons/lu'
import * as App from '../../wailsjs/go/main/App'
import {App as AntApp, Badge, Button, Card, Drawer, Form, Input, InputNumber, Select, Space, Switch, Tag, Tooltip, Typography} from 'antd'
import {PageHeader} from '@/components/layout/PageHeader'
import {EmptyState} from '@/components/ui/EmptyState'
import {useConfigStore} from '@/store/config'
import {maskKey} from '@/lib/format'
import {copyToClipboard} from '@/lib/clipboard'
import {cn} from '@/lib/cn'
import {newId} from '@/lib/id'
import {type Provider, type ProviderType} from '@/types'
import {useT} from '@/i18n/useT'

// i18n key mappings — used as fallback when backend data is unavailable
const i18nKeyMap: Record<string, {labelKey: string; descKey: string}> = {
  openai: {labelKey: 'providers.form.label.openai', descKey: 'providers.form.desc.openai'},
  anthropic: {labelKey: 'providers.form.label.anthropic', descKey: 'providers.form.desc.anthropic'},
  azure: {labelKey: 'providers.form.label.azure', descKey: 'providers.form.desc.azure'},
  gemini: {labelKey: 'providers.form.label.gemini', descKey: 'providers.form.desc.gemini'},
  ollama: {labelKey: 'providers.form.label.ollama', descKey: 'providers.form.desc.ollama'},
  custom: {labelKey: 'providers.form.label.custom', descKey: 'providers.form.desc.custom'},
}

// Hardcoded fallback matching backend KnownProviderTypes() order
const FALLBACK_TYPE_OPTIONS: {value: ProviderType; labelKey: string; descKey: string}[] = [
  {value: 'openai', labelKey: 'providers.form.label.openai', descKey: 'providers.form.desc.openai'},
  {value: 'anthropic', labelKey: 'providers.form.label.anthropic', descKey: 'providers.form.desc.anthropic'},
  {value: 'azure', labelKey: 'providers.form.label.azure', descKey: 'providers.form.desc.azure'},
  {value: 'gemini', labelKey: 'providers.form.label.gemini', descKey: 'providers.form.desc.gemini'},
  {value: 'ollama', labelKey: 'providers.form.label.ollama', descKey: 'providers.form.desc.ollama'},
  {value: 'custom', labelKey: 'providers.form.label.custom', descKey: 'providers.form.desc.custom'},
]

function isValidHttpUrl(s: string): boolean {
  try {
    const u = new URL(s)
    return u.protocol === 'http:' || u.protocol === 'https:'
  } catch {
    return false
  }
}

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

export function Providers() {
  const {message, modal} = AntApp.useApp()
  const providers = useConfigStore((s) => s.providers)
  const modelAliases = useConfigStore((s) => s.modelAliases)
  const upsertProvider = useConfigStore((s) => s.upsertProvider)
  const deleteProvider = useConfigStore((s) => s.deleteProvider)
  const t = useT()

  const [editing, setEditing] = useState<Provider | null>(null)
  const [initialEditing, setInitialEditing] = useState<Provider | null>(null)
  const [testing, setTesting] = useState<string | null>(null)
  const [typeOptions, setTypeOptions] = useState(FALLBACK_TYPE_OPTIONS)

  const [drawerWidth, setDrawerWidth] = useState<number | '100%'>(520)
  useEffect(() => {
    const update = () => setDrawerWidth(window.innerWidth < 600 ? '100%' : 520)
    update()
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])

  // Fetch provider types from backend; fall back to hardcoded list on error.
  useEffect(() => {
    let cancelled = false
    try {
      App.ListProviderTypes()
        .then((types) => {
          if (cancelled) return
          const mapped = types.map((ti) => ({
            value: ti.type as ProviderType,
            labelKey: i18nKeyMap[ti.type]?.labelKey ?? `providers.form.label.${ti.type}`,
            descKey: i18nKeyMap[ti.type]?.descKey ?? `providers.form.desc.${ti.type}`,
          }))
          setTypeOptions(mapped)
        })
        .catch(() => {})
    } catch {
      // Wails binding unavailable in browser dev mode
    }
    return () => { cancelled = true }
  }, [])

  const aliasCountByProvider = useMemo(() => {
    const map = new Map<string, number>()
    for (const a of modelAliases) {
      map.set(a.providerId, (map.get(a.providerId) ?? 0) + 1)
    }
    return map
  }, [modelAliases])

  async function handleSave(p: Provider) {
    try {
      await upsertProvider(p)
      message.success(t('toast.providerSaved'))
      setEditing(null)
      setInitialEditing(null)
    } catch (e) {
      message.error(`${t('toast.providers.saveFailed')}: ${String(e)}`)
    }
  }

  function openEditor(provider: Provider) {
    setEditing(provider)
    setInitialEditing(provider)
  }

  function tryCloseEditor() {
    const dirty = editing && initialEditing && JSON.stringify(editing) !== JSON.stringify(initialEditing)
    if (dirty) {
      modal.confirm({
        title: t('providers.form.discardConfirm'),
        content: t('providers.form.discardBody'),
        okText: t('common.confirm'),
        cancelText: t('common.cancel'),
        okButtonProps: {danger: true},
        onOk: () => {
          discardAndClose()
        },
      })
      return
    }
    setEditing(null)
    setInitialEditing(null)
  }

  function discardAndClose() {
    setEditing(null)
    setInitialEditing(null)
  }

  function confirmDelete(p: Provider) {
    modal.confirm({
      title: t('provider.form.deleteConfirm'),
      content: t('providers.modal.deleteDesc', {
        name: p.name || t('providers.card.untitled'),
        count: aliasCountByProvider.get(p.id) ?? 0,
      }),
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      okButtonProps: {danger: true},
      onOk: async () => {
        try {
          await deleteProvider(p.id)
          message.success(t('toast.providerDeleted'))
        } catch (e) {
          message.error(`${t('toast.providers.deleteFailed')}: ${String(e)}`)
        }
      },
    })
  }

  async function handleTest(p: Provider) {
    setTesting(p.id)
    try {
      const result = await App.TestProvider(p)
      message.success(`${t('toast.providers.connectionOK')} · ${result}`)
    } catch (e) {
      message.error(`${t('toast.providers.testFailed')}: ${String(e)}`)
    } finally {
      setTesting(null)
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('pages.title.providers')}
        description={t('pages.desc.providers')}
        actions={
          <Button
            type="primary"
            onClick={() => setEditing(defaultProvider())}
            icon={<LuPlus className="size-4" />}
          >
            {t('providers.add')}
          </Button>
        }
      />

      {providers.length === 0 ? (
        <Card>
          <EmptyState
            icon={<LuPlug className="size-5 text-fg-subtle" aria-hidden />}
            title={t('providers.empty.title')}
            description={t('providers.empty.desc')}
            action={
              <Button
                type="primary"
                onClick={() => setEditing(defaultProvider())}
                icon={<LuPlus className="size-4" />}
              >
                {t('providers.add')}
              </Button>
            }
          />
        </Card>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-5">
          {providers.map((p) => (
            <ProviderCard
              key={p.id}
              provider={p}
              aliasCount={aliasCountByProvider.get(p.id) ?? 0}
              onEdit={() => openEditor(p)}
              onDelete={() => confirmDelete(p)}
              onTest={() => handleTest(p)}
              testing={testing === p.id}
            />
          ))}
        </div>
      )}

      <Drawer
        open={editing !== null}
        onClose={tryCloseEditor}
        title={editing && providers.some((p) => p.id === editing.id) ? t('providers.drawer.edit') : t('providers.drawer.new')}
        width={drawerWidth}
        styles={{body: {padding: '24px 32px'}}}
        extra={
          editing ? (
            <Space>
              <Button onClick={tryCloseEditor}>{t('common.cancel')}</Button>
              <Button
                type="primary"
                onClick={() => handleSave(editing)}
                icon={<LuCircleCheck className="size-4" />}
              >
                {t('providers.drawer.save')}
              </Button>
            </Space>
          ) : null
        }
      >
        {editing && (
          <ProviderForm
            provider={editing}
            onChange={setEditing}
            typeOptions={typeOptions}
          />
        )}
      </Drawer>
    </div>
  )
}

function ProviderCard({
  provider,
  aliasCount,
  onEdit,
  onDelete,
  onTest,
  testing,
}: {
  provider: Provider
  aliasCount: number
  onEdit: () => void
  onDelete: () => void
  onTest: () => void
  testing: boolean
}) {
  const {message} = AntApp.useApp()
  const [reveal, setReveal] = useState(false)
  const upsertProvider = useConfigStore((s) => s.upsertProvider)
  const t = useT()

  async function toggleEnabled() {
    await upsertProvider({...provider, enabled: !provider.enabled})
  }

  return (
    <Card className="h-full flex flex-col!">
      <div className="flex items-start justify-between gap-2!">
        <div className="min-w-0!">
          <Typography.Title level={5} className="mt-0! mb-0! text-fg! truncate!">
            {provider.name || t('providers.card.untitled')}
          </Typography.Title>
          <Space size={4} className="mt-2!">
            <Tag color="blue">{provider.type}</Tag>
            <Badge
              status={provider.enabled ? 'success' : 'default'}
              text={provider.enabled ? t('providers.form.enabled') : t('dashboard.badge.disabled')}
            />
          </Space>
        </div>
        <Switch checked={provider.enabled} onChange={toggleEnabled} />
      </div>
      <div className="mt-4! space-y-4! flex-1!">
        <div className="space-y-1.5!">
          <div className="text-xs text-fg-subtle!">{t('providers.form.baseUrl')}</div>
          <div className="flex items-center gap-2!">
            <Typography.Text code className="flex-1 truncate!">
              {provider.baseUrl || '—'}
            </Typography.Text>
            {provider.baseUrl && (
              <Tooltip title={t('providers.card.copyUrl')}>
                <Button
                  type="text"
                  size="small"
                  aria-label={t('providers.card.copyUrl')}
                  onClick={() => {
                    copyToClipboard(provider.baseUrl, t('providers.card.copyUrl'))
                  }}
                  icon={<LuCopy className="size-3.5" />}
                />
              </Tooltip>
            )}
          </div>
        </div>
        <div className="space-y-1.5">
          <div className="text-xs text-fg-subtle">{t('providers.form.apiKey')}</div>
          <div className="flex items-center gap-2">
            <Typography.Text code className="flex-1 truncate">
              {provider.apiKey
                ? reveal
                  ? provider.apiKey
                  : maskKey(provider.apiKey)
                : '—'}
            </Typography.Text>
            <Tooltip title={reveal ? t('providers.card.hide') : t('providers.card.reveal')}>
              <Button
                type="text"
                size="small"
                aria-label={reveal ? t('providers.card.hide') : t('providers.card.reveal')}
                onClick={() => setReveal((v) => !v)}
                disabled={!provider.apiKey}
                icon={reveal ? <LuEyeOff className="size-3.5" /> : <LuEye className="size-3.5" />}
              />
            </Tooltip>
          </div>
        </div>
        <div className="flex items-center gap-4 text-xs text-fg-muted">
          <span>{t('providers.form.priority')} {provider.priority}</span>
          <span>{t('providers.form.weight')} {provider.weight}</span>
          <span>
            {t('providers.card.aliases', {count: aliasCount})}
          </span>
        </div>
        {provider.notes && (
          <p className="text-xs text-fg-muted line-clamp-2">{provider.notes}</p>
        )}
      </div>
      <div className="flex items-center justify-between mt-4! pt-4! border-t border-border!">
        <Button
          size="small"
          type="text"
          onClick={onTest}
          loading={testing}
          icon={<LuTestTube className="size-3.5" />}
        >
          {t('providers.card.test')}
        </Button>
        <Space size={4} className="mt-1!">
          <Button size="small" onClick={onEdit} icon={<LuPencil className="size-3.5" />}>
            {t('common.edit')}
          </Button>
          <Tooltip title={t('providers.card.deleteLabel')}>
            <Button
              size="small"
              type="text"
              aria-label={t('providers.card.deleteLabel')}
              onClick={onDelete}
              danger
              icon={<LuTrash2 className="size-3.5" />}
              className={cn('text-danger')}
            />
          </Tooltip>
        </Space>
      </div>
    </Card>
  )
}

function ProviderForm({
  provider,
  onChange,
  typeOptions,
}: {
  provider: Provider
  onChange: (next: Provider) => void
  typeOptions: {value: ProviderType; labelKey: string; descKey: string}[]
}) {
  const t = useT()
  const [touched, setTouched] = useState<{
    name: boolean
    baseUrl: boolean
    apiKey: boolean
  }>({name: false, baseUrl: false, apiKey: false})

  function update<K extends keyof Provider>(key: K, value: Provider[K]) {
    onChange({...provider, [key]: value})
  }

  const errors = useMemo(() => {
    const e: {name?: string; baseUrl?: string; apiKey?: string} = {}
    if (!provider.name.trim()) e.name = t('providers.form.validate.nameRequired')
    if (!provider.baseUrl.trim()) {
      e.baseUrl = t('providers.form.validate.baseUrlRequired')
    } else if (!isValidHttpUrl(provider.baseUrl.trim())) {
      e.baseUrl = t('providers.form.validate.baseUrlInvalid')
    }
    // Ollama and similar local providers don't require an API key.
    const localTypes = new Set(['ollama'])
    if (!provider.apiKey.trim() && !localTypes.has((provider.type ?? '').toLowerCase())) {
      e.apiKey = t('providers.form.validate.apiKeyRequired')
    }
    return e
  }, [provider.name, provider.baseUrl, provider.apiKey, provider.type, t])

  async function handleTypeChange(newType: ProviderType) {
    const updated = {...provider, type: newType}
    try {
      const suggested = await App.SuggestProviderDefaults(updated as any)
      onChange({
        ...provider,
        type: newType,
        baseUrl: suggested.baseUrl || provider.baseUrl,
        weight: suggested.weight || provider.weight,
      })
    } catch {
      // Backend unavailable — just update the type without auto-fill
      onChange({...provider, type: newType})
    }
  }

  return (
    <Form layout="vertical">
      <Form.Item
        label={t('providers.form.name')}
        required
        validateStatus={touched.name && errors.name ? 'error' : ''}
        help={touched.name ? errors.name : undefined}
      >
        <Input
          value={provider.name}
          onChange={(e) => update('name', e.target.value)}
          onBlur={() => setTouched((tt) => ({...tt, name: true}))}
          placeholder={t('providers.form.name.placeholder')}
        />
      </Form.Item>
      <div className="grid grid-cols-2 gap-3">
        <Form.Item label={t('providers.form.type')} required>
          <Select
            value={provider.type}
            onChange={(v) => handleTypeChange(v as ProviderType)}
            options={typeOptions.map((o) => ({value: o.value, label: t(o.labelKey)}))}
          />
        </Form.Item>
        <Form.Item
          label={t('providers.form.baseUrl')}
          required
          validateStatus={touched.baseUrl && errors.baseUrl ? 'error' : ''}
          help={touched.baseUrl ? errors.baseUrl : undefined}
        >
          <Input
            value={provider.baseUrl}
            onChange={(e) => update('baseUrl', e.target.value)}
            onBlur={() => setTouched((tt) => ({...tt, baseUrl: true}))}
            placeholder="https://api.openai.com/v1"
          />
        </Form.Item>
      </div>
      <Form.Item
        label={t('providers.form.apiKey')}
        required
        extra={t('providers.form.apiKey.hint')}
        validateStatus={touched.apiKey && errors.apiKey ? 'error' : ''}
        help={touched.apiKey ? errors.apiKey : undefined}
      >
        <Input.Password
          value={provider.apiKey}
          onChange={(e) => update('apiKey', e.target.value)}
          onBlur={() => setTouched((tt) => ({...tt, apiKey: true}))}
          placeholder="sk-…"
        />
      </Form.Item>
      <div className="grid grid-cols-2 gap-3">
        <Form.Item label={t('providers.form.priority')} extra={t('providers.form.priority.hint')}>
          <InputNumber
            value={provider.priority}
            onChange={(v) => update('priority', (typeof v === 'number' ? v : Number(v) || 0))}
            className="w-full!"
          />
        </Form.Item>
        <Form.Item label={t('providers.form.weight')} extra={t('providers.form.weight.hint')}>
          <InputNumber
            value={provider.weight}
            onChange={(v) => update('weight', (typeof v === 'number' ? v : Number(v) || 1))}
            className="w-full!"
          />
        </Form.Item>
      </div>
      <Form.Item label={t('providers.form.models')} extra={t('providers.form.models.hint')}>
        <Input
          value={provider.models.join(', ')}
          onChange={(e) =>
            update(
              'models',
              e.target.value
                .split(',')
                .map((s) => s.trim())
                .filter(Boolean),
            )
          }
          placeholder={t('providers.form.models.placeholder')}
        />
      </Form.Item>
      <Form.Item label={t('provider.form.notes')}>
        <Input
          value={provider.notes}
          onChange={(e) => update('notes', e.target.value)}
          placeholder={t('providers.form.notes.placeholder')}
        />
      </Form.Item>
      <Form.Item label={t('providers.form.enabled')} extra={t('providers.form.enabled.desc')}>
        <Switch
          checked={provider.enabled}
          onChange={(v) => update('enabled', v)}
        />
      </Form.Item>
    </Form>
  )
}
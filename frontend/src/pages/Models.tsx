import {useEffect, useMemo, useState} from 'react'
import {
  LuBox,
  LuCircleCheck,
  LuPencil,
  LuPlug,
  LuPlus,
  LuSearch,
  LuSparkles,
  LuTrash2,
} from 'react-icons/lu'
import {App as AntApp, Badge, Button, Card, Collapse, Divider, Drawer, Form, Input, Select, Space, Switch, Typography} from 'antd'
import {PageHeader} from '@/components/layout/PageHeader'
import {EmptyState} from '@/components/ui/EmptyState'
import {useConfigStore} from '@/store/config'
import {cn} from '@/lib/cn'
import {newId} from '@/lib/id'
import type {ModelAlias, Provider} from '@/types'
import {useT} from '@/i18n/useT'

// Common model presets per provider type. Shown as quick-pick chips
// when the user selects a provider. Users can still type any model ID.
const MODEL_PRESETS: Record<string, string[]> = {
  openai: ['gpt-4o', 'gpt-4o-mini', 'gpt-4-turbo', 'gpt-4.1', 'gpt-4.1-mini', 'o1', 'o1-mini', 'o3-mini', 'o4-mini', 'gpt-3.5-turbo'],
  anthropic: ['claude-sonnet-4-20250514', 'claude-3-5-haiku-latest', 'claude-3-opus-latest', 'claude-3-5-sonnet-latest'],
  gemini: ['gemini-2.5-flash', 'gemini-2.5-pro', 'gemini-2.0-flash', 'gemini-1.5-pro', 'gemini-1.5-flash'],
  ollama: ['llama3.1', 'llama3.2', 'mistral', 'codellama', 'qwen2.5', 'deepseek-r1', 'phi3'],
  azure: [],
  custom: [],
}

function getPresetsForProvider(provider: Provider | undefined): string[] {
  if (!provider) return []
  const type = (provider.type || '').toLowerCase()
  const preset = MODEL_PRESETS[type] ?? []
  // Merge presets with provider's own model list, deduped. Avoid
  // allocating a `new Set` per keystroke by walking both arrays in a
  // single pass and tracking seen values in a plain object map.
  const providerModels = provider.models ?? []
  const seen: Record<string, true> = {}
  const merged: string[] = []
  for (const m of preset) {
    if (!seen[m]) {
      seen[m] = true
      merged.push(m)
    }
  }
  for (const m of providerModels) {
    if (!seen[m]) {
      seen[m] = true
      merged.push(m)
    }
  }
  return merged
}

function defaultAlias(): ModelAlias {
  return {
    id: newId('alias'),
    alias: '',
    providerId: '',
    providerModel: '',
    tags: [],
    enabled: true,
    description: '',
  }
}

export function Models() {
  const {message, modal} = AntApp.useApp()
  const providers = useConfigStore((s) => s.providers)
  const modelAliases = useConfigStore((s) => s.modelAliases)
  const upsertModelAlias = useConfigStore((s) => s.upsertModelAlias)
  const deleteModelAlias = useConfigStore((s) => s.deleteModelAlias)
  const t = useT()

  const [search, setSearch] = useState('')
  const [editing, setEditing] = useState<ModelAlias | null>(null)

  const [drawerWidth, setDrawerWidth] = useState<number | '100%'>(520)
  useEffect(() => {
    const update = () =>
      setDrawerWidth((prev) => {
        const next = window.innerWidth < 600 ? '100%' : 520
        return prev === next ? prev : next
      })
    update()
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])

  const providerById = useMemo(() => {
    return new Map(providers.map((p) => [p.id, p]))
  }, [providers])

  // Precompute the per-alias search haystack so typing in the search
  // box doesn't re-join / re-lowercase each alias's strings on every
  // keystroke. Keyed on the alias roster + the provider-by-id map so
  // the map invalidates whenever either input moves.
  const haystackByAliasId = useMemo(() => {
    const m = new Map<string, string>()
    for (const a of modelAliases) {
      const provider = providerById.get(a.providerId)
      m.set(
        a.id,
        [a.alias, a.providerModel, a.description, provider?.name ?? '', ...(a.tags ?? [])]
          .join(' ')
          .toLowerCase(),
      )
    }
    return m
  }, [modelAliases, providerById])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return modelAliases
    return modelAliases.filter((a) => (haystackByAliasId.get(a.id) ?? '').includes(q))
  }, [modelAliases, search, haystackByAliasId])

  async function handleSave(alias: ModelAlias) {
    try {
      await upsertModelAlias(alias)
      message.success(t('toast.models.saved'))
      setEditing(null)
    } catch (e) {
      message.error(`${t('toast.models.saveFailed')}: ${String(e)}`)
    }
  }

  function confirmDelete(alias: ModelAlias) {
    modal.confirm({
      title: t('models.modal.deleteTitle'),
      content: (
        <Space orientation="vertical" size={0}>
          <span>{t('models.modal.deleteDesc', {alias: alias.alias})}</span>
          <span>
            {t('models.modal.deleteBodyBefore')} <Typography.Text code>{alias.alias}</Typography.Text> {t('models.modal.deleteBodyAfter')}
          </span>
        </Space>
      ),
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      okButtonProps: {danger: true},
      onOk: async () => {
        try {
          await deleteModelAlias(alias.id)
          message.success(t('toast.models.deleted'))
        } catch (e) {
          message.error(`${t('toast.models.deleteFailed')}: ${String(e)}`)
        }
      },
    })
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('pages.title.models')}
        description={t('pages.desc.models')}
        actions={
          <Button
            type="primary"
            onClick={() => setEditing(defaultAlias())}
            icon={<LuPlus className="size-4" />}
          >
            {t('models.add')}
          </Button>
        }
      />

      <div className="flex flex-col sm:flex-row sm:items-center gap-4">
        <Input
          prefix={<LuSearch className="size-4 text-fg-subtle" />}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t('models.search.placeholder')}
          className="flex-1! sm:max-w-lg!"
          allowClear
          size="large"
        />
        <div className="ml-auto inline-flex items-center gap-1.5 rounded-full bg-bg-subtle border border-border px-3 py-1 text-xs font-medium text-fg-muted">
          <span className="text-fg">{filtered.length}</span>
          <span className="text-fg-subtle">/</span>
          <span>{modelAliases.length}</span>
        </div>
      </div>

      {filtered.length === 0 ? (
        providers.length === 0 && modelAliases.length === 0 ? (
          <Card>
            <EmptyState
              icon={<LuPlug className="size-10 mx-auto text-fg-subtle" aria-hidden />}
              title={t('models.empty.noProviders')}
              description={t('models.empty.noProvidersDesc')}
              action={
                <Button
                  type="primary"
                  onClick={() => (window.location.hash = '#/providers')}
                  icon={<LuPlus className="size-4" />}
                >
                  {t('models.empty.addProvider')}
                </Button>
              }
            />
          </Card>
        ) : (
          <Card>
            <EmptyState
              icon={<LuBox className="size-10 mx-auto text-fg-subtle" aria-hidden />}
              title={modelAliases.length === 0 ? t('models.empty.title') : t('models.empty.noMatch')}
              description={
                modelAliases.length === 0
                  ? t('models.empty.desc')
                  : t('models.empty.noMatchDesc')
              }
              action={
                modelAliases.length === 0 && (
                  <Button
                    type="primary"
                    onClick={() => setEditing(defaultAlias())}
                    icon={<LuPlus className="size-4" />}
                  >
                    {t('models.add')}
                  </Button>
                )
              }
            />
          </Card>
        )
      ) : (
        <Card>
          <div className="divide-y divide-border">
            {filtered.map((alias) => {
              const provider = providerById.get(alias.providerId)
              return (
                <div
                  key={alias.id}
                  className="flex flex-col gap-3 py-3 sm:flex-row sm:items-center sm:justify-between"
                >
                  <div className="min-w-0 flex-1 space-y-1">
                    <Space wrap size={6}>
                      <Typography.Text code className="text-sm!">
                        {alias.alias || t('models.unnamed')}
                      </Typography.Text>
                      {alias.tags.map((tag) => (
                        <Badge key={tag} color="blue" text={tag} />
                      ))}
                    </Space>
                    <Typography.Paragraph type="secondary" className="text-sm! m-0!">
                      {provider?.name ?? t('dashboard.unknownProvider')}
                      <span className="mx-1 text-fg-subtle">→</span>
                      <span className="font-mono">{alias.providerModel || '—'}</span>
                    </Typography.Paragraph>
                  </div>
                  <Space size={4} className="items-center">
                    <Switch
                      checked={alias.enabled}
                      onChange={(v) => upsertModelAlias({...alias, enabled: v})}
                    />
                    <Button
                      size="small"
                      onClick={() => setEditing(alias)}
                      icon={<LuPencil className="size-3.5" />}
                    >
                      {t('common.edit')}
                    </Button>
                    <Button
                      size="small"
                      type="text"
                      aria-label={t('models.deleteAlias')}
                      onClick={() => confirmDelete(alias)}
                      danger
                      icon={<LuTrash2 className="size-3.5" />}
                    />
                  </Space>
                </div>
              )
            })}
          </div>
        </Card>
      )}

      <Card className="px-2!">
        <Collapse
          ghost
          items={[
            {
              key: 'howItWorks',
              label: <span className="text-sm font-semibold text-fg!">{t('models.howItWorks')}</span>,
              children: (
                <>
                  <Typography.Paragraph className="mt-2! mb-3! text-sm! text-fg-muted!">
                    {t('models.howItWorks.desc')}
                  </Typography.Paragraph>
                  <ol className="mt-1! space-y-1.5! text-sm! text-fg-muted! list-decimal pl-5!">
                    <li>{t('models.howItWorks.step1a')} <Typography.Text code>POST /v1/chat/completions</Typography.Text> {t('models.howItWorks.step1b')} <Typography.Text code>model: "my-alias"</Typography.Text>.</li>
                    <li>{t('models.howItWorks.step2a')} <Typography.Text code>my-alias</Typography.Text> {t('models.howItWorks.step2b')}</li>
                    <li>{t('models.howItWorks.step3')}</li>
                    <li>{t('models.howItWorks.step4')}</li>
                  </ol>
                </>
              ),
            },
          ]}
        />
        <Divider className="my-2!" />
        <Collapse
          ghost
          items={[
            {
              key: 'example',
              label: <span className="text-xs text-fg-subtle!">{t('models.showExample')}</span>,
              children: (
                <pre className="mt-3! rounded-md bg-bg-subtle! border border-border! px-3! py-2! text-xs! font-mono! text-fg! overflow-x-auto!">
{`curl -X POST http://localhost:8080/v1/chat/completions \\
  -H "Authorization: Bearer $GATEWAY_KEY" \\
  -d '{
    "model": "my-alias",
    "messages": [{ "role": "user", "content": "Hello!" }]
  }'`}
                </pre>
              ),
            },
          ]}
        />
      </Card>

      <Drawer
        open={editing !== null}
        onClose={() => setEditing(null)}
        title={editing && modelAliases.some((m) => m.id === editing.id) ? t('models.drawer.edit') : t('models.drawer.new')}
        size={drawerWidth}
        styles={{body: {padding: '24px 32px'}}}
      >
        {editing && (
          <AliasForm
            alias={editing}
            providers={providers}
            providerById={providerById}
            existingAliases={modelAliases}
            onChange={setEditing}
            onSave={() => handleSave(editing)}
            onCancel={() => setEditing(null)}
          />
        )}
      </Drawer>
    </div>
  )
}

function AliasForm({
  alias,
  providers,
  providerById,
  existingAliases,
  onChange,
  onSave,
  onCancel,
}: {
  alias: ModelAlias
  providers: Provider[]
  providerById: Map<string, Provider>
  existingAliases: ModelAlias[]
  onChange: (next: ModelAlias) => void
  onSave: () => void
  onCancel: () => void
}) {
  const [touched, setTouched] = useState<{alias: boolean; providerId: boolean; providerModel: boolean}>({
    alias: false,
    providerId: false,
    providerModel: false,
  })
  const [saving, setSaving] = useState(false)
  const t = useT()

  function update<K extends keyof ModelAlias>(key: K, value: ModelAlias[K]) {
    onChange({...alias, [key]: value})
  }

  // Validation
  const errors = useMemo(() => {
    const e: {alias?: string; providerId?: string; providerModel?: string} = {}
    const trimmedAlias = alias.alias.trim()
    if (!trimmedAlias) {
      e.alias = t('models.validate.aliasRequired')
    } else {
      const duplicate = existingAliases.find(
        (a) => a.id !== alias.id && a.alias.toLowerCase() === trimmedAlias.toLowerCase(),
      )
      if (duplicate) {
        e.alias = t('models.validate.aliasDuplicate')
      }
    }
    if (!alias.providerId) {
      e.providerId = t('models.validate.providerRequired')
    }
    if (!alias.providerModel.trim()) {
      e.providerModel = t('models.validate.modelRequired')
    }
    return e
  }, [alias.alias, alias.providerId, alias.providerModel, alias.id, existingAliases, t])

  const canSave = !errors.alias && !errors.providerId && !errors.providerModel && !saving

  async function handleSaveClick() {
    // Mark all fields as touched to show any remaining errors
    setTouched({alias: true, providerId: true, providerModel: true})
    if (!canSave) return
    setSaving(true)
    try {
      await onSave()
    } finally {
      setSaving(false)
    }
  }

  // Pull the selected provider out of the map we received from the
  // parent (parent already memoizes the map by `providers`). Skipping
  // the inline `providers.find(...)` saves an O(n) scan per keystroke
  // for the (typically long) providers list.
  const selectedProvider = providerById.get(alias.providerId)
  // Preset list is a derived view of the selected provider, so it
  // gets memoized too — only the preset chips / datalist depend on it.
  const modelPresets = useMemo(
    () => getPresetsForProvider(selectedProvider),
    [selectedProvider],
  )
  const providerModelSuggestions = selectedProvider?.models ?? []

  return (
    <Form layout="vertical">
      <Form.Item
        label={t('models.form.alias')}
        required
        extra={t('models.form.alias.hint')}
        validateStatus={touched.alias && errors.alias ? 'error' : ''}
        help={touched.alias ? errors.alias : undefined}
      >
        <Input
          value={alias.alias}
          onChange={(e) => update('alias', e.target.value)}
          onBlur={() => setTouched((tt) => ({...tt, alias: true}))}
          placeholder={t('models.form.alias.placeholder')}
        />
      </Form.Item>

      <Form.Item
        label={t('models.form.provider')}
        required
        validateStatus={touched.providerId && errors.providerId ? 'error' : ''}
        help={touched.providerId ? errors.providerId : undefined}
      >
        <Select
          value={alias.providerId || undefined}
          onChange={(v) => {
            update('providerId', v)
            setTouched((tt) => ({...tt, providerId: true}))
          }}
          disabled={providers.length === 0}
          placeholder={providers.length === 0 ? t('models.form.provider.none') : t('models.form.provider.select')}
          options={providers.map((p) => ({
            value: p.id,
            label: `${p.name || t('models.form.untitled')} (${p.type})`,
          }))}
        />
      </Form.Item>

      {modelPresets.length > 0 && (
        <div>
          <Space align="center" className="mb-3!">
            <LuSparkles className="size-3.5 text-accent" />
            <span className="text-sm font-medium text-fg-muted!">{t('models.form.presets')}</span>
          </Space>
          <Space wrap size={4}>
            {modelPresets.map((model) => {
              const selected = alias.providerModel === model
              return (
                <Button
                  key={model}
                  size="small"
                  type={selected ? 'primary' : 'default'}
                  onClick={() => {
                    update('providerModel', model)
                    setTouched((tt) => ({...tt, providerModel: true}))
                  }}
                  className={cn('font-mono!')}
                >
                  {model}
                </Button>
              )
            })}
          </Space>
        </div>
      )}

      <Form.Item
        label={t('models.form.providerModel')}
        required
        extra={t('models.form.providerModel.hint')}
        validateStatus={touched.providerModel && errors.providerModel ? 'error' : ''}
        help={touched.providerModel ? errors.providerModel : undefined}
      >
        <Input
          value={alias.providerModel}
          onChange={(e) => update('providerModel', e.target.value)}
          onBlur={() => setTouched((tt) => ({...tt, providerModel: true}))}
          placeholder={t('models.form.providerModel.placeholder')}
          list={providerModelSuggestions.length > 0 ? 'provider-model-suggestions' : undefined}
        />
        {providerModelSuggestions.length > 0 && (
          <datalist id="provider-model-suggestions">
            {providerModelSuggestions.map((m) => (
              <option key={m} value={m} />
            ))}
          </datalist>
        )}
      </Form.Item>

      <Form.Item label={t('provider.form.tags')}>
        <Select
          mode="tags"
          value={alias.tags}
          onChange={(v) => update('tags', v)}
          placeholder={t('models.form.tags.placeholder')}
          className="w-full!"
        />
      </Form.Item>

      <Form.Item label={t('models.form.description')}>
        <Input.TextArea
          rows={2}
          value={alias.description}
          onChange={(e) => update('description', e.target.value)}
          placeholder={t('models.form.description.placeholder')}
        />
      </Form.Item>

      <Form.Item label={t('providers.form.enabled')} extra={t('models.form.enabled.desc')}>
        <Switch
          checked={alias.enabled}
          onChange={(v) => update('enabled', v)}
        />
      </Form.Item>

          <div className="mt-5! pt-5! border-t border-border!">
          <Space>
            <div style={{flex: 1}} />
            <Button onClick={onCancel}>{t('common.cancel')}</Button>
            <Button
              type="primary"
              loading={saving}
              disabled={!canSave}
              onClick={handleSaveClick}
              icon={<LuCircleCheck className="size-4" />}
            >
              {t('models.drawer.save')}
            </Button>
          </Space>
        </div>
    </Form>
  )
}
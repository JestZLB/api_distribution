import {memo, useCallback, useState} from 'react'
import {
  LuCopy,
  LuEye,
  LuEyeOff,
  LuKeyRound,
  LuPencil,
  LuPlus,
  LuTrash2,
  LuWand,
} from 'react-icons/lu'
import * as App from '../../../wailsjs/go/main/App'
import {App as AntApp, Button, Card, Input, Modal, Space, Switch, Typography} from 'antd'
import {EmptyState} from '@/components/ui/EmptyState'
import {maskKey} from '@/lib/format'
import {copyToClipboard} from '@/lib/clipboard'
import {newId} from '@/lib/id'
import {useT} from '@/i18n/useT'
import type {ClientKey} from '@/types'

interface ClientKeysCardProps {
  clientKeys: ClientKey[]
  onChange: (next: ClientKey[]) => void
}

function ClientKeysCardImpl({clientKeys, onChange}: ClientKeysCardProps) {
  const {message} = AntApp.useApp()
  const t = useT()

  // Per-key reveal toggle.
  const [revealed, setRevealed] = useState<Record<string, boolean>>({})

  // Add-key form state.
  const [addOpen, setAddOpen] = useState(false)
  const [addLabel, setAddLabel] = useState('')
  const [addKey, setAddKey] = useState('')
  const [addError, setAddError] = useState<string | null>(null)
  const [generating, setGenerating] = useState(false)
  const [saving, setSaving] = useState(false)

  // Inline label editing state.
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editingLabel, setEditingLabel] = useState('')

  const toggleReveal = useCallback((id: string) => {
    setRevealed((prev) => ({...prev, [id]: !prev[id]}))
  }, [])

  const openAdd = useCallback(() => {
    setAddLabel('')
    setAddKey('')
    setAddError(null)
    setAddOpen(true)
  }, [])

  const handleGenerate = useCallback(async () => {
    setGenerating(true)
    try {
      const next = await App.GenerateClientKey()
      setAddKey(next)
    } catch (e) {
      message.error(`${t('settings.clientKeys.generate')}: ${String(e)}`)
    } finally {
      setGenerating(false)
    }
  }, [t, message])

  const handleAddSave = useCallback(async () => {
    const label = addLabel.trim()
    if (!label) {
      setAddError(t('settings.clientKeys.labelRequired'))
      return
    }
    if (clientKeys.some((ck) => ck.label === label)) {
      setAddError(t('settings.clientKeys.labelDuplicate'))
      return
    }
    let key = addKey.trim()
    if (!key) {
      // Auto-generate when the key is empty.
      try {
        key = await App.GenerateClientKey()
      } catch (e) {
        message.error(`${t('settings.clientKeys.generate')}: ${String(e)}`)
        return
      }
    }
    if (key.length < 8) {
      setAddError(t('settings.clientKeys.keyTooShort'))
      return
    }
    setSaving(true)
    try {
      const next: ClientKey = {
        id: newId('ck'),
        label,
        key,
        enabled: true,
        createdAt: Date.now() * 1_000_000,
      }
      onChange([...clientKeys, next])
      setAddOpen(false)
      message.success(t('settings.clientKeys.save'))
    } finally {
      setSaving(false)
    }
  }, [addLabel, addKey, clientKeys, onChange, t, message])

  const startEditLabel = useCallback((ck: ClientKey) => {
    setEditingId(ck.id)
    setEditingLabel(ck.label)
  }, [])

  const commitEditLabel = useCallback(
    (ck: ClientKey) => {
      const label = editingLabel.trim()
      if (label && label !== ck.label) {
        if (clientKeys.some((other) => other.id !== ck.id && other.label === label)) {
          message.error(t('settings.clientKeys.labelDuplicate'))
          setEditingId(null)
          return
        }
        onChange(clientKeys.map((item) => (item.id === ck.id ? {...item, label} : item)))
      }
      setEditingId(null)
    },
    [editingLabel, clientKeys, onChange, t, message],
  )

  const toggleEnabled = useCallback(
    (ck: ClientKey, enabled: boolean) => {
      onChange(clientKeys.map((item) => (item.id === ck.id ? {...item, enabled} : item)))
    },
    [clientKeys, onChange],
  )

  const handleDelete = useCallback(
    (ck: ClientKey) => {
      onChange(clientKeys.filter((item) => item.id !== ck.id))
    },
    [clientKeys, onChange],
  )

  return (
    <Card
      title={t('settings.clientKeys')}
      variant="outlined"
      extra={
        <Button
          size="small"
          type="primary"
          onClick={openAdd}
          icon={<LuPlus className="size-3.5" />}
        >
          {t('settings.clientKeys.add')}
        </Button>
      }
    >
      <Typography.Text type="secondary" className="block">
        {t('settings.clientKeys.desc')}
      </Typography.Text>
      <div className="mt-4">
        {clientKeys.length === 0 ? (
          <EmptyState
            icon={<LuKeyRound className="size-10 mx-auto text-fg-subtle" />}
            title={t('settings.clientKeys.empty')}
            description={t('settings.clientKeys.emptyDesc')}
            action={
              <Button
                size="small"
                type="primary"
                onClick={openAdd}
                icon={<LuPlus className="size-3.5" />}
              >
                {t('settings.clientKeys.add')}
              </Button>
            }
          />
        ) : (
          <ul className="space-y-1.5">
            {clientKeys.map((ck) => (
              <li
                key={ck.id}
                className="flex items-center gap-2 px-3 py-2 rounded-lg border border-border bg-bg-subtle"
              >
                {editingId === ck.id ? (
                  <Input
                    size="small"
                    autoFocus
                    value={editingLabel}
                    onChange={(e) => setEditingLabel(e.target.value)}
                    onBlur={() => commitEditLabel(ck)}
                    onPressEnter={() => commitEditLabel(ck)}
                    onKeyDown={(e) => {
                      if (e.key === 'Escape') setEditingId(null)
                    }}
                    className="w-40!"
                  />
                ) : (
                  <button
                    type="button"
                    onClick={() => startEditLabel(ck)}
                    className="group inline-flex items-center gap-1 text-sm font-medium text-fg hover:text-primary"
                    title={t('settings.clientKeys.label')}
                  >
                    <span className="truncate max-w-40">{ck.label}</span>
                    <LuPencil className="size-3 text-fg-subtle opacity-0 group-hover:opacity-100" />
                  </button>
                )}
                <Typography.Text code className="hidden md:inline-flex truncate max-w-40 text-xs">
                  {revealed[ck.id] ? ck.key : maskKey(ck.key)}
                </Typography.Text>
                <Space size={2} className="ml-auto">
                  <Button
                    type="text"
                    size="small"
                    aria-label={revealed[ck.id] ? t('settings.clientKeys.hide') : t('settings.clientKeys.reveal')}
                    onClick={() => toggleReveal(ck.id)}
                    icon={revealed[ck.id] ? <LuEyeOff className="size-4" /> : <LuEye className="size-4" />}
                  />
                  <Button
                    type="text"
                    size="small"
                    aria-label={t('settings.clientKeys.copy')}
                    onClick={() => copyToClipboard(ck.key, ck.label)}
                    icon={<LuCopy className="size-4" />}
                  />
                  <Switch
                    size="small"
                    checked={ck.enabled}
                    onChange={(v) => toggleEnabled(ck, v)}
                  />
                  <Button
                    type="text"
                    size="small"
                    danger
                    aria-label={t('settings.clientKeys.delete')}
                    onClick={() => handleDelete(ck)}
                    icon={<LuTrash2 className="size-4" />}
                  />
                </Space>
              </li>
            ))}
          </ul>
        )}
      </div>

      <Modal
        open={addOpen}
        title={t('settings.clientKeys.add')}
        onCancel={() => setAddOpen(false)}
        footer={null}
        width={420}
      >
        <div className="space-y-4 pt-2">
          <div>
            <Typography.Text className="text-sm">{t('settings.clientKeys.label')}</Typography.Text>
            <Input
              className="mt-1!"
              value={addLabel}
              onChange={(e) => {
                setAddLabel(e.target.value)
                setAddError(null)
              }}
              placeholder={t('settings.clientKeys.label')}
            />
          </div>
          <div>
            <Typography.Text className="text-sm">{t('settings.clientKeys.key')}</Typography.Text>
            <Space.Compact className="mt-1! w-full">
              <Input
                value={addKey}
                onChange={(e) => {
                  setAddKey(e.target.value)
                  setAddError(null)
                }}
                placeholder={t('settings.clientKeys.key')}
              />
              <Button
                loading={generating}
                onClick={handleGenerate}
                icon={<LuWand className="size-3.5" />}
              >
                {t('settings.clientKeys.generate')}
              </Button>
            </Space.Compact>
          </div>
          {addError && (
            <Typography.Text type="danger" className="text-xs!">
              {addError}
            </Typography.Text>
          )}
          <div className="flex justify-end gap-2 pt-2">
            <Button onClick={() => setAddOpen(false)}>{t('common.cancel')}</Button>
            <Button type="primary" loading={saving} onClick={handleAddSave}>
              {t('settings.clientKeys.save')}
            </Button>
          </div>
        </div>
      </Modal>
    </Card>
  )
}

export const ClientKeysCard = memo(ClientKeysCardImpl)

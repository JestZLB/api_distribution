import {toast} from '@/store/toast'
import {useLocaleStore} from '@/store/locale'
import {messages} from '@/i18n/locales'

function t(key: string, params?: Record<string, string | number>): string {
  const locale = useLocaleStore.getState().locale
  const template = messages[locale]?.[key] ?? messages['en-US'][key] ?? key
  if (!params) return template
  return template.replace(/\{\{(\w+)\}\}/g, (_, name) => {
    const v = params[name]
    return v === undefined || v === null ? `{{${name}}}` : String(v)
  })
}

function maskValueForToast(v: string): string {
  if (v.length <= 16) return ''
  return `${v.slice(0, 4)}…${v.slice(-4)}`
}

export async function copyToClipboard(value: string, label = 'Value'): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value)
    } else {
      const ta = document.createElement('textarea')
      ta.value = value
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    toast.success(t('clipboard.copied', {label}), maskValueForToast(value))
    return true
  } catch {
    toast.error(t('clipboard.failed', {label}))
    return false
  }
}
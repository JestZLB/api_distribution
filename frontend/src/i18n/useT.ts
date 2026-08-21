import {useMemo} from 'react'
import {useLocaleStore} from '@/store/locale'
import {messages} from '@/i18n/locales'

// Tiny translation hook. Resolves `key` in the active locale, falls
// back to English, and finally returns the raw key when no
// translation is available. Placeholders of the form `{{name}}`
// in the translated string are replaced from the optional `params`
// map.
export function useT() {
  const locale = useLocaleStore((s) => s.locale)
  return useMemo(() => {
    return (key: string, params?: Record<string, string | number>): string => {
      const fromLocale = messages[locale]?.[key]
      const template =
        fromLocale ?? messages['en-US'][key] ?? key
      if (!params) return template
      return template.replace(/\{\{(\w+)\}\}/g, (_, name) => {
        const v = params[name]
        return v === undefined || v === null ? `{{${name}}}` : String(v)
      })
    }
  }, [locale])
}
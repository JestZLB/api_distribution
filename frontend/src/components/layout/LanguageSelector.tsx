import {Select as AntSelect} from 'antd'
import {useLocaleStore} from '@/store/locale'
import {useT} from '@/i18n/useT'

// Language selector backed by antd Select. Lives at top-right of the
// topbar next to the theme toggle.
export function LanguageSelector() {
  const locale = useLocaleStore((s) => s.locale)
  const setLocale = useLocaleStore((s) => s.setLocale)
  const t = useT()
  return (
    <AntSelect
      value={locale}
      onChange={(v) => setLocale(v)}
      size="small"
      style={{minWidth: 120}}
      options={[
        {value: 'zh-CN', label: '简体中文'},
        {value: 'en-US', label: 'English'},
        {value: 'ja-JP', label: '日本語'},
        {value: 'ko-KR', label: '한국어'},
      ]}
      aria-label={t('common.locale')}
      variant="borderless"
    />
  )
}
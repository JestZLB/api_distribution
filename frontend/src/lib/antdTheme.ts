import {theme, type ThemeConfig} from 'antd'

/**
 * Generate the antd ThemeConfig for the given resolved mode.
 *
 * Visual direction: Slate-canvas developer tool (Linear / Vercel /
 * Supabase family). The previous bright antd green / saturated violet
 * accent read as a generic admin template — big-tech developer tools
 * use a cool slate base with one small deep-indigo accent and desaturated
 * status colours that never compete with the primary for attention.
 */
export function getAntdTheme(mode: 'light' | 'dark'): ThemeConfig {
  const isDark = mode === 'dark'
  return {
    algorithm: isDark ? theme.darkAlgorithm : theme.defaultAlgorithm,
    cssVar: {key: 'api-distribution'},
    token: {
      // Primary — a single deep indigo. Light uses indigo-600 so the
      // button / focus ring reads confidently on the slate-50 layout;
      // dark uses indigo-500 which stays vivid against slate-950
      // without going neon.
      colorPrimary: isDark ? '#6366F1' : '#4F46E5',
      // Status palette desaturated. Default antd greens/ambers looked
      // like sticker badges against the slate canvas; these sit one
      // shade darker and share the cool hue family with the primary.
      colorSuccess: isDark ? '#22C55E' : '#16A34A',
      colorWarning: isDark ? '#F59E0B' : '#D97706',
      colorError: isDark ? '#EF4444' : '#DC2626',
      colorInfo: isDark ? '#38BDF8' : '#0284C7',
      // Slate canvases replace its warm grey. This single move is the
      // biggest aesthetic shift toward Linear/Vercel/Supabase feel —
      // a cool grey base makes every panel feel intentional rather
      // than "default theme".
      colorBgLayout: isDark ? '#0B0F19' : '#F8FAFC',
      colorBgContainer: isDark ? '#111827' : '#FFFFFF',
      colorBgElevated: isDark ? '#111827' : '#FFFFFF',
      borderRadius: 8,
      fontSize: 14,
      lineWidth: 1,
    },
    // Lock the body surface to transparent so the slate canvas sits
    // behind without competing colour.
    components: {
      Layout: {
        colorBgBody: 'transparent',
      },
    },
  }
}
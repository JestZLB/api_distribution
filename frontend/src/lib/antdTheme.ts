import {theme, type ThemeConfig} from 'antd'

/**
 * Generate the antd ThemeConfig for the given resolved mode.
 *
 * The accent color is the only design token we still surface from the
 * project's CSS Variables; everything else (radii, shadows, control
 * heights) is delegated to antd's defaults so the app visually matches
 * the antd documentation.
 */
export function getAntdTheme(mode: 'light' | 'dark'): ThemeConfig {
  const isDark = mode === 'dark'
  return {
    algorithm: isDark ? theme.darkAlgorithm : theme.defaultAlgorithm,
    // Emit antd design tokens as CSS variables (--ant-*) on :root so the
    // Tailwind color utilities in style.css can reference the same palette
    // and stay in sync with the active light/dark theme.
    cssVar: {key: 'api-distribution'},
    token: {
      // Soft green (antd default green). Light: #52c41a, Dark: #49aa19.
      colorPrimary: isDark ? '#49aa19' : '#52c41a',
      borderRadius: 8,
      fontSize: 14,
    },
  }
}
import {cn} from '@/lib/cn'

export interface StatusDotProps {
  tone: 'online' | 'offline'
  pulsing?: boolean
}

export function StatusDot({tone, pulsing}: StatusDotProps) {
  const color = tone === 'online' ? 'bg-success' : 'bg-fg-subtle'
  return (
    <span
      className={cn(
        'relative inline-flex size-2.5 items-center justify-center rounded-full',
        color,
      )}
      aria-hidden
    >
      {pulsing && (
        <span className="absolute inset-0 rounded-full bg-success/40 animate-ping" />
      )}
    </span>
  )
}
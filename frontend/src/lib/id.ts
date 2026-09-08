export function newId(prefix = 'id'): string {
  return `${prefix}-${crypto.randomUUID()}`
}

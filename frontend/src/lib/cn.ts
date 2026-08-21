// Classname combinator. Joins truthy values, recursively flattens arrays,
// and drops falsy entries (except `0`, which is stringified as `"0"`).
// Tailwind class conflicts are not collapsed; callers should order classes
// so the desired one comes last.

type ClassValue = string | number | false | null | undefined | ClassValue[]

export function cn(...inputs: ClassValue[]): string {
  const out: string[] = []
  const push = (v: ClassValue) => {
    if (!v && v !== 0) return
    if (Array.isArray(v)) v.forEach(push)
    else out.push(String(v))
  }
  inputs.forEach(push)
  return out.join(' ')
}
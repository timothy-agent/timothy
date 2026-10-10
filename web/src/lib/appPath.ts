import { appRoutes } from '../routes'

// matchesPattern tests one react-router style pattern against segments:
// `:name` is one segment, `:name?` is optional, a trailing `/*` is any suffix.
function matchesPattern(pattern: string, segs: string[]): boolean {
  const parts = pattern.split('/').filter(Boolean)
  let i = 0
  for (let p = 0; p < parts.length; p++) {
    const part = parts[p]
    if (part === '*') return p === parts.length - 1
    if (part.startsWith(':') && part.endsWith('?')) {
      if (i < segs.length) i++
    } else if (part.startsWith(':')) {
      if (i >= segs.length) return false
      i++
    } else if (segs[i++] !== part) {
      return false
    }
  }
  return i === segs.length
}

// isAppPath reports whether href is an in-app path: it starts with a
// single `/`, carries no scheme, and matches one route in appRoutes.
// Any query string or hash is ignored for matching.
export function isAppPath(href: string): boolean {
  if (!href.startsWith('/') || href.startsWith('//') || href.includes('\\')) return false
  const path = href.split(/[?#]/)[0]
  const segs = path.split('/').filter(Boolean)
  return appRoutes.some((r) => matchesPattern(r.path, segs))
}

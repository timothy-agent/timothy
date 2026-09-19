// slugify derives an ASCII-safe suggestion or secret-ref fragment from
// free text; it is no longer the backend's name shape rule.
export function slugify(v: string): string {
  return v
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

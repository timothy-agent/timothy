import { useEffect, useState } from 'react'

// Module-level cache: a picker can render in every agent form; the
// underlying list (skills, tools, knowledge collections) changes
// rarely and a stale list self-heals on the next mount. Keyed so one
// module serves every list without cross-contaminating caches.
const cache = new Map<string, unknown[]>()
const failedKeys = new Set<string>()

// useCachedList fetches once per key for the module's lifetime and
// remembers a failure so callers can fall back (e.g. to free text)
// without retrying every render or showing a toast.
export function useCachedList<T>(key: string, fetcher: () => Promise<T[]>): { items: T[]; failed: boolean } {
  const [items, setItems] = useState<T[]>((cache.get(key) as T[] | undefined) ?? [])
  const [failed, setFailed] = useState(failedKeys.has(key))

  useEffect(() => {
    if (cache.has(key) || failedKeys.has(key)) return
    fetcher().then(
      (list) => {
        cache.set(key, list)
        setItems(list)
      },
      () => {
        failedKeys.add(key)
        setFailed(true)
      },
    )
    // key/fetcher identify the list to load; re-running on fetcher
    // identity changes would defeat the module-level cache.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  return { items, failed }
}

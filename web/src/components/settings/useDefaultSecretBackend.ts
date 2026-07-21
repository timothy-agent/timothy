import { useEffect, useState } from 'react'
import { listSecretBackends } from '../../api/client'

// useDefaultSecretBackend resolves which backend credential writes
// route through, so key inputs can ask for a value or a reference.
// Falls back to built-in storage while loading or on error.
export function useDefaultSecretBackend(): string {
  const [backend, setBackend] = useState('db')
  useEffect(() => {
    listSecretBackends().then(
      (bs) => setBackend(bs.find((b) => b.default)?.backend ?? 'db'),
      () => undefined,
    )
  }, [])
  return backend
}

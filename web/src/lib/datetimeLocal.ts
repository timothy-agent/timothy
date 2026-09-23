// Converts between a stored UTC ISO timestamp and a datetime-local input
// value in the browser's zone, so an untouched value round-trips exactly.

const pad = (n: number) => String(n).padStart(2, '0')

// isoToLocalInput renders iso as 'YYYY-MM-DDTHH:mm' local time; '' for empty.
export function isoToLocalInput(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// localInputToIso reads a datetime-local value as local time and returns UTC ISO.
export function localInputToIso(local: string): string {
  return new Date(local).toISOString()
}

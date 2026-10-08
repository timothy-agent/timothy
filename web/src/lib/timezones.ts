// FALLBACK_TIMEZONES stands in for Intl.supportedValuesOf('timeZone')
// when that API is unavailable (older test environments): a short,
// common list, not an attempt to cover every IANA zone.
const FALLBACK_TIMEZONES = [
  'UTC',
  'Europe/Amsterdam',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
  'America/Chicago',
  'Asia/Kolkata',
  'Asia/Dhaka',
  'Asia/Tokyo',
  'Asia/Shanghai',
  'Australia/Sydney',
]

export function listTimezones(): string[] {
  try {
    return Intl.supportedValuesOf('timeZone')
  } catch {
    return FALLBACK_TIMEZONES
  }
}

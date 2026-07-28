// cronPresets covers the common recurring shapes the schedule dialog
// offers directly; 'custom' has no cron value of its own — it means
// "let the user type one," matched last by presetFor.
export const cronPresets = [
  { value: 'daily-7am', label: 'Daily, 7:00 AM', cron: '0 7 * * *' },
  { value: 'weekdays-8am', label: 'Weekdays, 8:00 AM', cron: '0 8 * * 1-5' },
  { value: 'hourly', label: 'Hourly', cron: '0 * * * *' },
  { value: 'custom', label: 'Custom', cron: null },
] as const

export type CronPresetValue = (typeof cronPresets)[number]['value']

// presetFor maps a stored cron expression back to the preset that
// produced it, so editing a schedule reopens the same preset it was
// created with rather than always falling back to "Custom".
export function presetFor(cron: string): CronPresetValue {
  const match = cronPresets.find((p) => p.cron === cron)
  return match?.value ?? 'custom'
}

// describeCron renders a cron expression as plain English for the
// handful of shapes this UI ever produces; anything else is shown
// verbatim (still valid cron, just not one of our presets).
export function describeCron(cron: string): string {
  const preset = cronPresets.find((p) => p.cron === cron)
  if (preset) return preset.label
  return cron
}

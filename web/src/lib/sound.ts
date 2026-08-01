const soundKey = 'timothy.notificationSound'

// getNotificationSoundEnabled/setNotificationSoundEnabled mirror
// theme.ts's localStorage get/set pattern; enabled by default so a
// fresh install still alerts on a parked permission ask.
export function getNotificationSoundEnabled(): boolean {
  return localStorage.getItem(soundKey) !== 'off'
}

export function setNotificationSoundEnabled(enabled: boolean) {
  localStorage.setItem(soundKey, enabled ? 'on' : 'off')
}

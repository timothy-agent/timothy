const key = 'timothy.transcribeLanguage'

// getTranscribeLanguage/setTranscribeLanguage mirror sound.ts's
// localStorage get/set pattern. Empty string means auto-detect, the
// default — whisper's own language guess is wrong often enough for
// some languages that a sticky override is worth persisting.
export function getTranscribeLanguage(): string {
  return localStorage.getItem(key) ?? ''
}

export function setTranscribeLanguage(language: string) {
  localStorage.setItem(key, language)
}

// A short curated list, not an exhaustive ISO 639-1 table — whisper
// supports far more, but the mic button needs a pickable list, not a
// search box.
export const TRANSCRIBE_LANGUAGES: { code: string; label: string }[] = [
  { code: 'en', label: 'English' },
  { code: 'bn', label: 'Bangla' },
  { code: 'hi', label: 'Hindi' },
  { code: 'es', label: 'Spanish' },
  { code: 'fr', label: 'French' },
  { code: 'de', label: 'German' },
  { code: 'nl', label: 'Dutch' },
  { code: 'ar', label: 'Arabic' },
  { code: 'zh', label: 'Chinese' },
  { code: 'ja', label: 'Japanese' },
  { code: 'ru', label: 'Russian' },
]

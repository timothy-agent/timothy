// Shared choices for a github destination's mode/commit-style pickers,
// used by both the mission form's github destination fields and the
// Settings → Destinations github kind (issue #560).

// Sentinel for a Select's "apply the settings default"/"do nothing"
// choice: Radix Select.Item rejects an empty string value, so the wire
// value ('' for commit style, '' for mode-off) is represented by a
// sentinel on the Select itself.
export const COMMIT_STYLE_DEFAULT = '__default__'
export const ON_COMPLETE_NONE = '__none__'

export const commitStyleChoices: { value: string; label: string }[] = [
  { value: COMMIT_STYLE_DEFAULT, label: 'Default (from settings)' },
  { value: 'conventional', label: 'Conventional' },
  { value: 'plain', label: 'Plain' },
]

export const onCompleteChoices: { value: string; label: string }[] = [
  { value: ON_COMPLETE_NONE, label: 'Nothing' },
  { value: 'push', label: 'Push branch when done' },
  { value: 'push_pr', label: 'Push and open a PR when done' },
]
